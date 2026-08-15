// The cloud account behind a machine: what the provider says, and the three
// things guard can ask it to do.
//
// Two surfaces, the same split the rest of the cluster keeps. /cluster is
// where a linked machine gets its provider strip — power state, plan,
// transfer, the switch and the snapshots — because that page is where things
// are run. /settings/cluster is where the link itself is made and where
// instances are imported, because that page is where machines are declared.
//
// Everything here is fetched on a press or on a mount, never on the live
// tick. Behind these calls is somebody's API rate limit rather than guard's
// own SQLite, and a power state twenty seconds old has never been what made
// an outage worse.
import { adminHeaders, el, muted, qs, qsa, relativeTime, request } from "./core.js";
import { ensure, forget, get as stored } from "./store.js";
import { ask } from "./cluster.js";

// What the provider last said about one machine, and when. Kept per node so a
// card rebuilt on the three-second tick redraws from memory instead of asking
// the provider again.
const facts = new Map(); // nodeID -> { at, data, error }
const inFlight = new Map(); // nodeID -> promise
const snapshots = new Map(); // nodeID -> { at, rows, error }
const factsTTL = 60_000;

// Which cards have their snapshot list open. A fold is a reading position,
// and a redraw that closed it would be a redraw that argued with the reader.
const openSnapshots = new Set();

// The accounts, for the two selects that need them. One request, cached: it
// is guard's own database and it changes when somebody adds a key.
let accounts = null;
let accountsRequest = null;

// The providers guard can talk to, and what each one can be asked for. Also
// one request, cached harder: this is the shape of the binary rather than
// anybody's data, and it cannot change without a restart.
//
// Every difference between providers on these pages is read from here. What
// the secret is called, whether the form needs an account id, whether an
// account has machines to link a row to — all of it is the server's answer,
// derived from what each provider package implements, so a button that would
// fail is never drawn.
let providers = null;
let providersRequest = null;

// The instances one account runs, for the link picker and the import list.
const instances = new Map(); // accountID -> { at, rows, error }

const states = {
  running: "cn-badge-variant-default",
  stopped: "cn-badge-variant-destructive",
  pending: "cn-badge-variant-secondary",
};

