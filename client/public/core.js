// Shared by guard.js, charts.js and views.js.
//
// These lived in guard.js until the views page needed them too. Everything here
// is either a DOM shorthand, a formatter, or the one fetch wrapper — no state,
// nothing that renders. Modules that render import from here; this imports from
// nothing.
//
// Note for anyone adding a Tailwind class in a JavaScript file: the @source
// list in the Makefile's css target has to see the file, and the class has to
// appear as a literal string. A class assembled from a variable is a class
// Tailwind cannot find, and it will simply not be in the bundle.

export const number = new Intl.NumberFormat();
export const svgNS = "http://www.w3.org/2000/svg";

// Eight hues that stay distinguishable on the dark surface and keep their
// order — series colours are assigned by index, so a series must not change
// colour because another one appeared above it.
export const palette = ["#5bd8a6", "#60a5fa", "#c084fc", "#f59e0b", "#fb7185", "#22d3ee", "#a3e635", "#f472b6"];

export const muted = "text-muted-foreground";

export const text = (value) => document.createTextNode(value ?? "");
export const qs = (selector, root = document) => root.querySelector(selector);
export const qsa = (selector, root = document) => [...root.querySelectorAll(selector)];

// The session's state lives in store.js — outside the outlet, so it survives
// navigation. It is imported from there rather than re-exported here: a
// renderer that reads the store should say so in its import line.

export async function request(path, options = {}) {
  const response = await fetch(path, { headers: { Accept: "application/json", ...(options.headers || {}) }, ...options });
  // The session ended — expired, signed out in another tab, or removed from the
  // members list. The dashboard polls every three seconds, so this is what
  // notices, and the only sensible answer is the login page: every panel on
  // screen is about to fail for the same reason, and a wall of error text says
  // nothing a person can act on.
  if (response.status === 401 && !path.startsWith("/auth/")) {
    const body = await response.clone().json().catch(() => ({}));
    if (body.login) {
      const next = encodeURIComponent(location.pathname + location.search);
      location.href = `${body.login}?next=${next}`;
    }
  }
  if (!response.ok) {
    // The API answers {"error": "...", "correlation_id": "..."}. What goes on
    // screen is the sentence; the id rides along on the error for anyone who
    // needs to find the line in the log. Throwing the raw body put a JSON
    // object in front of the reader — every caller shows .message, and none of
    // them wanted braces in it.
    const text = (await response.text()).trim();
    let message = text;
    let correlationID = "";
    try {
      const body = JSON.parse(text);
      message = body.error || body.message || text;
      correlationID = body.correlation_id || "";
    } catch { /* not JSON: the text is the best there is */ }
    const failure = new Error(message || response.statusText);
    failure.status = response.status;
    failure.correlationID = correlationID;
    throw failure;
  }
  if (response.status === 204) return null;
  return response.json();
}

// Guard's write endpoints take one shared bearer token, kept in sessionStorage
// by the settings page. The dashboard is the only caller, so this is the whole
// of its auth story.
export function adminHeaders() {
  const token = qs('[data-setting="token"]')?.value || sessionStorage.getItem("guard.token") || "";
  if (token) sessionStorage.setItem("guard.token", token);
  return { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) };
}

export function el(tag, className = "", content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (content !== undefined) node.appendChild(text(content));
  return node;
}

export function svg(tag, attributes = {}) {
  const node = document.createElementNS(svgNS, tag);
  for (const [key, value] of Object.entries(attributes)) {
    if (value !== undefined && value !== null) node.setAttribute(key, value);
  }
  return node;
}

// A number a panel has room for. Axis labels and stat values are read at a
// glance, and 1.24M is read faster than 1,240,000 — but only above a thousand,
// because rounding 847 to 847 helps nobody and rounding it to 0.85k hurts.
export function compact(value) {
  if (value === null || value === undefined || Number.isNaN(value)) return "—";
  const magnitude = Math.abs(value);
  if (magnitude >= 1_000_000_000) return `${trim(value / 1_000_000_000)}B`;
  if (magnitude >= 1_000_000) return `${trim(value / 1_000_000)}M`;
  if (magnitude >= 1_000) return `${trim(value / 1_000)}k`;
  if (magnitude >= 100) return String(Math.round(value));
  if (magnitude >= 1) return trim(value);
  if (magnitude === 0) return "0";
  return String(Number(value.toPrecision(2)));
}

function trim(value) {
  return String(Number(value.toFixed(2)));
}

// A measurement, in the unit it was measured in — and durations are read as
// durations. "15,000 ms" is a number somebody has to convert in their head
// before it means anything; 15s is the thing itself. The stat panel already did
// this; every other renderer went through here and did not, so a p95 that
// crossed a second read as four digits and a latency axis was unreadable
// exactly where it mattered.
export function withUnit(value, unit) {
  if (unit === "ms") return duration(value);
  return unit ? `${compact(value)} ${unit}` : compact(value);
}

