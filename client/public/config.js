// The configuration page: guard's whole environment, drawn from the catalogue
// the server answers with.
//
// Nothing here knows the name of a single variable. The page is built from
// GET /api/config, so adding a setting to internal/config's list adds a field
// here and nothing else — a page with forty hand-written inputs is a page that
// is one commit away from being wrong about what guard reads.
//
// Two rules carry the interaction:
//
//   - **A field somebody is editing is never overwritten.** The rows are built
//     once and then patched, and a row the person has touched keeps what they
//     typed. This page is a form somebody fills in slowly, with a browser
//     window open beside it on somebody's OAuth console.
//   - **Only what changed is sent.** Two people with this page open should not
//     overwrite each other's untouched fields, and the server takes a partial
//     map for exactly that reason.
import { adminHeaders, el, qs, qsa, request, text } from "./core.js";
import { ensure, forget, set } from "./store.js";
import { ask } from "./cluster.js";

// What the last answer said, so a save can be diffed against it and the restart
// button knows which token to carry over.
let known = new Map();

function status(message) {
  const node = qs("[data-config-status]");
  if (node) node.textContent = message;
}

// A row keeps whichever of the two inputs its kind wanted and drops the other,
// so this is the one input in it.
function field(row) {
  return qs("[data-config-input]", row) || qs("[data-config-textarea]", row);
}

// touched is the whole of the dirty tracking: an input the person has typed in,
// or has focus in right now. Either way a background refresh leaves it alone.
function touched(node) {
  return node.dataset.dirty === "true" || node === document.activeElement;
}

function buildRow(value) {
  const template = qs("[data-config-row-template]");
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.configFor = value.name;
  const id = `config-${value.name}`;
  const label = qs("[data-config-label]", row);
  label.textContent = value.label;
  label.htmlFor = id;
  qs("[data-config-name]", row).textContent = value.name;
  const help = qs("[data-config-help]", row);
  if (value.help) help.textContent = value.help;
  else help.remove();
  const input = qs("[data-config-input]", row);
  const textarea = qs("[data-config-textarea]", row);
  // One row, two inputs, one of them removed — a PEM key in a single-line input
  // is a field you cannot read what you pasted into.
  if (value.kind === "multiline") {
    input.remove();
    textarea.hidden = false;
    textarea.id = id;
  } else {
    textarea.remove();
    input.id = id;
    input.placeholder = value.default || "not set";
    if (value.kind === "number") input.inputMode = "numeric";
  }
  // A secret is typed and not read back, so a single-line one is a password box.
  // A multi-line one is not: `type` is read-only on a textarea, and a .p8 nobody
  // can see while pasting it is a .p8 pasted wrong.
  if (value.secret && value.kind !== "multiline") field(row).type = "password";
  return row;
}

function patchRow(row, value) {
  const node = field(row);
  // A secret never comes back, so there is nothing to patch into the box: it
  // stays empty, and the placeholder is what says whether one is stored. Writing
  // dots in would be a value somebody tries to copy.
  if (value.secret) {
    node.placeholder = value.is_set ? "set — paste a new one to replace it" : "not set";
    if (!touched(node)) node.value = "";
  } else if (!touched(node)) {
    node.value = value.value || "";
    node.dataset.dirty = "false";
  }
  // A row guard can mint gets the two buttons that used to be a card of their
  // own: Generate, and a Copy for pasting the result into a collector on another
  // box.
  const generate = qs("[data-config-generate]", row);
  if (generate) generate.hidden = !value.generatable;
  const copy = qs("[data-config-copy]", row);
  if (copy) copy.hidden = !value.generatable || !value.is_set;
  // Clearing is the one thing an empty box cannot mean for a secret, so it gets a
  // button: without it there would be no way to take Google's credentials back
  // out, and the pair rule refuses removing the id on its own.
  const clear = qs("[data-config-clear]", row);
  if (clear) clear.hidden = !value.secret || !value.is_set;
  const source = qs("[data-config-source]", row);
  source.textContent = value.secret
      ? value.is_set ? `set · ${value.source === "stored" ? "stored here" : "from the environment"}` : "not set"
      : value.source === "stored" ? "stored here" : value.source === "environment" ? "from the environment" : "default";
  qs("[data-config-pending]", row).hidden = !value.pending;
}