function bytes(value) {
  if (!value) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

// The stored accounts, held in the session's store rather than in a variable
// this module owns: three pages read them, and walking between those pages
// should not re-ask. `force` is what a save uses — the answer changed, and the
// store should hear about it now rather than on somebody's next navigation.
export async function cloudAccounts(force = false) {
  if (!force) {
    const known = stored("cloud.accounts");
    if (known) {
      accounts = known;
      // Answer from the store and confirm behind the caller's back, so the
      // next page to ask has a current list without this one having waited.
      // Nothing is redrawn here: the caller already drew, and the store only
      // wakes anybody if the answer actually moved.
      ensure("cloud.accounts", () => request("/api/cloud/accounts").catch(() => []), (list) => {
        accounts = list || [];
      }).catch(() => {});
      return accounts;
    }
  }
  return ensure("cloud.accounts", () => request("/api/cloud/accounts").catch(() => []), (list) => {
    accounts = list || [];
  });
}

export async function cloudProviders() {
  if (providers) return providers;
  if (!providersRequest) {
    providersRequest = request("/api/cloud/providers")
      .then((list) => { providers = list || []; return providers; })
      .catch(() => { providers = []; return providers; })
      .finally(() => { providersRequest = null; });
  }
  return providersRequest;
}

// can answers "may this account be asked for this?" for one capability. An
// account whose provider guard no longer knows answers no to everything,
// which is the safe way for a row from an older database to behave.
export function can(account, capability) {
  const provider = (providers || []).find((entry) => entry.id === account?.provider);
  return !!provider?.capabilities?.[capability];
}

// ---- the card strip, on /cluster ----

// fillCloudCard is called by cluster.js for every card it draws. A machine
// with no link keeps the section hidden and nothing is fetched: linking is
// opt-in, and an unlinked machine is a perfectly ordinary thing to watch.
export function fillCloudCard(item, node) {
  const host = qs("[data-cloud]", item);
  if (!host) return;
  if (!node.provider_instance_id) { host.hidden = true; return; }
  host.hidden = false;

  const entry = facts.get(node.id);
  const state = qs("[data-cloud-state]", item);
  const meta = qs("[data-cloud-meta]", item);
  const error = qs("[data-cloud-error]", item);
  const transfer = qs("[data-cloud-transfer]", item);
  const bar = qs("[data-cloud-transfer-bar]", item);

  if (!entry) {
    state.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${states.pending}`;
    state.textContent = "reading…";
    meta.textContent = "";
    transfer.textContent = "";
    error.textContent = "";
  } else if (entry.error) {
    state.className = "cn-badge cn-badge-variant-secondary inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap";
    state.textContent = "unreadable";
    meta.textContent = "";
    transfer.textContent = "";
    error.textContent = entry.error;
  } else {
    const instance = entry.data.instance || {};
    const power = instance.power_status || "unknown";
    state.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${states[power] || states.pending}`;
    // The server status matters when it disagrees with the switch: an
    // instance can be "running" and still be installing, which is a
    // different answer to "why is it not serving".
    state.textContent = instance.server_status && instance.server_status !== "ok"
      ? `${power} · ${instance.server_status}`
      : power;
    meta.textContent = [
      instance.plan,
      instance.region,
      instance.vcpu_count ? `${instance.vcpu_count} vCPU` : "",
      instance.ram_mb ? `${Math.round(instance.ram_mb / 1024)} GB` : "",
      instance.main_ip,
    ].filter(Boolean).join(" · ");
    error.textContent = "";

    const used = (entry.data.bandwidth?.in_bytes || 0) + (entry.data.bandwidth?.out_bytes || 0);
    const allowed = (instance.allowed_bandwidth_gb || 0) * 1024 ** 3;
    if (allowed > 0) {
      const share = Math.min(100, Math.round((used / allowed) * 100));
      bar.style.width = `${share}%`;
      // Past four fifths the bar stops being decoration: the next thing that
      // happens is an overage nobody chose.
      bar.className = share >= 80 ? "h-full rounded-full bg-destructive" : "h-full rounded-full bg-primary";
      transfer.textContent = `${bytes(used)} of ${instance.allowed_bandwidth_gb} GB this month`;
    } else {
      bar.style.width = "0%";
      transfer.textContent = entry.data.transfer_error ? "transfer unavailable" : "";
    }
  }

  const open = openSnapshots.has(node.id);
  qs("[data-cloud-snapshots]", item).hidden = !open;
  qs("[data-cloud-snapshots-toggle]", item)?.setAttribute("aria-expanded", open ? "true" : "false");
  if (open) fillSnapshots(item, node);

  // A locked machine can be read and powered, and refuses everything that
  // takes something away. The buttons say so rather than failing on click.
  for (const control of qsa("[data-snapshot-restore],[data-snapshot-delete]", item)) {
    if (node.locked) { control.disabled = true; control.title = "This machine is locked"; }
  }

  loadFacts(node.id).catch(() => {});
}

function fillSnapshots(item, node) {
  const host = qs("[data-cloud-snapshots]", item);
  const template = qs("[data-cloud-snapshot-template]");
  if (!host || !template) return;
  const entry = snapshots.get(node.id);
  if (!entry) {
    host.replaceChildren(el("p", `text-xs ${muted}`, "Reading snapshots…"));
    loadSnapshots(node.id).catch(() => {});
    return;
  }
  if (entry.error) {
    host.replaceChildren(el("p", "text-xs text-destructive", entry.error));
    return;
  }
  const rows = entry.rows;
  if (!rows.length) {
    host.replaceChildren(el("p", `text-xs ${muted}`, "No snapshots on this account yet."));
    return;
  }
  host.replaceChildren(...rows.map((row) => {
    const chip = template.content.firstElementChild.cloneNode(true);
    chip.dataset.snapshot = row.snapshot.id;
    // Guard's own snapshots of this machine first and plainly; the rest of
    // the account is listed dimmer, because it is still restorable and
    // hiding it would send somebody to another website to find it.
    chip.className = row.ours
      ? "flex items-center gap-3 rounded-lg bg-muted/40 px-3 py-2"
      : "flex items-center gap-3 rounded-lg bg-muted/20 px-3 py-2 opacity-70";
    qs("[data-snapshot-name]", chip).textContent = row.snapshot.description || row.snapshot.id;
    const parts = [row.ours ? "this machine" : "elsewhere in the account"];
    if (row.snapshot.created) parts.push(relativeTime(row.snapshot.created));
    if (row.snapshot.size_bytes) parts.push(bytes(row.snapshot.size_bytes));
    if (row.snapshot.status && row.snapshot.status !== "complete") parts.push(row.snapshot.status);
    qs("[data-snapshot-meta]", chip).textContent = parts.join(" · ");
    if (row.snapshot.status && row.snapshot.status !== "complete") {
      const restore = qs("[data-snapshot-restore]", chip);
      restore.disabled = true;
      restore.title = "This image is still being written";
    }
    return chip;
  }));
}

async function loadFacts(nodeID, force = false) {
  const entry = facts.get(nodeID);
  if (!force && entry && Date.now() - entry.at < factsTTL) return entry;
  if (inFlight.has(nodeID)) return inFlight.get(nodeID);
  const work = request(`/api/cluster/provider?node=${nodeID}`)
    .then((data) => ({ at: Date.now(), data }))
    .catch((failure) => ({ at: Date.now(), error: failure.message }))
    .then((result) => { facts.set(nodeID, result); inFlight.delete(nodeID); redraw(); return result; });
  inFlight.set(nodeID, work);
  return work;
}

async function loadSnapshots(nodeID, force = false) {
  const entry = snapshots.get(nodeID);
  if (!force && entry && Date.now() - entry.at < factsTTL) return entry;
  const result = await request(`/api/cluster/provider/snapshots?node=${nodeID}`)
    .then((rows) => ({ at: Date.now(), rows: rows || [] }))
    .catch((failure) => ({ at: Date.now(), rows: [], error: failure.message }));
  snapshots.set(nodeID, result);
  redraw();
  return result;
}

// The cards are cluster.js's to draw. Asking it to redraw is one custom
// event rather than an import back the other way, which would be a cycle.
function redraw() {
  document.dispatchEvent(new CustomEvent("guard:cloud-updated"));
}

// ---- the link, on /settings/cluster ----

// fillCloudDetail fills one row's cloud section. Called by cluster.js when a
// row is opened and on every redraw of an open row.
export async function fillCloudDetail(panel, node) {
  const section = qs("[data-node-cloud]", panel);
  if (!section) return;
  qs("[data-cloud-linked]", section).hidden = !node.provider_instance_id;
  const accountSelect = qs("[data-cloud-account]", section);
  const instanceSelect = qs("[data-cloud-instance]", section);
  const note = qs("[data-cloud-note]", section);

  // A locked machine keeps its link and cannot be repointed: a link is a new
  // way to act on a machine somebody finished configuring.
  const frozen = !!node.locked;
  qs("[data-cloud-link]", section).disabled = frozen;
  qs("[data-cloud-unlink]", section).disabled = frozen || !node.provider_instance_id;
  accountSelect.disabled = frozen;
  instanceSelect.disabled = frozen;
  if (frozen) note.textContent = "This machine is locked: its cloud link cannot be changed.";

  const list = await computeAccounts();
  if (!list.length) {
    accountSelect.replaceChildren(new Option("No accounts with machines", ""));
    instanceSelect.replaceChildren(new Option("—", ""));
    note.textContent = "Link a machine to an account at a provider that runs machines — add one under Settings → Cloud accounts.";
    return;
  }
  const chosen = Number(accountSelect.value) || node.provider_account_id || list[0].id;
  accountSelect.replaceChildren(...list.map((account) => {
    const option = new Option(account.name, String(account.id));
    option.selected = account.id === chosen;
    return option;
  }));
  await fillInstanceOptions(instanceSelect, chosen, node.provider_instance_id);
}

// computeAccounts is the accounts a machine can actually be linked to. A
// Cloudflare account has no instance to point a row at, and offering one
// would be offering a power switch for something that does not exist.
async function computeAccounts() {
  const [list] = await Promise.all([cloudAccounts(), cloudProviders()]);
  return list.filter((account) => can(account, "compute"));
}

async function fillInstanceOptions(select, accountID, selected) {
  const entry = await loadInstances(accountID);
  if (entry.error) {
    select.replaceChildren(new Option(entry.error, ""));
    return;
  }
  const options = [new Option("Not linked", "")];
  for (const row of entry.rows) {
    const label = [row.instance.label || row.instance.id, row.instance.main_ip, row.instance.region]
      .filter(Boolean).join(" · ");
    // An instance another machine already watches is listed and refused,
    // rather than hidden: "where is my box" is a worse question than "why is
    // this one greyed out".
    const option = new Option(row.node_id && row.instance.id !== selected ? `${label} — watched by ${row.node_name}` : label, row.instance.id);
    option.disabled = !!row.node_id && row.instance.id !== selected;
    option.selected = row.instance.id === selected;
    options.push(option);
  }
  select.replaceChildren(...options);
}

async function loadInstances(accountID, force = false) {
  const entry = instances.get(accountID);
  if (!force && entry && Date.now() - entry.at < factsTTL) return entry;
  const result = await request(`/api/cluster/provider/instances?account=${accountID}`, { headers: adminHeaders() })
    .then((rows) => ({ at: Date.now(), rows: rows || [] }))
    .catch((failure) => ({ at: Date.now(), rows: [], error: failure.message }));
  instances.set(accountID, result);
  return result;
}

// ---- the import list, on /settings/cluster ----

export async function refreshImport(force = false) {
  const host = qs("[data-import-rows]");
  if (!host) return;
  const select = qs("[data-import-account]");
  const list = await computeAccounts();
  if (!list.length) {
    select.replaceChildren(new Option("No accounts with machines", ""));
    host.replaceChildren();
    importStatus("Importing needs an account at a provider that runs machines. Add one under Settings → Cloud accounts.");
    return;
  }
  if (!select.options.length || force) {
    const chosen = Number(select.value) || list[0].id;
    select.replaceChildren(...list.map((account) => {
      const option = new Option(account.name, String(account.id));
      option.selected = account.id === chosen;
      return option;
    }));
  }
  if (!force) return; // the list itself is a press, not a render
  const accountID = Number(select.value);
  importStatus("Asking the provider…");
  const entry = await loadInstances(accountID, true);
  if (entry.error) { importStatus(entry.error); host.replaceChildren(); return; }
  const template = qs("[data-import-row-template]");
  host.replaceChildren(...entry.rows.map((row) => {
    const item = template.content.firstElementChild.cloneNode(true);
    item.dataset.importRow = row.instance.id;
    item.dataset.account = String(accountID);
    qs("[data-import-label]", item).textContent = row.instance.label || row.instance.id;
    qs("[data-import-meta]", item).textContent = [
      row.instance.main_ip, row.instance.region, row.instance.plan, row.instance.power_status,
    ].filter(Boolean).join(" · ");
    const watched = qs("[data-import-watched]", item);
    const add = qs("[data-import-add]", item);
    if (row.node_id) {
      watched.textContent = `watched by ${row.node_name}`;
      add.disabled = true;
    }
    return item;
  }));
  importStatus(entry.rows.length ? "" : "This account runs no instances.");
}

function importStatus(message) {
  const note = qs("[data-import-status]");
  if (note) note.textContent = message;
}

// ---- the accounts page, /settings/cloud ----

export async function refreshCloud(force = false) {
  const work = [];
  if (qs("[data-cloud-account-form]")) work.push(fillProviderOptions());
  if (qs("[data-cloud-accounts]")) work.push(refreshAccountRows());
  if (qs("[data-import-rows]")) work.push(refreshImport(false));
  await Promise.allSettled(work);
  if (force) {
    accounts = null;
    forget("cloud.accounts");
  }
}

// fillProviderOptions draws the add-account form from what the server says
// exists. It runs once per mount and again on nothing: the provider list is
// the binary's shape.
async function fillProviderOptions() {
  const form = qs("[data-cloud-account-form]");
  const select = qs('[data-account="provider"]', form);
  if (!select) return;
  const list = await cloudProviders();
  if (!list.length) return;
  const chosen = select.value || list[0].id;
  select.replaceChildren(...list.map((provider) => {
    const option = new Option(provider.label, provider.id);
    option.selected = provider.id === chosen;
    return option;
  }));
  describeProvider(form);
}

// describeProvider says what the chosen provider calls its own secret, and
// asks for an account id only where one is needed. Cloudflare's every
// endpoint hangs off an account id; Vultr's key names its own account, and a
// box for one there would be a box nobody can fill.
function describeProvider(form) {
  const list = providers || [];
  const chosen = qs('[data-account="provider"]', form)?.value;
  const provider = list.find((entry) => entry.id === chosen) || list[0];
  if (!provider) return;
  const key = qs('[data-account="api_key"]', form);
  if (key) key.placeholder = `The provider's ${provider.key_label}`;
  const keyLabel = qs("[data-account-key-label]", form);
  if (keyLabel) keyLabel.textContent = provider.key_label;
  const keyHint = qs("[data-account-key-hint]", form);
  if (keyHint) keyHint.textContent = provider.key_hint ? ` ${provider.key_hint}` : "";

  const field = qs("[data-account-external-field]", form);
  const needed = !!provider.capabilities?.needs_account_id;
  field.hidden = !needed;
  const external = qs('[data-account="external_id"]', form);
  external.required = needed;
  if (!needed) external.value = "";
  qs("[data-account-external-label]", form).textContent = provider.account_label || "Account ID";
  qs("[data-account-external-hint]", form).textContent = provider.account_hint || "";

  // The S3 pair is optional and only for the providers that need one stored.
  // Vultr reads its own per subscription, so asking for one there would be a
  // box that does nothing.
  const s3 = qs("[data-account-s3-field]", form);
  const wanted = !!provider.s3_label;
  s3.hidden = !wanted;
  if (!wanted) {
    qs('[data-account="s3_access_key"]', form).value = "";
    qs('[data-account="s3_secret_key"]', form).value = "";
  }
  qs("[data-account-s3-label]", form).textContent = provider.s3_label || "S3 access key";
  qs("[data-account-s3-hint]", form).textContent = provider.s3_hint || "";
}

async function refreshAccountRows() {
  const body = qs("[data-cloud-accounts]");
  if (!body) return;
  try {
    const [list] = await Promise.all([cloudAccounts(true), cloudProviders()]);
    if (!list.length) {
      body.replaceChildren(row(4, "No accounts stored. Add a provider API key above."));
      return;
    }
    body.replaceChildren(...list.map((account) => {
      const line = document.createElement("tr");
      line.className = "cn-table-row";
      line.append(
        cell(account.name, "font-medium"),
        // The provider's own label, and under it the account id for the
        // providers that need one — it is not a secret, and seeing which
        // account a key points at is half of knowing whether it is the right
        // key.
        providerCell(account),
        // Dots, the same as the SSH passwords: the server said has_key and
        // nothing else, and the count says nothing about the length.
        cell(account.has_key ? "••••••••••••" : "—", `font-mono text-xs ${muted}`),
        actionCell(account),
      );
      return line;
    }));
  } catch (failure) {
    body.replaceChildren(row(4, failure.message));
  }
}

const cellBase = "cn-table-cell cn-table-cell-aria";

function cell(value, className = "") {
  const td = el("td", `${cellBase} ${className}`.trim());
  td.textContent = value ?? "";
  return td;
}

function providerCell(account) {
  const td = el("td", cellBase);
  const provider = (providers || []).find((entry) => entry.id === account.provider);
  td.append(el("span", "text-sm", provider?.label || account.provider));
  if (account.external_id) {
    td.append(el("p", `font-mono text-xs ${muted}`, account.external_id));
  }
  // A stored S3 pair is worth saying out loud: it is the difference between an
  // account whose buckets can be opened and one whose cannot.
  if (account.has_s3_keys) {
    td.append(el("p", `text-xs ${muted}`, "S3 key stored — buckets can be browsed"));
  }
  return td;
}

function actionCell(account) {
  const td = el("td", `${cellBase} text-right`);
  // The S3 pair is offered only where the provider needs one stored — Vultr
  // reads its own per subscription — and the word changes with what is there,
  // because "Add" and "Replace" are different decisions.
  const provider = (providers || []).find((entry) => entry.id === account.provider);
  if (provider?.s3_label) {
    const keys = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm text-muted-foreground",
      account.has_s3_keys ? "Replace S3 keys" : "Add S3 keys");
    keys.type = "button";
    keys.dataset.accountS3 = account.id;
    keys.dataset.accountS3Name = account.name;
    td.append(keys);
  }
  const control = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm text-muted-foreground hover:text-destructive", "Remove");
  control.type = "button";
  control.dataset.accountDelete = account.id;
  control.dataset.accountDeleteName = account.name;
  td.append(control);
  return td;
}

