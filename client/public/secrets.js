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

let workspaces = [];
let envs = [];
let secrets = [];
let keys = [];
// Which application and which of its stages are open. Kept in the module rather
// than the URL: they survive navigating away and back, and "hushkey/production"
// in the address bar is one more place it gets copied into a chat.
let space = 0;
let current = 0;
// Which values are unmasked, by secret id. Cleared on every environment change,
// because "show" was asked about one screen and not about the next.
const shown = new Set();

export async function refreshSecrets() {
  if (!qs("[data-secret-envs]")) return;
  try {
    await ensure("secrets.workspaces", () => request("/api/secrets", { headers: adminHeaders() }), (answer) => {
      workspaces = answer || [];
      if (!workspaces.some((entry) => entry.id === space)) space = workspaces[0]?.id || 0;
      renderWorkspaces();
    });
    await loadEnvs();
    await Promise.all([loadValues(), loadKeys()]);
  } catch (failure) {
    status(failure.message);
  }
}

async function loadEnvs() {
  if (!space) {
    envs = [];
    current = 0;
    renderEnvs();
    return;
  }
  await ensure(`secrets.envs.${space}`,
    () => request(`/api/secrets/envs?workspace=${space}`, { headers: adminHeaders() }),
    (answer) => {
      envs = answer || [];
      if (!envs.some((env) => env.id === current)) current = envs[0]?.id || 0;
      renderEnvs();
    });
}

function status(message) {
  const note = qs("[data-secret-status]");
  if (!note) return;
  note.textContent = message || "";
  note.hidden = !message;
}

// ---------------------------------------------------------------------------
// Workspaces
// ---------------------------------------------------------------------------

function renderWorkspaces() {
  const picker = qs("[data-secret-workspaces]");
  if (!picker) return;
  picker.replaceChildren(...workspaces.map((entry) => {
    const option = el("option", "", entry.name);
    option.value = entry.id;
    option.selected = entry.id === space;
    return option;
  }));
  const counts = qs("[data-workspace-counts]");
  const open = workspaces.find((entry) => entry.id === space);
  if (counts) {
    counts.textContent = open
      ? `${open.envs} environment${open.envs === 1 ? "" : "s"} · ${open.secrets} secret${open.secrets === 1 ? "" : "s"} · ${open.keys} key${open.keys === 1 ? "" : "s"}`
      : "No application yet — add one to start.";
  }
}