function renderConfig(state) {
  const host = qs("[data-config-groups]");
  if (!host) return;
  // One catalogue, two pages: /settings/config for the settings somebody tunes and
  // /settings/security for the ones that decide who may open the dashboard. The
  // group carries which page it belongs to, so this filters rather than fetching
  // something different — and `known` holds only this page's rows, which is what
  // makes "only what changed is sent" true per page.
  const page = host.dataset.configPage || "config";
  const groups = (state.groups || []).filter((group) => (group.page || "config") === page);
  known = new Map();
  for (const group of groups) for (const value of group.values) known.set(value.name, value);

  const ignored = qs("[data-config-ignored]");
  if (ignored) ignored.hidden = !state.ignored;
  // Pending is the whole process's, not this page's: a value saved on the other
  // page is still a restart this one can press, and hiding the button here would
  // be hiding the only thing that makes either save real.
  const restart = qs("[data-config-restart]");
  if (restart) restart.hidden = !(state.pending && state.restartable);

  // First answer builds the form; every later one patches it, so a background
  // refresh cannot take away a half-typed client secret.
  if (!host.dataset.built) {
    host.replaceChildren();
    const template = qs("[data-config-group-template]");
    for (const group of groups) {
      const card = template.content.firstElementChild.cloneNode(true);
      qs("[data-config-group-name]", card).textContent = group.name;
      const rows = qs("[data-config-group-rows]", card);
      for (const value of group.values) rows.append(buildRow(value));
      host.append(card);
    }
    host.dataset.built = "true";
  }
  for (const row of qsa("[data-config-row]", host)) {
    const value = known.get(row.dataset.configFor);
    if (value) patchRow(row, value);
  }
  const waiting = groups.some((group) => group.values.some((value) => value.pending));
  if (waiting && state.restartable) status("Saved. Guard is still running the old values until it restarts.");
  else if (waiting) status("Saved. Restart Guard by hand to use it.");
  else if (state.pending) status("Everything here is in force. Something saved on another page is waiting for a restart.");
  else status("Everything here is what Guard is running.");
}

export async function refreshConfig() {
  const host = qs("[data-config-groups]");
  if (!host) return;
  try {
    // Through the store like every other page, so walking back to it draws
    // instantly instead of saying "Loading…" again.
    await ensure("config", () => request("/api/config", { headers: adminHeaders() }), renderConfig);
  } catch (error) {
    // In the middle of the page, not as a line under a sticky bar. Every row here
    // is admin, so on an instance with GUARD_TOKEN set this is what somebody sees
    // on their first visit in a new tab — and an empty form with a sentence
    // somewhere below it reads as "broken", which is how you end up looking for
    // the bug in the build rather than in the field you have not filled in.
    const locked = error.status === 401 || error.status === 403;
    host.replaceChildren(errorPanel(locked, error.message));
    status(locked ? "Waiting for the admin token." : error.message);
  }
}

function errorPanel(locked, message) {
  const panel = el("div", "space-y-3 rounded-xl border border-border bg-card p-6");
  panel.append(el("p", "text-sm font-medium", locked ? "This page needs the admin token" : "Could not read the configuration"));
  if (!locked) {
    panel.append(el("p", "text-sm text-muted-foreground", message));
    return panel;
  }
  const line = el("p", "max-w-2xl text-sm text-muted-foreground");
  line.append(
    text("Every value here is admin-only, and this instance has "),
    el("code", "font-mono", "GUARD_TOKEN"),
    text(" set — on an instance with no token and nobody signing in, none of this asks. Paste it once and this tab remembers it: "),
    text("it is kept in this browser tab only and never written to SQLite. It is the same field as the one on "),
    link("/settings", "Data storage"),
    text("."),
  );
  panel.append(line);
  // The box goes here rather than only on another page, because being sent
  // somewhere else to type a value and then having to come back is the whole of
  // why this looked broken.
  panel.append(tokenBox());
  // And the trap this feature can set for itself: a token generated on this page
  // is sealed in guard's database, so the only thing that can read it back is the
  // page you cannot open. Naming the way out here is the difference between a
  // minute and an afternoon.
  const lost = el("p", "max-w-2xl text-xs text-muted-foreground");
  lost.append(
    text("Lost it? A token generated here is sealed in Guard's database and cannot be read back any other way. Start Guard with "),
    el("code", "font-mono", "GUARD_CONFIG_IGNORE=1"),
    text(" — it then runs on the environment alone, nothing asks for a token, and you can read or generate a new one from this page."),
  );
  panel.append(lost);
  return panel;
}