function row(columns, message) {
  const line = document.createElement("tr");
  line.className = "cn-table-row";
  const td = el("td", `${cellBase} ${muted}`, message);
  td.colSpan = columns;
  line.append(td);
  return line;
}

// ---- the S3 pair on an existing account ----

let s3For = 0;

function openS3(id, name) {
  s3For = id;
  const form = qs("[data-s3-form]");
  if (!form) return;
  form.hidden = false;
  qs("[data-s3-account]", form).textContent = name;
  qs('[data-s3="access"]', form).value = "";
  qs('[data-s3="secret"]', form).value = "";
  qs("[data-s3-status]", form).textContent = "";
  qs('[data-s3="access"]', form).focus();
}

function closeS3() {
  s3For = 0;
  const form = qs("[data-s3-form]");
  if (form) form.hidden = true;
}

async function saveS3() {
  const form = qs("[data-s3-form]");
  const note = qs("[data-s3-status]", form);
  const access = qs('[data-s3="access"]', form).value.trim();
  const secret = qs('[data-s3="secret"]', form).value.trim();
  if ((access === "") !== (secret === "")) {
    note.textContent = "An access key and its secret go together — or leave both blank to forget the pair.";
    return;
  }
  note.textContent = access ? "Checking the pair against the storage…" : "Forgetting the pair…";
  try {
    await request("/api/cloud/accounts/s3", {
      method: "PUT",
      headers: adminHeaders(),
      body: JSON.stringify({ account_id: s3For, s3_access_key: access, s3_secret_key: secret }),
    });
    closeS3();
    accounts = null;
    await refreshAccountRows();
  } catch (failure) {
    note.textContent = failure.message;
  }
}

