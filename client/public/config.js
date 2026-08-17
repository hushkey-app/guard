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
import { adminHeaders, qs, qsa, request } from "./core.js";
import { ensure, set } from "./store.js";
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
  return row;
}

function patchRow(row, value) {
  const node = field(row);
  if (!touched(node)) {
    node.value = value.value || "";
    node.dataset.dirty = "false";
  }
  // Read-only rather than absent: "where is this configured" is a question the
  // page should answer even for the two values it cannot change.
  node.readOnly = Boolean(value.bootstrap);
  if (value.hidden) {
    node.value = value.is_set ? "•".repeat(32) : "";
    node.placeholder = "never shown";
  }
  // A row guard can mint gets the two buttons that used to be a card of their
  // own: Generate, and a Copy for pasting the result into a collector on another
  // box.
  const generate = qs("[data-config-generate]", row);
  if (generate) generate.hidden = !value.generatable || value.bootstrap;
  const copy = qs("[data-config-copy]", row);
  if (copy) copy.hidden = !value.generatable || !value.is_set;
  const source = qs("[data-config-source]", row);
  source.textContent = value.bootstrap
    ? `read-only · ${value.source}`
    : value.source === "stored" ? "stored here" : value.source === "environment" ? "from the environment" : "default";
  qs("[data-config-pending]", row).hidden = !value.pending;
}

function renderConfig(state) {
  const host = qs("[data-config-groups]");
  if (!host) return;
  known = new Map();
  for (const group of state.groups || []) for (const value of group.values) known.set(value.name, value);

  const ignored = qs("[data-config-ignored]");
  if (ignored) ignored.hidden = !state.ignored;
  const restart = qs("[data-config-restart]");
  if (restart) restart.hidden = !(state.pending && state.restartable);

  // First answer builds the form; every later one patches it, so a background
  // refresh cannot take away a half-typed client secret.
  if (!host.dataset.built) {
    host.replaceChildren();
    const template = qs("[data-config-group-template]");
    for (const group of state.groups || []) {
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
  if (state.pending && state.restartable) status("Saved. Guard is still running the old values until it restarts.");
  else if (state.pending) status("Saved. Restart Guard by hand to use it.");
  else status("Everything here is what Guard is running.");
}

export async function refreshConfig() {
  if (!qs("[data-config-groups]")) return;
  try {
    // Through the store like every other page, so walking back to it draws
    // instantly instead of saying "Loading…" again.
    await ensure("config", () => request("/api/config", { headers: adminHeaders() }), renderConfig);
  } catch (error) {
    qs("[data-config-loading]")?.remove();
    status(error.status === 401 || error.status === 403
      ? "Only an admin may read this. Enter the admin token on the Data storage page."
      : error.message);
  }
}

// changed is every row whose input differs from the value the server last sent.
// An emptied field is a change too — it is how a value is taken back out of the
// database, and the server reads "" as a removal rather than as an empty value.
function changed() {
  const values = {};
  for (const row of qsa("[data-config-row]")) {
    const value = known.get(row.dataset.configFor);
    if (!value || value.bootstrap || value.hidden) continue;
    const typed = field(row).value;
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
  if (copy) copyValue(copy.closest("[data-config-row]"));
});

// Nothing to unmount: the marker that says "the form is built" lives on the
// element itself, and howl throws that away on navigation — so the next mount
// starts from an empty host and builds again.