// Durations are read as durations, not as a count of milliseconds: 1.4s beats
// 1400 ms, and 2m 30s beats 150000.
// Bytes at human scale. 1024, because this counts memory and disk rather than
// anything a marketing department sized.
//
// `cloud.js` and `registries.js` each carry their own older copy of this. They
// are not quite the same function — one says "0 B" where the other says a dash,
// one spells it kB — so they are left where they are rather than unified in a
// change that would quietly alter two working pages. This is the one new code
// should reach for.
export function bytes(value) {
  if (!value) return "—";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = Math.abs(value);
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

// A span of seconds as somebody would say it: "6d 4h", "18m". Used for uptime,
// where the interesting part is the largest unit and the one below it — "532801
// seconds" is a number nobody has ever read as six days.
export function since(seconds) {
  if (!seconds || seconds < 0) return "—";
  const days = Math.floor(seconds / 86_400);
  const hours = Math.floor((seconds % 86_400) / 3_600);
  const minutes = Math.floor((seconds % 3_600) / 60);
  if (days) return `${days}d ${hours}h`;
  if (hours) return `${hours}h ${minutes}m`;
  if (minutes) return `${minutes}m`;
  return `${Math.round(seconds)}s`;
}

export function duration(ms) {
  if (ms === null || ms === undefined || Number.isNaN(ms)) return "—";
  if (ms < 1) return `${Number(ms.toPrecision(2))}ms`;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  const minutes = Math.floor(ms / 60_000);
  return `${minutes}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function timeText(value) {
  return new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 });
}

// Axis labels across a window: the date is only worth the space when the window
// spans more than a day, and seconds only when it spans less than a minute.
export function axisTimeText(value, spanMS) {
  const date = new Date(value);
  if (spanMS > 3 * 86_400_000) return date.toLocaleDateString([], { month: "short", day: "numeric" });
  if (spanMS > 86_400_000) return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  if (spanMS < 120_000) return date.toLocaleTimeString([], { minute: "2-digit", second: "2-digit" });
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export function relativeTime(value) {
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

export function shortID(value) {
  return value ? `${value.slice(0, 12)}${value.length > 12 ? "…" : ""}` : "—";
}

// A stable colour per series key, so the same service is the same colour in
// every panel on the dashboard and across a reload. Assigning by array index
// instead would recolour everything the moment a new service appeared.
export function colourFor(key, index = 0) {
  if (!key) return palette[index % palette.length];
  let hash = 0;
  for (let i = 0; i < key.length; i++) hash = (hash * 31 + key.charCodeAt(i)) | 0;
  return palette[Math.abs(hash) % palette.length];
}

// Nice round numbers for an axis, so gridlines land on 0, 25, 50 rather than
// 0, 23.7, 47.4.
export function niceScale(min, max, ticks = 4) {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return { min: 0, max: 1, step: 0.25 };
  if (min === max) {
    const pad = Math.abs(min || 1) * 0.1;
    min -= pad;
    max += pad;
  }
  // A chart of positive quantities that does not include zero exaggerates every
  // change on it. Counts and durations are always positive, so anchor there.
  if (min > 0 && min < max * 0.5) min = 0;
  const raw = (max - min) / ticks;
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= raw) ?? magnitude * 10;
  return { min: Math.floor(min / step) * step, max: Math.ceil(max / step) * step, step };
}


// ANSI, rendered rather than printed.
//
// Everything guard shows in an output pane came off somebody's terminal, and a
// program that decided it was talking to one wrote colour into it: `docker
// compose`, deno, anything with a tinted logger. Put through `textContent`
// that arrives as a literal `ESC[38;2;87;194;110m[14:35:13.672]`, which is
// worse than no colour at all — it is the escape sequence *and* the loss of
// the thing it was marking.
//
// So the sequences are parsed and the ones that mean something here become
// spans. What is deliberately not implemented is cursor movement: a pane is a
// transcript, not a screen, and a program that moves the cursor to redraw is
// one whose output we keep in full rather than replay.

// The eight, then the bright eight. Values rather than Tailwind classes,
// because a class assembled from a variable is one Tailwind never emits — and
// these have to survive `make css` never having seen them.
const ansiBase = [
  "#1e2430", "#f87171", "#4ade80", "#fbbf24", "#60a5fa", "#c084fc", "#22d3ee", "#d4d8e0",
  "#4b5563", "#fca5a5", "#86efac", "#fde047", "#93c5fd", "#d8b4fe", "#67e8f9", "#f8fafc",
];

// xterm's 256: the sixteen above, a 6x6x6 cube, then twenty-four greys.
function ansi256(n) {
  if (n < 16) return ansiBase[n];
  if (n < 232) {
    const step = (v) => [0, 95, 135, 175, 215, 255][v].toString(16).padStart(2, "0");
    const i = n - 16;
    return `#${step(Math.floor(i / 36) % 6)}${step(Math.floor(i / 6) % 6)}${step(i % 6)}`;
  }
  const grey = (8 + (n - 232) * 10).toString(16).padStart(2, "0");
  return `#${grey}${grey}${grey}`;
}

// One SGR run, applied to the state the pane carries from chunk to chunk.
function applySGR(codes, state) {
  for (let i = 0; i < codes.length; i++) {
    const code = codes[i];
    if (code === 0) {
      state.fg = null; state.bg = null; state.bold = false;
      state.dim = false; state.italic = false; state.underline = false; state.inverse = false;
    } else if (code === 1) state.bold = true;
    else if (code === 2) state.dim = true;
    else if (code === 3) state.italic = true;
    else if (code === 4) state.underline = true;
    else if (code === 7) state.inverse = true;
    else if (code === 22) { state.bold = false; state.dim = false; }
    else if (code === 23) state.italic = false;
    else if (code === 24) state.underline = false;
    else if (code === 27) state.inverse = false;
    else if (code === 39) state.fg = null;
    else if (code === 49) state.bg = null;
    else if (code >= 30 && code <= 37) state.fg = ansiBase[code - 30];
    else if (code >= 90 && code <= 97) state.fg = ansiBase[code - 90 + 8];
    else if (code >= 40 && code <= 47) state.bg = ansiBase[code - 40];
    else if (code >= 100 && code <= 107) state.bg = ansiBase[code - 100 + 8];
    else if (code === 38 || code === 48) {
      const target = code === 38 ? "fg" : "bg";
      if (codes[i + 1] === 5) { state[target] = ansi256(codes[i + 2] || 0); i += 2; }
      else if (codes[i + 1] === 2) {
        state[target] = `rgb(${codes[i + 2] || 0} ${codes[i + 3] || 0} ${codes[i + 4] || 0})`;
        i += 4;
      }
    }
  }
}

function styleOf(state) {
  const fg = state.inverse ? state.bg : state.fg;
  const bg = state.inverse ? state.fg : state.bg;
  const parts = [];
  if (fg) parts.push(`color:${fg}`);
  if (bg) parts.push(`background-color:${bg}`);
  if (state.bold) parts.push("font-weight:600");
  if (state.dim) parts.push("opacity:.65");
  if (state.italic) parts.push("font-style:italic");
  if (state.underline) parts.push("text-decoration:underline");
  return parts.join(";");
}

// A carriage return is a program drawing over its own line — a progress bar, a
// spinner, `docker pull` counting layers. The pane keeps the last thing it
// drew, which is the line that was true when it stopped, rather than every
// frame of it stacked up.
function lastFrame(value) {
  if (!value.includes("\r")) return value;
  return value.split("\n").map((line) => {
    const frames = line.split("\r");
    for (let i = frames.length - 1; i >= 0; i--) if (frames[i] !== "") return frames[i];
    return "";
  }).join("\n");
}

// What a transcript has to survive. Only SGR (`m`) is rendered; the cursor
// moves, the erases, the OSC title strings and the stray control bytes are
// dropped, because a pane cannot honour them and printing them is the whole
// problem this is here to solve. Tabs and newlines are left alone.
const ansiPattern = new RegExp([
  "\\u001b\\[([0-9;:]*)m",                       // SGR — the only one drawn
  "\\u001b\\[[0-9;?]*[A-HJKSTfhlnsu]",           // cursor moves, erases, modes
  "\\u001b\\][\\s\\S]*?(?:\\u0007|\\u001b\\\\)", // OSC, to BEL or ST
  "\\u001b[()][AB0-2]",                          // charset selection
  "\\u001b[=>78]",                               // keypad modes, save/restore
  "[\\u0000-\\u0008\\u000b\\u000c\\u000e-\\u001f\\u007f]", // strays, not tab or newline
].join("|"), "g");

// ansiFragment turns terminal output into nodes for a pane, as a
// DocumentFragment so the caller can replaceChildren it in one go.
export function ansiFragment(output) {
  const fragment = document.createDocumentFragment();
  const source = lastFrame(String(output ?? ""));
  const state = { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, inverse: false };
  let at = 0;
  const push = (piece) => {
    if (!piece) return;
    const style = styleOf(state);
    if (!style) { fragment.append(document.createTextNode(piece)); return; }
    const span = document.createElement("span");
    span.setAttribute("style", style);
    span.textContent = piece;
    fragment.append(span);
  };
  for (const match of source.matchAll(ansiPattern)) {
    push(source.slice(at, match.index));
    at = match.index + match[0].length;
    // match[1] is set only for SGR. A bare ESC[m is a reset.
    if (match[1] !== undefined) {
      applySGR(match[1] === "" ? [0] : match[1].split(/[;:]/).map((part) => Number(part) || 0), state);
    }
  }
  push(source.slice(at));
  return fragment;
}

// setOutput is the one call a pane makes. In one place rather than inlined at
// each of them, because the interesting part is that it is never innerHTML:
// everything above builds text nodes and spans, so output claiming to be
// markup stays output.
export function setOutput(pane, output) {
  pane.replaceChildren(ansiFragment(output));
}