async function addAccount(form) {
  const value = (name) => qs(`[data-account="${name}"]`, form)?.value || "";
  const note = qs("[data-cloud-account-status]", form);
  note.textContent = "Checking the key against the provider…";
  try {
    await request("/api/cloud/accounts", {
      method: "POST",
      headers: adminHeaders(),
      body: JSON.stringify({
        name: value("name").trim(),
        provider: value("provider"),
        external_id: value("external_id").trim(),
        s3_access_key: value("s3_access_key").trim(),
        s3_secret_key: value("s3_secret_key"),
        api_key: value("api_key"),
      }),
    });
    note.textContent = "Key proved and stored.";
    form.reset();
    accounts = null;
    // A reset puts the provider select back to its first option, so the rest
    // of the form has to be told again which provider it is describing.
    describeProvider(form);
    await refreshAccountRows();
  } catch (failure) {
    note.textContent = failure.message;
  }
}

// ---- what the presses do ----

document.addEventListener("click", (event) => {
  const keys = event.target.closest("[data-account-s3]");
  if (keys) { openS3(Number(keys.dataset.accountS3), keys.dataset.accountS3Name); return; }
  if (event.target.closest("[data-s3-cancel]")) { closeS3(); return; }
  if (event.target.closest("[data-s3-save]")) { saveS3(); return; }
});

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-cloud-account-form]")) return;
  event.preventDefault();
  addAccount(event.target);
});

