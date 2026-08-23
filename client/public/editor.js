// A code editor made out of a textarea: YAML highlighting, indent-aware keys,
// and Cmd/Ctrl+/ to comment a selection.
//
// It stays a <textarea> on purpose. Every caller in guard reads and writes
// `.value` — deploys.js fills the compose field, derives the image from it and
// posts it — and a contenteditable rewrite would break all of them at once
// while also throwing away the browser's own undo stack, spellcheck plumbing
// and form semantics. So the highlighting is a <pre> painted *behind* the
// textarea, whose own text is transparent and whose caret and selection are
// not. Nothing above this file has to know it happened.
//
// The one hard rule: the <pre> and the <textarea> must agree on every metric
// that can move a glyph — font, size, line height, letter spacing, padding,
// tab size and wrapping. One disagreement and the highlight slides out from
// under the caret a few characters in, which is the failure everybody who has
// built one of these has shipped once. Those metrics are set together in
// client/styles/app.css under [data-code-editor], never inherited from
// `cn-textarea` on one side only.

const INDENT = "  "; // two spaces: what docker compose files are written in.

// Above this, highlighting is dropped and the plain textarea shows through.
// A compose file is a few kilobytes; anything at this size is a paste nobody
// wants to watch the browser tokenise on every keystroke.
const PAINT_LIMIT = 200_000;

const attached = new WeakMap();

// ------------------------------------------------------------------ painting

const ESCAPES = { "&": "&amp;", "<": "&lt;", ">": "&gt;" };
const escape = (value) => String(value).replace(/[&<>]/g, (c) => ESCAPES[c]);
const span = (kind, value) => `<span class="tok-${kind}">${escape(value)}</span>`;

