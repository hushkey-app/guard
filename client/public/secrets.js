// The secrets page: what your applications boot with.
//
// One module, three panels, because configuring this is one job — a group, the
// pairs in it, and a key that reads them. Everything here writes; nothing here
// is what an application talks to. That is `guard-vault`, a second process on
// the same database, and the reason this page can be down while production
// keeps booting.
//
// Two rules shape the interaction:
//
//   - **Every row saves itself.** No page-wide save, the same as the alerts
//     page: these are independent values, and one button that writes all of
//     them is how a half-typed password reaches production beside the change
//     somebody meant to make.
//   - **Values arrive but stay masked.** Reading them back is the point of the
//     page — a store you can only write to means the real copy lives in a file
//     on a laptop — but a screen full of live credentials is a screenshot and a
//     screen share waiting to happen. So a value is shown when it is asked for,
//     one press at a time, or all at once by somebody who meant to.

import { adminHeaders, el, qs, qsa, relativeTime, request } from "./core.js";
import { ensure, forget } from "./store.js";
import { ask } from "./cluster.js";

let envs = [];
let secrets = [];
let keys = [];
// Which environment is open. Kept in the module rather than the URL: it
// survives navigating away and back, and an environment name in the address bar
// is one more place "production" gets copied into a chat.
let current = 0;
// Which values are unmasked, by secret id. Cleared on every environment change,
// because "show" was asked about one screen and not about the next.
const shown = new Set();

export async function refreshSecrets() {
  if (!qs("[data-secret-envs]")) return;
  try {
    await ensure("secrets.envs", () => request("/api/secrets", { headers: adminHeaders() }), (answer) => {
      envs = answer || [];
      if (!envs.some((env) => env.id === current)) current = envs[0]?.id || 0;
      renderEnvs();
    });
    await Promise.all([loadValues(), loadKeys()]);
  } catch (failure) {
    status(failure.message);
  }
}

function status(message) {
  const note = qs("[data-secret-status]");
  if (!note) return;
  note.textContent = message || "";
  note.hidden = !message;
}

// ---------------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------------

function renderEnvs() {
  const host = qs("[data-secret-envs]");
  if (!host) return;
  host.replaceChildren(...envs.map((env) => envRow(env)));
  const name = qs("[data-secret-env-name]");
  const note = qs("[data-secret-env-note]");
  const open = envs.find((env) => env.id === current);
  if (name) name.textContent = open ? open.name : "No environment";
  if (note) {
    note.textContent = open
      ? `${open.secrets} secret${open.secrets === 1 ? "" : "s"} · ${open.keys} key${open.keys === 1 ? "" : "s"}`
      : "Add one to start.";
  }
}

function envRow(env) {
  const row = el("div", `flex cursor-pointer items-center gap-2 px-4 py-2.5 hover:bg-muted/30${
    env.id === current ? " bg-muted/50" : ""}`);
  row.dataset.envId = env.id;
  const names = el("div", "min-w-0 flex-1");
  names.append(
    el("p", "truncate text-sm font-medium", env.name),
    el("p", "text-[.65rem] text-muted-foreground",
      `${env.secrets} secret${env.secrets === 1 ? "" : "s"}${env.keys ? ` · ${env.keys} key${env.keys === 1 ? "" : "s"}` : ""}`),
  );
  const remove = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-icon-sm shrink-0 text-muted-foreground hover:text-destructive", "×");
  remove.type = "button";
  remove.dataset.envRemove = env.id;
  remove.dataset.envName = env.name;
  remove.setAttribute("aria-label", `Delete ${env.name}`);
  row.append(names, remove);
  return row;
}