document.addEventListener("change", (event) => {
  const providerSelect = event.target.closest('[data-account="provider"]');
  if (providerSelect) {
    describeProvider(providerSelect.closest("[data-cloud-account-form]"));
    return;
  }
  const accountSelect = event.target.closest("[data-cloud-account]");
  if (accountSelect) {
    const panel = accountSelect.closest("[data-node-cloud]");
    fillInstanceOptions(qs("[data-cloud-instance]", panel), Number(accountSelect.value), "");
    return;
  }
  if (event.target.closest("[data-import-account]")) refreshImport(true);
});

document.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-account-delete]");
  if (remove) {
    removeAccount(remove.dataset.accountDelete, remove.dataset.accountDeleteName);
    return;
  }
  if (event.target.closest("[data-import-refresh]")) { refreshImport(true); return; }
  const importAdd = event.target.closest("[data-import-add]");
  if (importAdd) { importInstance(importAdd.closest("[data-import-row]")); return; }

  const link = event.target.closest("[data-cloud-link]");
  if (link) { saveLink(link.closest("[data-node-cloud]"), false); return; }
  const unlink = event.target.closest("[data-cloud-unlink]");
  if (unlink) { saveLink(unlink.closest("[data-node-cloud]"), true); return; }

  const card = event.target.closest("[data-node-id]");
  if (!card) return;
  const nodeID = Number(card.dataset.nodeId);

  const toggle = event.target.closest("[data-cloud-snapshots-toggle]");
  if (toggle) {
    if (openSnapshots.has(nodeID)) openSnapshots.delete(nodeID);
    else { openSnapshots.add(nodeID); loadSnapshots(nodeID).catch(() => {}); }
    redraw();
    return;
  }
  const power = event.target.closest("[data-cloud-power]");
  if (power) { powerNode(nodeID, power.dataset.cloudPower, card); return; }
  if (event.target.closest("[data-cloud-snapshot]")) { takeSnapshot(nodeID, card); return; }
  const remove2 = event.target.closest("[data-snapshot-delete]");
  if (remove2) { deleteSnapshot(nodeID, remove2.closest("[data-snapshot]").dataset.snapshot, card); return; }
  const restore = event.target.closest("[data-snapshot-restore]");
  if (restore) { restoreSnapshot(nodeID, restore.closest("[data-snapshot]").dataset.snapshot, card); }
});