// `${TAG}` is the one thing in a guard compose file that is not just text: it
// is what the deploy substitutes, and a template that never mentions it is
// refused by the server. So interpolations are a token of their own wherever
// they appear — inside a quoted string too, which is where they usually are.
const VARIABLE = /\$\{[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*/g;

function withVariables(kind, value) {
  let out = "";
  let at = 0;
  VARIABLE.lastIndex = 0;
  for (let hit = VARIABLE.exec(value); hit; hit = VARIABLE.exec(value)) {
    if (hit.index > at) out += span(kind, value.slice(at, hit.index));
    out += span("var", hit[0]);
    at = hit.index + hit[0].length;
  }
  if (at < value.length) out += span(kind, value.slice(at));
  return out;
}

// Where an inline comment starts, or -1. A `#` only opens one at the start of
// a token — `syd.vultrcr.com/hushkey/pack:v1#2` is a tag, not a comment — and
// never inside quotes.
function commentAt(value) {
  let quote = "";
  for (let i = 0; i < value.length; i++) {
    const c = value[i];
    if (quote) {
      if (c === "\\" && quote === '"') i++;
      else if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'") { quote = c; continue; }
    if (c === "#" && (i === 0 || value[i - 1] === " " || value[i - 1] === "\t")) return i;
  }
  return -1;
}

const CONSTANTS = /^(true|false|yes|no|on|off|null|~)$/i;
const NUMBER = /^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$/;

function paintScalar(value) {
  const body = value.trimEnd();
  const tail = value.slice(body.length);
  if (!body) return escape(value);
  if (/^[|>][-+]?\d*$/.test(body)) return span("punct", body) + escape(tail);
  if (/^[&*!]/.test(body)) return span("anchor", body) + escape(tail);
  if (/^"/.test(body) || /^'/.test(body)) return withVariables("string", body) + escape(tail);
  if (NUMBER.test(body)) return span("number", body) + escape(tail);
  if (CONSTANTS.test(body)) return span("const", body) + escape(tail);
  return withVariables("plain", body) + escape(tail);
}

function paintValue(value) {
  const lead = /^[ \t]*/.exec(value)[0];
  let body = value.slice(lead.length);
  let comment = "";
  const at = commentAt(body);
  if (at >= 0) { comment = body.slice(at); body = body.slice(0, at); }
  return escape(lead) + paintScalar(body) + (comment ? span("comment", comment) : "");
}

// A key ends at the first `:` that is followed by whitespace or the end of the
// line. That is YAML's own rule, and it is why `image: registry:5000/app` is a
// key of `image` rather than of `image: registry`.
const KEY = /^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^#]*?)(:)([ \t].*|)$/;

function paintLine(line) {
  const indent = /^[ \t]*/.exec(line)[0];
  let rest = line.slice(indent.length);
  let out = escape(indent);
  if (!rest) return out;
  if (rest.startsWith("#")) return out + span("comment", rest);

  // Sequence markers, and there can be more than one on a line.
  for (let hit = /^-([ \t]+|$)/.exec(rest); hit; hit = /^-([ \t]+|$)/.exec(rest)) {
    out += span("punct", "-") + escape(hit[1]);
    rest = rest.slice(hit[0].length);
  }
  if (!rest) return out;
  if (rest.startsWith("#")) return out + span("comment", rest);

  const key = KEY.exec(rest);
  if (!key) return out + paintValue(rest);
  return out + withVariables("key", key[1]) + span("punct", ":") + paintValue(key[3]);
}

// Block scalars are the reason this walks the whole text rather than mapping
// over lines independently: the shell script inside `command: |` in a compose
// file is text, and tokenising it as YAML turns `pack.hushkey.app:80 {` into a
// key — the one place the highlighting would be confidently wrong.
export function paintYAML(text) {
  const lines = text.split("\n");
  const out = [];
  let block = -1; // indent of the line that opened the block, or -1.
  for (const line of lines) {
    if (block >= 0) {
      const indent = /^[ \t]*/.exec(line)[0].length;
      if (!line.trim() || indent > block) { out.push(span("string", line)); continue; }
      block = -1;
    }
    out.push(paintLine(line));
    const opener = /^([ \t]*)(?:-[ \t]+)*(?:[^#]*?:[ \t]+)?[|>][-+]?\d*[ \t]*$/.exec(line);
    if (opener) block = opener[1].length;
  }
  // A trailing newline keeps the painted text as tall as the textarea's, so a
  // scroll to the very bottom lines up instead of stopping a line short.
  return out.join("\n") + "\n";
}

// ------------------------------------------------------------------- editing

// Write through execCommand where it exists. It is deprecated and it is still
// the only way to change a textarea without emptying the browser's undo stack
// — and an editor where Cmd+Z stops working the moment you press Tab is worse
// than one with no Tab handling at all.
function replace(field, start, end, text) {
  field.focus();
  field.setSelectionRange(start, end);
  let wrote = false;
  try {
    wrote = document.execCommand("insertText", false, text);
  } catch {
    wrote = false;
  }
  if (!wrote) {
    field.setRangeText(text, start, end, "end");
    field.dispatchEvent(new Event("input", { bubbles: true }));
  }
}

// The whole lines the selection touches, because indenting and commenting are
// line operations however little of the line is selected.
function lineBlock(field) {
  const value = field.value;
  const start = value.lastIndexOf("\n", field.selectionStart - 1) + 1;
  let to = field.selectionEnd;
  // A drag that ends on the next line's first column has not touched that
  // line, and commenting it would surprise whoever selected three services
  // and got four.
  if (to > field.selectionStart && to > 0 && value[to - 1] === "\n") to--;
  let end = value.indexOf("\n", to);
  if (end < 0) end = value.length;
  return { start, end, lines: value.slice(start, end).split("\n") };
}

function applyBlock(field, block, lines) {
  const text = lines.join("\n");
  if (text === field.value.slice(block.start, block.end)) return;
  replace(field, block.start, block.end, text);
  field.setSelectionRange(block.start, block.start + text.length);
}

// The two line transforms are exported and take plain arrays, so the rules
// below can be asserted without a DOM — client/public/editor_test.mjs. What is
// left in the handlers is the part that genuinely needs a textarea.

export function shiftBlock(lines, out) {
  return lines.map((line) => {
    if (out) return line.replace(/^(?: {1,2}|\t)/, "");
    return line.trim() ? INDENT + line : line;
  });
}

// Toggle, not comment: if every line that has anything on it is already
// commented, this uncomments. `#` goes in at the shallowest indent of the
// block rather than at column zero, so commenting out a service inside a
// compose file leaves the structure readable — and a blank line in the middle
// of the selection stays blank, because a lone `#` is noise nobody typed.
export function commentBlock(lines) {
  const filled = lines.filter((line) => line.trim());
  if (!filled.length) return lines;
  if (filled.every((line) => /^[ \t]*#/.test(line))) {
    return lines.map((line) => line.replace(/^([ \t]*)#[ ]?/, "$1"));
  }
  const column = Math.min(...filled.map((line) => /^[ \t]*/.exec(line)[0].length));
  return lines.map((line) =>
    line.trim() ? line.slice(0, column) + "# " + line.slice(column) : line);
}

// Enter keeps the current indentation, and adds a level after a line that
// opened something — `services:`, `- ` or a block scalar. Typing the same two
// spaces by hand forty times is how a compose file ends up with three
// different indent widths in it.
function newline(field) {
  const before = field.value.slice(0, field.selectionStart);
  const line = before.slice(before.lastIndexOf("\n") + 1);
  if (/^[ \t]*#/.test(line)) return false; // let a comment line break plainly.
  let indent = /^[ \t]*/.exec(line)[0];
  const body = line.trim();
  if (/[:-]$/.test(body) || /[|>][-+]?\d*$/.test(body)) indent += INDENT;
  replace(field, field.selectionStart, field.selectionEnd, "\n" + indent);
  return true;
}

// ----------------------------------------------------------------- formatting

// Deliberately not a YAML parse-and-dump. Round-tripping this file through a
// serialiser would sort keys, normalise quotes and drop every comment — and
// the comments in a compose file are where the deployment notes live. So this
// only touches whitespace no reader can see, and says when it changed nothing.
export function formatYAML(text) {
  const lines = text.replace(/\r\n?/g, "\n").split("\n").map((line) => {
    const indent = /^[ \t]*/.exec(line)[0];
    return indent.replace(/\t/g, INDENT) + line.slice(indent.length).trimEnd();
  });
  const out = [];
  for (const line of lines) {
    if (!line && !out.length) continue;                       // no leading blanks
    if (!line && !out[out.length - 1]) continue;              // never two in a row
    out.push(line);
  }
  while (out.length && !out[out.length - 1]) out.pop();
  return out.length ? out.join("\n") + "\n" : "";
}

// ------------------------------------------------------------------ attaching

// Idempotent: the template dialog is opened many times over a session and the
// markup outlives every one of them, so attaching twice must not mean painting
// twice or two keydown handlers fighting over one Tab.
export function attachEditor(field) {
  if (!field) return null;
  const existing = attached.get(field);
  if (existing) return existing;

  const shell = field.closest("[data-code-editor]");
  const ink = shell && shell.querySelector("[data-code-ink]");
  if (!shell || !ink) return null;

  const paint = () => {
    const value = field.value;
    if (value.length > PAINT_LIMIT) { ink.textContent = value; return; }
    ink.innerHTML = paintYAML(value);
    ink.parentElement.scrollTop = field.scrollTop;
    ink.parentElement.scrollLeft = field.scrollLeft;
  };

  // addEventListener rather than .oninput: deploys.js already owns .oninput on
  // this field to keep the derived line up to date, and the last assignment
  // would silently win.
  field.addEventListener("input", paint);
  field.addEventListener("scroll", () => {
    ink.parentElement.scrollTop = field.scrollTop;
    ink.parentElement.scrollLeft = field.scrollLeft;
  });

  field.addEventListener("keydown", (event) => {
    if (event.key === "Tab") {
      event.preventDefault();
      const spans = field.selectionStart !== field.selectionEnd &&
        field.value.slice(field.selectionStart, field.selectionEnd).includes("\n");
      if (event.shiftKey || spans) {
        const block = lineBlock(field);
        applyBlock(field, block, shiftBlock(block.lines, event.shiftKey));
      }
      else replace(field, field.selectionStart, field.selectionEnd, INDENT);
      paint();
      return;
    }
    // The editor shortcut everybody already has in their fingers. Cmd on a
    // Mac, Ctrl elsewhere; `event.code` because on several keyboard layouts
    // `/` is a shifted key and `event.key` is then not "/".
    if ((event.metaKey || event.ctrlKey) && (event.key === "/" || event.code === "Slash")) {
      event.preventDefault();
      const block = lineBlock(field);
      applyBlock(field, block, commentBlock(block.lines));
      paint();
      return;
    }
    if (event.key === "Enter" && !event.shiftKey && !event.metaKey && !event.ctrlKey) {
      if (newline(field)) { event.preventDefault(); paint(); }
    }
  });

  const instance = {
    field,
    refresh: paint,
    format() {
      const formatted = formatYAML(field.value);
      if (formatted === field.value) return false;
      // Through the same write path as every keystroke, so Cmd+Z undoes a
      // format in one press like it undoes anything else.
      replace(field, 0, field.value.length, formatted);
      paint();
      return true;
    },
  };
  attached.set(field, instance);
  paint();
  return instance;
}