async function addEnv() {
  const name = prompt("New environment name", "");
  if (name === null || !name.trim()) return;
  try {
    const saved = await request("/api/secrets", {
      method: "POST", headers: adminHeaders(), body: JSON.stringify({ name: name.trim() }),
    });
    current = saved.id;
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

async function removeEnv(id, name) {
  const env = envs.find((entry) => entry.id === id);
  // Typing the name, like locking a machine — this takes the secrets and the
  // keys with it, and a key pointing at a deleted environment is a token
  // nobody thinks to revoke.
  const agreed = await ask({
    title: `Delete ${name}?`,
    body: `Its ${env?.secrets || 0} secret(s) and ${env?.keys || 0} key(s) go with it. Anything still holding one of those keys stops booting.`,
    detail: "Type the environment's name to confirm.",
    confirm: "Delete environment",
    phrase: name,
  });
  if (!agreed) return;
  try {
    await request(`/api/secrets/${id}`, { method: "DELETE", headers: adminHeaders() });
    if (current === id) current = 0;
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

// reload drops what the store is holding and asks again — after a write, where
// the counts, the rows and the keys have all moved at once.
async function reload() {
  forget("secrets.envs");
  forget("secrets.values." + current);
  forget("secrets.keys");
  await refreshSecrets();
}

// ---------------------------------------------------------------------------
// The pairs
// ---------------------------------------------------------------------------

async function loadValues() {
  if (!current) {
    secrets = [];
    renderSecrets();
    return;
  }
  await ensure(`secrets.values.${current}`,
    () => request(`/api/secrets/values?env=${current}`, { headers: adminHeaders() }),
    (answer) => {
      secrets = answer || [];
      renderSecrets();
    });
}

function renderSecrets() {
  const host = qs("[data-secret-rows]");
  const template = qs("[data-secret-row-template]");
  if (!host || !template) return;
  host.replaceChildren(...secrets.map((secret) => secretRow(template, secret)));
  const empty = qs("[data-secret-empty]");
  if (empty) empty.hidden = secrets.length > 0 || !current;
}

function secretRow(template, secret) {
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.secretId = secret.id || 0;
  qs("[data-secret-key]", row).value = secret.key || "";
  const value = qs("[data-secret-value]", row);
  value.value = secret.value || "";
  value.type = shown.has(secret.id) ? "text" : "password";
  qs("[data-secret-unreadable]", row).hidden = !secret.unreadable;
  if (secret.unreadable) {
    // Nothing to show and nothing to copy: what is in the box is not the
    // value, and pretending otherwise would have somebody paste an empty
    // string into production.
    value.value = "";
    value.placeholder = "sealed with a different key — set it again";
  }
  const updated = qs("[data-secret-updated]", row);
  if (updated && secret.updated_at && !secret.updated_at.startsWith("0001")) {
    updated.textContent = relativeTime(secret.updated_at);
  }
  return row;
}

// A new row is a row, not a dialog: the page is a table of pairs, and adding
// one should look like the thing next to it.
function addSecret() {
  if (!current) return;
  const host = qs("[data-secret-rows]");
  const template = qs("[data-secret-row-template]");
  if (!host || !template) return;
  const row = secretRow(template, { id: 0, key: "", value: "" });
  qs("[data-secret-value]", row).type = "text";
  host.append(row);
  qs("[data-secret-empty]").hidden = true;
  qs("[data-secret-key]", row).focus();
}

async function saveSecret(row) {
  const key = qs("[data-secret-key]", row).value.trim();
  const value = qs("[data-secret-value]", row).value;
  if (!key) {
    status("a secret needs a key");
    return;
  }
  try {
    await request("/api/secrets/values", {
      method: "PUT", headers: adminHeaders(),
      body: JSON.stringify({ env_id: current, key, value }),
    });
    status("");
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

async function removeSecret(row) {
  const id = Number(row.dataset.secretId);
  const key = qs("[data-secret-key]", row).value.trim();
  // An unsaved row is just markup — nothing to confirm and nothing to call.
  if (!id) {
    row.remove();
    return;
  }
  const agreed = await ask({
    title: `Delete ${key}?`,
    body: "Anything reading this environment gets one fewer variable on its next fetch.",
    confirm: "Delete secret",
  });
  if (!agreed) return;
  try {
    await request(`/api/secrets/values/${id}`, { method: "DELETE", headers: adminHeaders() });
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

// ---------------------------------------------------------------------------
// Import and export
// ---------------------------------------------------------------------------

function importPanel() { return qs("[data-secret-import]"); }

// Opened as a modal: pasting a file is one transaction, and the rows behind it
// are not part of it. Everything is reset on the way in, because a report left
// over from the last paste is a report about a file that is no longer there.
function openImport(panel) {
  qs("[data-secret-import-text]", panel).value = "";
  qs("[data-secret-import-prune]", panel).checked = false;
  qs("[data-secret-import-status]", panel).textContent = "";
  const report = qs("[data-secret-import-report]", panel);
  report.replaceChildren();
  report.hidden = true;
  panel.showModal();
  qs("[data-secret-import-text]", panel).focus();
}

async function runImport(dryRun) {
  const panel = importPanel();
  const text = qs("[data-secret-import-text]", panel).value;
  const prune = qs("[data-secret-import-prune]", panel).checked;
  const note = qs("[data-secret-import-status]", panel);
  if (!text.trim()) {
    note.textContent = "Paste something first.";
    return;
  }
  try {
    const result = await request("/api/secrets/import", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ env_id: current, text, prune, dry_run: dryRun }),
    });
    note.textContent = "";
    reportImport(result);
    if (dryRun) return;
    // The modal stays open on what it did. Closing on success would replace
    // the one screen that says which keys changed and which lines were
    // skipped with a table that does not, and those names are the reason the
    // report is worth printing at all.
    note.textContent = "Imported.";
    qs("[data-secret-import-text]", panel).value = "";
    await reload();
  } catch (failure) {
    note.textContent = failure.message;
  }
}

// What it did, or would do, named rather than counted. "3 lines skipped" sends
// somebody back to a hundred-line file to work out which three.
function reportImport(result) {
  const host = qs("[data-secret-import-report]");
  if (!host) return;
  const lines = [];
  const summary = el("p", "font-medium",
    `${result.dry_run ? "Would add" : "Added"} ${result.added.length}, ` +
    `${result.dry_run ? "change" : "changed"} ${result.changed.length}, ` +
    `${result.unchanged.length} already the same` +
    (result.pruned.length ? `, ${result.dry_run ? "delete" : "deleted"} ${result.pruned.length}` : ""));
  lines.push(summary);
  for (const [label, names, tone] of [
    ["New", result.added, "text-muted-foreground"],
    ["Changed", result.changed, "text-muted-foreground"],
    ["Deleted", result.pruned, "text-destructive"],
  ]) {
    if (!names.length) continue;
    lines.push(el("p", `font-mono ${tone}`, `${label}: ${names.join(", ")}`));
  }
  for (const skip of result.skipped || []) {
    lines.push(el("p", "text-destructive", `line ${skip.line}: ${skip.reason} — ${skip.text}`));
  }
  host.replaceChildren(...lines);
  host.hidden = false;
}

async function exportEnv() {
  try {
    const answer = await request(`/api/secrets/export?env=${current}`, { headers: adminHeaders() });
    await navigator.clipboard?.writeText(answer.text);
    status(`${answer.env} copied as .env text — it is in your clipboard in plain text.`);
  } catch (failure) {
    status(failure.message);
  }
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

async function loadKeys() {
  await ensure("secrets.keys", () => request("/api/secrets/keys", { headers: adminHeaders() }), (answer) => {
    keys = answer || [];
    renderKeys();
  });
}

function renderKeys() {
  const host = qs("[data-secret-keys]");
  const template = qs("[data-secret-key-row-template]");
  if (!host || !template) return;
  host.replaceChildren(...keys.map((key) => keyRow(template, key)));
  const empty = qs("[data-secret-keys-empty]");
  if (empty) empty.hidden = keys.length > 0;
}

function keyRow(template, key) {
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.keyId = key.id;
  qs("[data-key-name]", row).textContent = key.name;
  qs("[data-key-env]", row).textContent = key.env_name || "";
  qs("[data-key-prefix]", row).textContent = `${key.prefix}…`;
  const revoked = !!key.revoked_at && !key.revoked_at.startsWith("0001");
  qs("[data-key-revoked]", row).hidden = !revoked;
  qs("[data-key-revoke]", row).hidden = revoked;
  if (revoked) row.classList.add("opacity-60");
  // Never used is a sentence, not a blank: a key nobody has spent since March
  // is one that can go, and an empty column cannot say that.
  const used = key.last_used_at && !key.last_used_at.startsWith("0001");
  qs("[data-key-used]", row).textContent = used ? relativeTime(key.last_used_at) : "never";
  return row;
}

async function addKey() {
  if (!current) return;
  const env = envs.find((entry) => entry.id === current);
  const name = prompt(`What holds this key? (reads ${env?.name || "this environment"})`, "");
  if (name === null || !name.trim()) return;
  try {
    const minted = await request("/api/secrets/keys", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ env_id: current, name: name.trim() }),
    });
    showToken(minted.token);
    forget("secrets.keys");
    forget("secrets.envs");
    await refreshSecrets();
  } catch (failure) {
    status(failure.message);
  }
}

function showToken(token) {
  const panel = qs("[data-secret-token]");
  if (!panel) return;
  qs("[data-secret-token-value]", panel).textContent = token;
  panel.hidden = false;
  panel.scrollIntoView({ behavior: "smooth", block: "center" });
}

async function revokeKey(id) {
  const key = keys.find((entry) => entry.id === id);
  const agreed = await ask({
    title: `Revoke ${key?.name || "this key"}?`,
    body: "Whatever holds it stops reading secrets on its very next fetch. The row stays, marked and dated.",
    confirm: "Revoke key",
  });
  if (!agreed) return;
  try {
    await request(`/api/secrets/keys/${id}`, { method: "DELETE", headers: adminHeaders() });
    forget("secrets.keys");
    await refreshSecrets();
  } catch (failure) {
    status(failure.message);
  }
}

// ---------------------------------------------------------------------------
// What the presses do
// ---------------------------------------------------------------------------

document.addEventListener("click", (event) => {
  if (!qs("[data-secret-envs]")) return;

  if (event.target.closest("[data-env-add]")) { addEnv(); return; }
  const removeEnvButton = event.target.closest("[data-env-remove]");
  if (removeEnvButton) {
    removeEnv(Number(removeEnvButton.dataset.envRemove), removeEnvButton.dataset.envName);
    return;
  }
  const envRowNode = event.target.closest("[data-env-id]");
  if (envRowNode) {
    const id = Number(envRowNode.dataset.envId);
    if (id !== current) {
      current = id;
      // The mask is per screen: "show" was asked about the environment that
      // was open, not about the one being opened.
      shown.clear();
      renderEnvs();
      loadValues();
    }
    return;
  }

  if (event.target.closest("[data-secret-add]")) { addSecret(); return; }
  if (event.target.closest("[data-secret-export]")) { exportEnv(); return; }
  if (event.target.closest("[data-secret-reveal-all]")) {
    for (const box of qsa("[data-secret-value]")) box.type = "text";
    for (const secret of secrets) shown.add(secret.id);
    return;
  }

  if (event.target.closest("[data-secret-import-open]")) {
    if (!current) { status("Add an environment first."); return; }
    openImport(importPanel());
    return;
  }
  if (event.target.closest("[data-secret-import-cancel]")) { importPanel().close(); return; }
  if (event.target.closest("[data-secret-import-check]")) { runImport(true); return; }
  if (event.target.closest("[data-secret-import-apply]")) { runImport(false); return; }

  if (event.target.closest("[data-key-add]")) { addKey(); return; }
  const revoke = event.target.closest("[data-key-revoke]");
  if (revoke) { revokeKey(Number(revoke.closest("[data-key-id]").dataset.keyId)); return; }

  if (event.target.closest("[data-secret-token-copy]")) {
    navigator.clipboard?.writeText(qs("[data-secret-token-value]").textContent);
    return;
  }
  if (event.target.closest("[data-secret-token-done]")) {
    const panel = qs("[data-secret-token]");
    qs("[data-secret-token-value]", panel).textContent = "";
    panel.hidden = true;
    return;
  }

  const row = event.target.closest("[data-secret-id]");
  if (!row) return;
  if (event.target.closest("[data-secret-save]")) { saveSecret(row); return; }
  if (event.target.closest("[data-secret-remove]")) { removeSecret(row); return; }
  if (event.target.closest("[data-secret-show]")) {
    const box = qs("[data-secret-value]", row);
    const id = Number(row.dataset.secretId);
    box.type = box.type === "password" ? "text" : "password";
    if (box.type === "text") shown.add(id);
    else shown.delete(id);
    return;
  }
  if (event.target.closest("[data-secret-copy]")) {
    navigator.clipboard?.writeText(qs("[data-secret-value]", row).value);
  }
});

// Enter saves the row it was typed in, because a table of one-line values is a
// form, and reaching for the mouse after every value is what makes people paste
// a whole file into a text editor instead.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Enter") return;
  const row = event.target.closest("[data-secret-id]");
  if (row && event.target.matches("[data-secret-key], [data-secret-value]")) {
    event.preventDefault();
    saveSecret(row);
  }
});