async function addWorkspace() {
  const name = prompt("New application (pack, hushkey, auth…)", "");
  if (name === null || !name.trim()) return;
  try {
    const saved = await request("/api/secrets", {
      method: "POST", headers: adminHeaders(), body: JSON.stringify({ name: name.trim() }),
    });
    // It arrives with the four stages in it, so open the first one rather than
    // leaving somebody looking at an empty right-hand column.
    space = saved.id;
    current = 0;
    shown.clear();
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

async function removeWorkspace() {
  const open = workspaces.find((entry) => entry.id === space);
  if (!open) return;
  // Typing the name, like locking a machine: this takes every environment,
  // every secret and every key underneath it.
  const agreed = await ask({
    title: `Delete ${open.name}?`,
    body: `Its ${open.envs} environment(s), ${open.secrets} secret(s) and ${open.keys} key(s) go with it. Anything still holding one of those keys stops booting.`,
    detail: "Type the application's name to confirm.",
    confirm: "Delete application",
    phrase: open.name,
  });
  if (!agreed) return;
  try {
    await request(`/api/secrets/${space}`, { method: "DELETE", headers: adminHeaders() });
    space = 0;
    current = 0;
    await reload();
  } catch (failure) {
    status(failure.message);
  }
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
  // Qualified, because "production" alone on a page with eight applications is
  // the one heading somebody should never have to guess at.
  if (name) name.textContent = open ? `${open.workspace} / ${open.name}` : "No environment";
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
  if (!space) { status("Add an application first."); return; }
  const name = prompt("New environment name", "");
  if (name === null || !name.trim()) return;
  try {
    const saved = await request("/api/secrets/envs", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ workspace_id: space, name: name.trim() }),
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
    await request(`/api/secrets/envs/${id}`, { method: "DELETE", headers: adminHeaders() });
    if (current === id) current = 0;
    await reload();
  } catch (failure) {
    status(failure.message);
  }
}

// reload drops what the store is holding and asks again — after a write, where
// the counts, the rows and the keys have all moved at once.
async function reload() {
  forget("secrets.workspaces");
  forget(`secrets.envs.${space}`);
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
// Compare, and copy across
// ---------------------------------------------------------------------------
//
// One dialog, two modes. Compare reads several environments and writes
// nothing; Duplicate reads exactly two and puts an arrow beside every key that
// disagrees. They share a table because they are the same question — which
// keys do not match — and two tables would eventually answer it differently.
//
// The colours come down decided (`GET /api/secrets/compare`), which is what
// makes the table readable with every value still masked: three states answer
// "is production configured like staging" without putting production on a
// screen somebody is sharing. Show values is for the moment the difference
// itself is the thing needed, and it is off every time the dialog opens.
//
// Copying is the PUT the row's own Save button uses, once per key. No bulk
// write endpoint: a second way to write a secret is a second thing to get
// wrong, and this one is exercised every day.

// Matches model.MaxCompare. Eight fixed columns is where a row stops being
// readable at a glance, and reading it at a glance is the point.
const MAX_COMPARE = 8;

// Which mode the dialog is in, which environments it is showing (in column
// order) and the last answer drawn. Everything here is rebuilt on open, so a
// closed dialog costs nothing on the page's tick — the rows are only ever
// touched by a press.
let compareMode = "";
let comparePick = [];
let comparison = null;

// The three states, as the three colours already in the palette. Enumerated
// literally rather than assembled, because Tailwind only emits classes it can
// find in the source.
const CELL_TONE = {
  same: "border-success/40 bg-success/10 text-success",
  different: "border-warning/40 bg-warning/15 text-warning",
  missing: "border-destructive/40 bg-destructive/10 text-destructive",
  unreadable: "border-destructive/40 bg-destructive/10 text-destructive",
};

function comparePanel() { return qs("[data-secret-compare]"); }

function envName(id) {
  const env = envs.find((entry) => entry.id === id);
  return env ? env.name : "—";
}

function openCompare(mode) {
  const panel = comparePanel();
  if (!panel) return;
  // Within one application, always. Two workspaces' environments hold
  // unrelated keys, so a table comparing hushkey/production against
  // auth/production is a page of red boxes that means nothing.
  if (envs.length < 2) {
    status("This application has one environment — there is nothing to compare it with.");
    return;
  }
  compareMode = mode;
  comparison = null;
  const from = envs.some((env) => env.id === current) ? current : envs[0].id;
  comparePick = mode === "duplicate"
    ? [from, envs.find((env) => env.id !== from).id]
    : envs.slice(0, MAX_COMPARE).map((env) => env.id);
  qs("[data-compare-reveal]", panel).checked = false;
  qs("[data-compare-only-diff]", panel).checked = false;
  qs("[data-compare-status]", panel).textContent = "";
  qs("[data-compare-pair]", panel).hidden = mode !== "duplicate";
  qs("[data-compare-picker]", panel).hidden = mode === "duplicate";
  qs("[data-compare-copy-all]", panel).hidden = mode !== "duplicate";
  qs("[data-compare-title]", panel).textContent = mode === "duplicate"
    ? "Duplicate an environment"
    : "Compare environments";
  qs("[data-compare-blurb]", panel).textContent = mode === "duplicate"
    ? "An arrow copies that one value into the environment on the right; the × deletes it from there. Values already the same are shown with a ×, because there is nothing left to copy."
    : "Read only. A cell is green when every environment here has that key with the same value, amber when they disagree, and red where it is not set at all — which works with the values still masked.";
  renderComparePair();
  renderComparePicker();
  panel.showModal();
  loadComparison();
}

function renderComparePair() {
  const panel = comparePanel();
  for (const [selector, chosen] of [["[data-compare-from]", comparePick[0]], ["[data-compare-to]", comparePick[1]]]) {
    const picker = qs(selector, panel);
    picker.replaceChildren(...envs.map((env) => {
      const option = el("option", "", env.name);
      option.value = env.id;
      option.selected = env.id === chosen;
      return option;
    }));
  }
}

function renderComparePicker() {
  const host = qs("[data-compare-picker]", comparePanel());
  host.replaceChildren(...envs.map((env) => {
    const chosen = comparePick.includes(env.id);
    const chip = el("label", `flex cursor-pointer items-center gap-2 rounded-full border px-3 py-1 text-xs ${
      chosen ? "border-primary/50 bg-primary/10 text-foreground" : "border-border text-muted-foreground"}`);
    const box = el("input", "cn-checkbox size-3.5");
    box.type = "checkbox";
    box.checked = chosen;
    box.dataset.compareEnv = env.id;
    chip.append(box, env.name);
    return chip;
  }));
}

async function loadComparison() {
  const panel = comparePanel();
  if (!panel?.open) return;
  const note = qs("[data-compare-status]", panel);
  try {
    comparison = await request(`/api/secrets/compare?envs=${comparePick.join(",")}`, { headers: adminHeaders() });
  } catch (failure) {
    comparison = null;
    note.textContent = failure.message;
  }
  renderComparison();
}

// The action beside a row, in duplicate mode. The rule the buttons draw:
// there is something to copy until the two agree, and once they do the only
// thing left to do to that key is take it away.
function duplicateAction(row) {
  const [from, into] = row.cells;
  const agreed = from.state === "same" && into.state === "same";
  if (into.present && (!from.present || agreed)) return "drop";
  // Nothing readable to send: a value sealed with a key this instance no
  // longer has would be copied across as an empty string.
  if (from.present && from.state !== "unreadable") return "copy";
  return "none";
}

function renderComparison() {
  const panel = comparePanel();
  const host = qs("[data-compare-rows]", panel);
  const head = qs("[data-compare-head]", panel);
  const template = qs("[data-compare-row-template]");
  const empty = qs("[data-compare-empty]", panel);
  const summary = qs("[data-compare-summary]", panel);
  if (!host || !head || !template) return;
  if (!comparison) {
    head.replaceChildren();
    host.replaceChildren();
    summary.textContent = "";
    empty.hidden = true;
    return;
  }

  const names = comparison.envs.map((env) => env.name);
  // A track per environment, plus one for the two presses where there are
  // presses. Assembled as a style because a class built from a variable is
  // one Tailwind never emits.
  const columns = `minmax(8rem,14rem) repeat(${names.length}, minmax(0,1fr))${
    compareMode === "duplicate" ? " 2.25rem" : ""}`;
  head.style.gridTemplateColumns = columns;
  head.replaceChildren(
    el("span", "", "Key"),
    ...names.map((name) => el("span", "truncate", name)),
    ...(compareMode === "duplicate" ? [el("span", "")] : []),
  );

  const revealed = qs("[data-compare-reveal]", panel).checked;
  const onlyDiff = qs("[data-compare-only-diff]", panel).checked;
  const rows = onlyDiff ? comparison.rows.filter((row) => row.state !== "same") : comparison.rows;
  host.replaceChildren(...rows.map((row) => comparisonRow(template, row, columns, names, revealed)));

  empty.hidden = rows.length > 0;
  empty.textContent = comparison.rows.length
    ? "Every key matches in all of these environments."
    : "None of these environments holds anything yet.";
  summary.textContent = `${comparison.rows.length} key${comparison.rows.length === 1 ? "" : "s"} across ` +
    `${names.length} environments — ${comparison.same} the same, ${comparison.different} differ, ` +
    `${comparison.missing} missing somewhere.`;
}

function comparisonRow(template, row, columns, names, revealed) {
  const node = template.content.firstElementChild.cloneNode(true);
  node.dataset.compareRow = row.key;
  node.style.gridTemplateColumns = columns;
  const key = qs("[data-compare-key]", node);
  key.textContent = row.key;
  key.title = row.key;
  const action = qs("[data-compare-action]", node);
  for (const cell of row.cells) node.insertBefore(compareCell(cell, revealed), action);
  if (compareMode !== "duplicate") {
    action.hidden = true;
    return node;
  }
  const verdict = duplicateAction(row);
  const copy = qs("[data-compare-copy]", node);
  const drop = qs("[data-compare-drop]", node);
  copy.hidden = verdict !== "copy";
  drop.hidden = verdict !== "drop";
  copy.title = `Copy into ${names[1]}`;
  drop.title = `Delete from ${names[1]}`;
  return node;
}

function compareCell(cell, revealed) {
  const box = el("input", `cn-input h-8 w-full min-w-0 font-mono text-xs ${CELL_TONE[cell.state] || ""}`);
  box.readOnly = true;
  box.spellcheck = false;
  if (cell.state === "missing") {
    box.placeholder = "not set";
    return box;
  }
  if (cell.state === "unreadable") {
    box.placeholder = "unreadable";
    box.title = "sealed with a different key — set it again";
    return box;
  }
  box.value = cell.value;
  box.type = revealed ? "text" : "password";
  return box;
}

// The write side. One PUT per key — the same call the row's Save button makes
// — and then the whole comparison is read again, so what is on screen after a
// press is the database rather than an assumption about it.
async function copyAcross(rows) {
  if (!rows.length) return;
  const panel = comparePanel();
  const note = qs("[data-compare-status]", panel);
  const target = comparePick[1];
  try {
    for (const row of rows) {
      await request("/api/secrets/values", {
        method: "PUT", headers: adminHeaders(),
        body: JSON.stringify({ env_id: target, key: row.key, value: row.cells[0].value }),
      });
    }
    await afterCompareWrite(`${rows.length} value${rows.length === 1 ? "" : "s"} copied into ${envName(target)}.`);
  } catch (failure) {
    note.textContent = failure.message;
    await loadComparison();
  }
}

async function dropAcross(key) {
  const row = comparison?.rows.find((entry) => entry.key === key);
  const cell = row?.cells[1];
  if (!cell?.secret_id) return;
  const target = envName(comparePick[1]);
  const agreed = await ask({
    title: `Delete ${row.key} from ${target}?`,
    body: `Anything reading ${target} gets one fewer variable on its next fetch. The copy in ${envName(comparePick[0])} is untouched.`,
    confirm: "Delete secret",
  });
  if (!agreed) return;
  try {
    await request(`/api/secrets/values/${cell.secret_id}`, { method: "DELETE", headers: adminHeaders() });
    await afterCompareWrite(`${row.key} deleted from ${target}.`);
  } catch (failure) {
    qs("[data-compare-status]", comparePanel()).textContent = failure.message;
  }
}

async function copyEveryDifference() {
  if (!comparison) return;
  const pending = comparison.rows.filter((row) => duplicateAction(row) === "copy");
  const target = envName(comparePick[1]);
  if (!pending.length) {
    qs("[data-compare-status]", comparePanel()).textContent = `Nothing to copy — ${target} already matches.`;
    return;
  }
  const replacing = pending.filter((row) => row.cells[1].present).length;
  const agreed = await ask({
    title: `Copy ${pending.length} value${pending.length === 1 ? "" : "s"} into ${target}?`,
    body: replacing
      ? `${pending.length - replacing} new, ${replacing} replacing a different value. Nothing is deleted — a key ${target} has and ${envName(comparePick[0])} does not is left alone.`
      : `All ${pending.length} are new. Nothing is replaced and nothing is deleted.`,
    confirm: "Copy them",
  });
  if (!agreed) return;
  await copyAcross(pending);
}

// After a write: the counts moved, one of these environments is the one the
// page behind the dialog is showing, and the comparison itself is now out of
// date. All three, rather than only the visible one — a stale row underneath
// is how somebody presses Save on a value that has already changed.
async function afterCompareWrite(message) {
  forget("secrets.workspaces");
  forget(`secrets.envs.${space}`);
  for (const id of comparePick) forget(`secrets.values.${id}`);
  await loadComparison();
  qs("[data-compare-status]", comparePanel()).textContent = message;
  await loadEnvs();
  await loadValues();
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
  qs("[data-key-workspace]", row).textContent = key.workspace || "";
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
  const name = prompt(`What holds this key? (reads ${env ? `${env.workspace}/${env.name}` : "this environment"})`, "");
  if (name === null || !name.trim()) return;
  try {
    const minted = await request("/api/secrets/keys", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ env_id: current, name: name.trim() }),
    });
    showToken(minted.token);
    forget("secrets.keys");
    forget("secrets.workspaces");
    forget(`secrets.envs.${space}`);
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

  if (event.target.closest("[data-workspace-add]")) { addWorkspace(); return; }
  if (event.target.closest("[data-workspace-remove]")) { removeWorkspace(); return; }
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

  if (event.target.closest("[data-secret-compare-open]")) { openCompare("compare"); return; }
  if (event.target.closest("[data-secret-duplicate-open]")) { openCompare("duplicate"); return; }
  if (event.target.closest("[data-compare-close]")) { comparePanel().close(); return; }
  if (event.target.closest("[data-compare-copy-all]")) { copyEveryDifference(); return; }
  if (event.target.closest("[data-compare-swap]")) {
    comparePick = [comparePick[1], comparePick[0]];
    renderComparePair();
    loadComparison();
    return;
  }
  const compareRow = event.target.closest("[data-compare-row]");
  if (compareRow) {
    const key = compareRow.dataset.compareRow;
    if (event.target.closest("[data-compare-copy]")) {
      copyAcross(comparison.rows.filter((row) => row.key === key));
      return;
    }
    if (event.target.closest("[data-compare-drop]")) { dropAcross(key); return; }
    return;
  }

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

// The workspace picker. A change here is a different application's secrets, so
// everything below it is reloaded and nothing shown stays shown.
document.addEventListener("change", (event) => {
  // The compare dialog's own controls. Reveal and the filter are redraws of an
  // answer already in hand; the pickers are a different question, so they ask
  // it again.
  if (event.target.matches("[data-compare-reveal], [data-compare-only-diff]")) {
    renderComparison();
    return;
  }
  if (event.target.matches("[data-compare-from], [data-compare-to]")) {
    const from = Number(qs("[data-compare-from]").value);
    const into = Number(qs("[data-compare-to]").value);
    // An environment compared with itself is two identical columns proving
    // nothing, so the other select steps aside rather than refusing.
    comparePick = from === into
      ? (event.target.matches("[data-compare-from]")
        ? [from, envs.find((env) => env.id !== from).id]
        : [envs.find((env) => env.id !== into).id, into])
      : [from, into];
    renderComparePair();
    loadComparison();
    return;
  }
  const chosen = event.target.closest("[data-compare-env]");
  if (chosen) {
    const id = Number(chosen.dataset.compareEnv);
    const next = chosen.checked ? [...comparePick, id] : comparePick.filter((entry) => entry !== id);
    const note = qs("[data-compare-status]");
    if (next.length < 2 || next.length > MAX_COMPARE) {
      chosen.checked = !chosen.checked;
      note.textContent = next.length < 2
        ? "A comparison needs at least two environments."
        : `Compare up to ${MAX_COMPARE} at a time.`;
      return;
    }
    note.textContent = "";
    // Kept in the environment list's own order, so the columns do not
    // rearrange themselves as boxes are ticked.
    comparePick = envs.filter((env) => next.includes(env.id)).map((env) => env.id);
    renderComparePicker();
    loadComparison();
    return;
  }

  const picker = event.target.closest("[data-secret-workspaces]");
  if (!picker) return;
  space = Number(picker.value);
  current = 0;
  shown.clear();
  renderWorkspaces();
  loadEnvs().then(() => Promise.all([loadValues(), loadKeys()])).catch((failure) => status(failure.message));
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