// Paste the token and get on with it. It lands in the same sessionStorage key
// adminHeaders reads, so every page in this tab is unlocked by one paste — and it
// dies with the tab, which is what makes it safe to type into a laptop's browser.
function tokenBox() {
  const form = el("form", "flex flex-wrap items-stretch gap-2");
  const input = el("input", "cn-input h-9 min-w-0 flex-1 font-mono text-xs");
  input.type = "password";
  input.placeholder = "GUARD_TOKEN";
  input.autocomplete = "off";
  input.spellcheck = false;
  input.dataset.configToken = "true";
  const submit = el("button", "cn-button cn-button-variant-default cn-button-size-sm h-9 shrink-0", "Unlock");
  submit.type = "submit";
  form.append(input, submit);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    const token = input.value.trim();
    if (!token) return;
    sessionStorage.setItem("guard.token", token);
    // Forget the failed read, or the store would hand the next page the error it
    // is holding rather than asking again with the token.
    forget("config");
    status("Reading…");
    refreshConfig();
  });
  return form;
}

function link(href, label) {
  const anchor = el("a", "underline underline-offset-2 hover:text-foreground", label);
  anchor.href = href;
  anchor.dataset.navLink = "true";
  return anchor;
}

// changed is every row whose input differs from the value the server last sent.
// An emptied field is a change too — it is how a value is taken back out of the
// database, and the server reads "" as a removal rather than as an empty value.
function changed() {
  const values = {};
  for (const row of qsa("[data-config-row]")) {
    const value = known.get(row.dataset.configFor);
    if (!value) continue;
    const typed = field(row).value;
    // An empty box is "leave it alone" for a secret and "remove it" for
    // everything else, which is the whole difference between a value you can read
    // back and one you cannot: a page that treated empty as a removal here would
    // delete somebody's client secret the first time they saved a neighbouring
    // field.
    if (value.secret) {
      if (typed) values[value.name] = typed;
      continue;
    }
    if (typed !== (value.value || "")) values[value.name] = typed;
  }
  return values;
}

// Mint one of the two tokens. The value is generated and stored by the server —
// 32 random bytes as hex, the same thing `openssl rand -hex 32` gives — so the
// browser never invents a credential and the row lands pending a restart like any
// other saved value.
async function generateValue(row) {
  const name = row.dataset.configFor;
  const held = known.get(name);
  if (held?.is_set && !await ask({
    title: `Replace ${name}?`,
    body: "Everything presenting the current value stops working when Guard restarts. Rotating the collector secret and the operator token are separate presses on purpose.",
    confirm: "Generate a new one",
  })) return;
  status(`Generating ${name}…`);
  try {
    const state = await request("/api/config/generate", {
      method: "POST",
      headers: adminHeaders(),
      body: JSON.stringify({ name }),
    });
    renderConfig(state);
    set("config", state);
    // Straight to the clipboard: the reason to generate one of these is to paste
    // it somewhere, and the value is on screen either way.
    const value = known.get(name)?.value;
    if (value) navigator.clipboard?.writeText(value).catch(() => {});
  } catch (error) {
    status(error.message);
  }
}

// What has to go with a value when it goes.
//
// A provider's credentials are all-or-nothing — guard treats half a configuration
// as fatal at startup and refuses to store one — so "remove the client secret" can
// only mean "turn this provider off". A Remove that sent one name and came back
// with the pair error would be a button whose only outcome is an error message.
//
// Apple's private key is the exception in the exception: it is legal to remove it
// when the key *file* is set, because that is where the key comes from then.
function alsoClear(name) {
  if (name === "GUARD_GOOGLE_CLIENT_SECRET") return ["GUARD_GOOGLE_CLIENT_ID"];
  if (name === "GUARD_APPLE_PRIVATE_KEY") {
    if (known.get("GUARD_APPLE_PRIVATE_KEY_FILE")?.is_set) return [];
    return ["GUARD_APPLE_CLIENT_ID", "GUARD_APPLE_TEAM_ID", "GUARD_APPLE_KEY_ID", "GUARD_APPLE_PRIVATE_KEY_FILE"];
  }
  return [];
}