function nameOf(card) {
  return qs("[data-node-name]", card)?.textContent || "this machine";
}

function cardError(card, message) {
  const note = qs("[data-cloud-error]", card);
  if (note) note.textContent = message;
}

async function powerNode(nodeID, action, card) {
  const name = nameOf(card);
  const words = {
    start: { title: `Start ${name}?`, body: "The instance powers on. It answers again once it has booted." },
    reboot: { title: `Reboot ${name}?`, body: "This is a power-cycle at the provider, not a reboot over SSH. Anything running on the machine stops now." },
    halt: { title: `Halt ${name}?`, body: "The instance stops. It keeps its disk, its address and its bill — and answers nothing until it is started again." },
  }[action];
  if (!await ask({ ...words, confirm: `Yes, ${action}` })) return;
  try {
    await request("/api/cluster/provider/power", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ node_id: nodeID, action }),
    });
    cardError(card, "");
    // The provider takes a moment to agree that anything happened, so the
    // strip is refetched rather than guessed at.
    setTimeout(() => loadFacts(nodeID, true).catch(() => {}), 1500);
  } catch (failure) {
    cardError(card, failure.message);
  }
}

async function takeSnapshot(nodeID, card) {
  const name = nameOf(card);
  if (!await ask({
    title: `Snapshot ${name}?`,
    body: "The provider images the machine's disk. It takes minutes for a real machine, storage is billed while it exists, and the machine keeps running throughout.",
    confirm: "Take the snapshot",
  })) return;
  try {
    await request("/api/cluster/provider/snapshots", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ node_id: nodeID, description: `${name} — guard` }),
    });
    cardError(card, "");
    openSnapshots.add(nodeID);
    await loadSnapshots(nodeID, true);
  } catch (failure) {
    cardError(card, failure.message);
  }
}

async function deleteSnapshot(nodeID, snapshotID, card) {
  if (!await ask({
    title: "Delete this snapshot?",
    body: "The image is gone at the provider. Anything restored from it has to come from a different one.",
    confirm: "Delete it",
  })) return;
  try {
    await request(`/api/cluster/provider/snapshots?node=${nodeID}&snapshot=${encodeURIComponent(snapshotID)}`,
      { method: "DELETE", headers: adminHeaders() });
    cardError(card, "");
    await loadSnapshots(nodeID, true);
  } catch (failure) {
    cardError(card, failure.message);
  }
}

async function restoreSnapshot(nodeID, snapshotID, card) {
  const name = nameOf(card);
  // The machine's name, typed. The same confirmation locking and deleting
  // take, and for the same reason: a dialog with a yes button is a dialog
  // people click without reading, and this one overwrites a disk.
  if (!await ask({
    title: `Restore ${name} from a snapshot?`,
    body: "Everything on the machine that is not in the image is gone. The instance reboots into the restored disk, and the only undo is a snapshot taken before this one.",
    confirm: "Restore it",
    phrase: name,
  })) return;
  try {
    await request("/api/cluster/provider/restore", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ node_id: nodeID, snapshot_id: snapshotID }),
    });
    cardError(card, "");
    setTimeout(() => loadFacts(nodeID, true).catch(() => {}), 1500);
  } catch (failure) {
    cardError(card, failure.message);
  }
}