// Take a stored secret back out. Its own press, because an empty box means
// "unchanged" for these rows and there has to be some way to say "gone".
async function clearValue(row) {
  const name = row.dataset.configFor;
  const others = alsoClear(name).filter((other) => known.get(other)?.is_set);
  if (!await ask({
    title: others.length ? `Turn this provider off?` : `Remove ${name}?`,
    body: "Guard forgets the value. Whatever it configures stops working at the next restart, and the only way back is pasting it again from the provider.",
    detail: [name, ...others].join(", "),
    confirm: others.length ? "Turn it off" : "Remove it",
  })) return;
  status(`Removing ${name}…`);
  try {
    const values = { [name]: "" };
    for (const other of others) values[other] = "";
    const state = await request("/api/config", {
      method: "PUT",
      headers: adminHeaders(),
      body: JSON.stringify({ values }),
    });
    renderConfig(state);
    set("config", state);
  } catch (error) {
    status(error.message);
  }
}

async function copyValue(row) {
  const value = field(row).value;
  if (!value) return;
  try {
    await navigator.clipboard.writeText(value);
    status(`${row.dataset.configFor} copied.`);
  } catch {
    status("Could not copy — select the field instead.");
  }
}

async function saveConfig() {
  const values = changed();
  const names = Object.keys(values);
  if (!names.length) { status("Nothing has changed."); return; }
  status(`Saving ${names.length === 1 ? names[0] : `${names.length} settings`}…`);
  try {
    const state = await request("/api/config", { method: "PUT", headers: adminHeaders(), body: JSON.stringify({ values }) });
    // The answer is the new truth, so the dirty marks come off and the rows are
    // patched from it — including the ones a validation error would have left
    // unwritten, because a refused save writes nothing at all.
    for (const row of qsa("[data-config-row]")) field(row).dataset.dirty = "false";
    // The answer is the new truth: patch the rows from it, and put it in the
    // store so navigating away and back does not re-fetch to learn the same
    // thing. A refused save writes nothing at all, so there is no half state to
    // reconcile — the error message is the whole outcome.
    renderConfig(state);
    set("config", state);
  } catch (error) {
    status(error.message);
  }
}

// The restart is guard exiting and its supervisor starting it again, so this
// page's own connection is what goes away. It polls until something answers,
// then reloads — carrying the operator token over first, because the tab may
// have been authenticating with the one that has just been replaced.
async function restartGuard() {
  const ok = await ask({
    title: "Restart Guard?",
    body: "The dashboard reconnects in a few seconds. Telemetry in flight is lost, and the saved configuration becomes the running one.",
    confirm: "Restart",
  });
  if (!ok) return;
  const wanted = known.get("GUARD_TOKEN")?.value || "";
  const usingToken = Boolean(sessionStorage.getItem("guard.token"));
  status("Restarting…");
  try { await request("/api/config/restart", { method: "POST", headers: adminHeaders() }); }
  catch (error) { status(error.message); return; }
  // Long enough for the old process to be gone: it answers first and exits
  // behind the response, so polling immediately would find the one that is
  // about to stop and call it a success.
  await new Promise((done) => setTimeout(done, 2000));
  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      const response = await fetch("/healthz", { cache: "no-store" });
      if (response.ok) {
        if (usingToken && wanted) sessionStorage.setItem("guard.token", wanted);
        location.reload();
        return;
      }
    } catch { /* still down — the expected answer for a second or two */ }
    await new Promise((done) => setTimeout(done, 750));
  }
  status("Guard has not come back yet. Check the service on the box.");
}

document.addEventListener("input", (event) => {
  const node = event.target.closest("[data-config-input], [data-config-textarea]");
  if (node) node.dataset.dirty = "true";
});

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-config-save]")) { saveConfig(); return; }
  if (event.target.closest("[data-config-restart]")) { restartGuard(); return; }
  const generate = event.target.closest("[data-config-generate]");
  if (generate) { generateValue(generate.closest("[data-config-row]")); return; }
  const copy = event.target.closest("[data-config-copy]");
  if (copy) { copyValue(copy.closest("[data-config-row]")); return; }
  const clear = event.target.closest("[data-config-clear]");
  if (clear) clearValue(clear.closest("[data-config-row]"));
});

// Nothing to unmount: the marker that says "the form is built" lives on the
// element itself, and howl throws that away on navigation — so the next mount
// starts from an empty host and builds again.