async function saveLink(section, clear) {
  const nodeID = Number(section.closest("[data-node-id]")?.dataset.nodeId || section.closest("[data-node-row]")?.dataset.nodeId);
  const note = qs("[data-cloud-note]", section);
  const accountID = Number(qs("[data-cloud-account]", section).value);
  const instanceID = clear ? "" : qs("[data-cloud-instance]", section).value;
  if (!clear && !instanceID) { note.textContent = "Choose an instance, or press Unlink."; return; }
  note.textContent = clear ? "Unlinking…" : "Checking the instance against the provider…";
  try {
    await request("/api/cluster/provider/link", {
      method: "PUT", headers: adminHeaders(),
      body: JSON.stringify({ node_id: nodeID, account_id: accountID, instance_id: instanceID, provider: "vultr" }),
    });
    note.textContent = clear ? "Unlinked." : "Linked.";
    facts.delete(nodeID);
    snapshots.delete(nodeID);
    instances.delete(accountID);
    redraw();
  } catch (failure) {
    note.textContent = failure.message;
  }
}

async function importInstance(row) {
  if (!row) return;
  importStatus("Declaring the machine…");
  try {
    const node = await request("/api/cluster/provider/import", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ account_id: Number(row.dataset.account), instance_id: row.dataset.importRow }),
    });
    importStatus(`${node.name} is now watched — paused until you give it a health path.`);
    instances.delete(Number(row.dataset.account));
    redraw();
    await refreshImport(true);
  } catch (failure) {
    importStatus(failure.message);
  }
}

async function removeAccount(id, name) {
  if (!await ask({
    title: `Remove ${name}?`,
    body: "The stored key is forgotten. Nothing at the provider is touched — but every machine linked into this account is unlinked.",
    confirm: "Remove the account",
  })) return;
  try {
    await request(`/api/cloud/accounts/${id}`, { method: "DELETE", headers: adminHeaders() });
    accounts = null;
    await refreshAccountRows();
  } catch (failure) {
    const body = qs("[data-cloud-accounts]");
    if (body) body.replaceChildren(row(4, failure.message));
  }
}
