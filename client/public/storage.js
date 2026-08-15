// Object storage: what the stored account keys own, read live.
//
// The same shape as the registries page — nothing is cached on the server,
// nothing is stored in guard, and every request leaves from the server. The
// one thing that is different is the credentials.
//
// The provider hands back an S3 access key and secret with every read of a
// subscription. Guard's listing carries neither: each card says a pair exists
// and draws dots. Pressing Reveal goes to an endpoint whose whole job is that
// one thing, and which writes a line saying it happened. That is deliberate
// rather than paranoid — copying those two strings into an application is the
// reason to have this page at all, and a page that only ever showed dots would
// just mean the provider's console stays open in another tab.
import { adminHeaders, el, muted, qs, qsa, request, svg } from "./core.js";
import { ensure } from "./store.js";
import { ask } from "./cluster.js";
import { can, cloudAccounts, cloudProviders } from "./cloud.js";

const MASK = "••••••••••••••••";

let rows = [];
// The last answer, kept whole. A create needs the account row it belongs to —
// its name and its capabilities — and that is only in the answer, not in the
// flattened rows the cards are drawn from.
let answered = [];
// What each account's provider offers a create form: where storage may live,
// what it costs there, and which storage classes exist. Per account, because
// the answer is the provider's and the account is which provider it is.
let options = new Map(); // accountID -> {regions, tiers, classes}
// The revealed pairs, held in this tab only: never written to storage, never
// re-fetched on a redraw, gone when the page is.
const revealed = new Map(); // storageID -> {access, secret}

// Which rows are opened out. Kept here rather than in the DOM because a reveal
// redraws every row, and a fold that closed itself when the keys arrived would
// hide the thing that was just asked for.
const opened = new Set(); // storageID

const states = {
  active: "cn-badge-variant-default",
  pending: "cn-badge-variant-secondary",
};

export async function refreshStorage() {
  const host = qs("[data-storage-rows]");
  if (!host) return;
  // A refresh behind an open browser would repaint the grid underneath
  // somebody reading a folder. The browser refetches itself on its own.
  if (browsing()) return;
  try {
    // Through the store: behind this is somebody's provider API, so coming
    // back to this page must not mean waiting on it again. What was there
    // last time is drawn in the navigation's own frame, the provider is asked
    // in the background, and the rows are rebuilt only if the answer moved.
    await ensure("cloud.storage", () => request("/api/cloud/storage"), (answer, stale) => {
      answered = answer || [];
      rows = [];
      for (const account of answered) {
        if (account.error) status(`${account.account.name}: ${account.error}`);
        // The capabilities come down with the row: what a card may offer is
        // the provider's answer, not this file's opinion.
        for (const storage of account.storage || []) {
          rows.push({ account: account.account, capabilities: account.capabilities || {}, storage });
        }
      }
      render();
      if (stale) status("from your last visit — asking the provider…");
      else if (!answered.some((account) => account.error)) status("");
    });
  } catch (failure) {
    status(failure.message);
  }
}

function status(message) {
  const note = qs("[data-storage-status]");
  if (!note) return;
  note.textContent = message || "";
  note.hidden = !message;
}

function render() {
  const host = qs("[data-storage-rows]");
  const template = qs("[data-storage-card-template]");
  if (!host || !template) return;
  // Grouped by the account key the buckets came from, the way the cluster page
  // groups machines: two accounts on the same provider are two bills and two
  // logins, and a flat list makes somebody read every endpoint to tell which
  // is which.
  const sections = [];
  for (const account of answered) {
    const mine = rows.filter((row) => row.account.id === account.account.id);
    sections.push(accountHeading(account.account, mine.length, account.error));
    if (!mine.length) continue;
    const list = el("div", "divide-y divide-border overflow-hidden rounded-xl border border-border");
    list.append(...mine.map((row) => card(template, row)));
    sections.push(list);
  }
  host.replaceChildren(...sections);
  qs("[data-storage-empty]").hidden = rows.length > 0;
  const summary = qs("[data-storage-summary]");
  if (summary) {
    summary.textContent = rows.length
      ? `${rows.length} bucket${rows.length === 1 ? "" : "s"} across the stored accounts`
      : "Nothing on the stored accounts";
  }
}

function bytes(value) {
  if (!value) return "0 B";
  const units = ["B", "kB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${Number(size.toFixed(size >= 100 || unit === 0 ? 0 : 1))} ${units[unit]}`;
}

// One line per account: whose key these buckets came from, and how many it
// answered for. The same heading the cluster page puts over a group.
function accountHeading(account, total, failed) {
  const row = el("div", "flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-border pb-2");
  row.append(el("h2", "text-sm font-semibold tracking-tight", account.name || "Account"));
  row.append(el("span", `text-xs ${muted}`, account.provider || ""));
  if (failed) row.append(el("span", "text-xs text-destructive", failed));
  else row.append(el("span", `text-xs ${muted}`, `${total} bucket${total === 1 ? "" : "s"}`));
  return row;
}

function card(template, { account, capabilities, storage }) {
  const item = template.content.firstElementChild.cloneNode(true);
  item.dataset.storageId = storage.id;
  item.dataset.account = String(account.id);
  qs("[data-storage-name]", item).textContent = storage.label || storage.id;
  qs("[data-storage-region]", item).textContent = storage.region || "—";
  qs("[data-storage-since]", item).textContent = storage.created
    ? `since ${new Date(storage.created).toLocaleDateString()}`
    : "";

  // The badge says whatever the provider actually reports. Vultr gives a
  // provisioning state; R2 has none and gives a storage class instead. With
  // neither, no badge — "unknown" is a word for something that has a state
  // guard failed to read, which is not this.
  const state = qs("[data-storage-state]", item);
  const label = storage.status || storage.class || "";
  state.hidden = !label;
  state.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${states[storage.status] || states.pending}`;
  state.textContent = label;

  qs("[data-storage-host]", item).textContent = storage.s3_hostname || "—";

  // Usage, where the provider reports it. Cloudflare does; Vultr does not, and
  // a dash with "not reported" under it is more honest than a zero — the
  // column stays either way, because a column that disappears on some rows is
  // a list that no longer lines up.
  const reported = storage.used_bytes || storage.objects;
  qs("[data-storage-usage]", item).textContent = reported ? bytes(storage.used_bytes) : "—";
  qs("[data-storage-objects]", item).textContent = reported
    ? `${storage.objects || 0} object${storage.objects === 1 ? "" : "s"}`
    : "not reported";

  // A provider that cannot hand out an S3 pair gets no credentials block and
  // no buttons for one. Drawing dots over keys that will never arrive, or a
  // Reveal that always fails, would be the page lying about what it can do.
  const keys = !!capabilities.storage_keys;
  qs("[data-storage-credentials]", item).hidden = !keys;
  qs("[data-storage-reveal]", item).hidden = !keys;
  qs("[data-storage-regenerate]", item).hidden = !keys;
  qs("[data-storage-rename]", item).hidden = !capabilities.storage_rename;
  // Browse needs both: a provider that can look inside, and — where the
  // credential for that is one guard stores — an account that has one.
  const openable = capabilities.storage_objects && (capabilities.storage_keys || account.has_s3_keys);
  const browseButton = qs("[data-storage-browse]", item);
  browseButton.hidden = !openable;
  browseButton.title = openable
    ? "Look inside — read-only"
    : "This account has no S3 key stored, so its objects cannot be read";
  const note = qs("[data-storage-keys-note]", item);
  note.hidden = keys;
  if (!keys) {
    note.textContent = "S3 credentials for this provider are minted on its own token screen — guard cannot issue or rotate them.";
  }

  if (keys) {
    const pair = revealed.get(storage.id);
    const access = qs("[data-storage-access]", item);
    const secret = qs("[data-storage-secret]", item);
    access.textContent = pair ? pair.access : storage.has_keys ? MASK : "no keys yet";
    secret.textContent = pair ? pair.secret : storage.has_keys ? MASK : "no keys yet";
    // Dots are not a value to copy. The button is only live once there is
    // something real behind it.
    for (const control of qsa("[data-storage-copy]", item)) control.disabled = !pair;
    qs("[data-storage-reveal]", item).disabled = !storage.has_keys;
  }

  // The head says whether there is a pair at all; the pair itself is in the
  // fold, where somebody had to ask for it.
  qs("[data-storage-keys-state]", item).textContent = !keys
    ? "provider's own"
    : revealed.has(storage.id)
    ? "revealed"
    : storage.has_keys
    ? "stored"
    : "none yet";

  showBody(item, opened.has(storage.id));
  return item;
}

// Open or shut one row, chevron and all — the same fold the cluster list has.
function showBody(item, open) {
  const body = qs("[data-card-body]", item);
  if (body) body.hidden = !open;
  const toggle = qs("[data-card-toggle]", item);
  if (toggle) {
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
    const chevron = qs("svg", toggle);
    if (chevron) chevron.style.transform = open ? "rotate(180deg)" : "";
  }
}

// ---- what is inside a bucket ----
//
// A drill on the same page, like the registries one: the storage grid stays in
// the DOM and is hidden, so closing the browser is instant and refetches
// nothing. Where the reader is — which storage, which bucket, which prefix —
// lives here rather than in the URL, because a prefix can be anything and a
// page that put object keys in the address bar would put them in every log
// between here and the browser.
const browse = { account: 0, storage: "", name: "", container: "", prefix: "", cursor: "", rows: [] };

function browsing() { return !!browse.storage; }

function browseStatus(message) {
  const note = qs("[data-browse-status]");
  if (!note) return;
  note.textContent = message || "";
  note.hidden = !message;
}

async function openBrowser(target, item) {
  browse.account = target.account_id;
  browse.storage = target.storage_id;
  browse.name = qs("[data-storage-name]", item).textContent;
  browse.container = "";
  browse.prefix = "";
  await loadFolder(false);
}

function closeBrowser() {
  browse.storage = "";
  browse.rows = [];
  qs("[data-storage-browser]").hidden = true;
  for (const node of qsa("[data-storage-rows],[data-storage-empty]")) node.hidden = false;
  qs("[data-storage-rows]").classList.remove("hidden");
  render();
}

// loadFolder reads one level. `more` keeps what is showing and appends the
// next page — a listing is paginated by the provider, and a table that
// replaced itself on "load more" would lose the reader's place.
async function loadFolder(more) {
  const section = qs("[data-storage-browser]");
  if (!section) return;
  section.hidden = false;
  qs("[data-storage-rows]").hidden = true;
  qs("[data-storage-empty]").hidden = true;
  qs("[data-storage-form]").hidden = true;
  browseStatus("");
  if (!more) { browse.rows = []; browse.cursor = ""; }
  renderBrowser(true);
  const query = new URLSearchParams({ account: String(browse.account), storage: browse.storage });
  if (browse.container) query.set("container", browse.container);
  if (browse.prefix) query.set("prefix", browse.prefix);
  if (more && browse.cursor) query.set("cursor", browse.cursor);
  try {
    const answer = await request(`/api/cloud/storage/objects?${query}`, { headers: adminHeaders() });
    const page = [
      // Buckets first where there are any, then folders, then files: it is
      // the order they nest in, and a folder among files reads as a file.
      ...(answer.containers || []).map((entry) => ({ kind: "container", name: entry.name, container: entry.name })),
      ...(answer.folders || []).map((prefix) => ({ kind: "folder", name: folderName(prefix), prefix })),
      ...(answer.objects || []).map((object) => ({ kind: "object", ...object })),
    ];
    browse.rows = more ? [...browse.rows, ...page] : page;
    browse.cursor = answer.cursor || "";
    renderBrowser(false);
  } catch (failure) {
    browse.rows = more ? browse.rows : [];
    renderBrowser(false);
    browseStatus(failure.message);
  }
}

// folderName is the last segment of a prefix: "user/019f/" shows as "019f".
function folderName(prefix) {
  const parts = prefix.replace(/\/$/, "").split("/");
  return parts[parts.length - 1] || prefix;
}

// The three shapes a row can be. Drawn here rather than cloned from a
// template because they are four paths between them and a row is built in
// JavaScript anyway — and because "is this a folder" is the question the eye
// asks first, before it reads a single name.
const icons = {
  up: ["M9 14 4 9l5-5", "M20 20v-7a4 4 0 0 0-4-4H4"],
  folder: ["M4 20a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2z"],
  file: ["M14 3v4a1 1 0 0 0 1 1h4", "M17 21H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h7l5 5v11a2 2 0 0 1-2 2z"],
};

// label is an icon beside a word, on one line.
//
// It exists because the button cannot be the flex container. The shadcn button
// rules are nested under .style-nova, so `.cn-btn { display: inline-block }`
// out-specifies a plain `inline-flex` utility on the same element — and
// Tailwind's preflight makes an svg `display: block`, which then takes a line
// of its own. A span inside the button carries no cn-* class, so the utility
// wins there and the two sit together.
function label(kind, words, className = "") {
  const wrap = el("span", "inline-flex items-center gap-2 text-muted-foreground");
  wrap.append(icon(kind), el("span", className, words));
  return wrap;
}

function icon(kind) {
  const node = svg("svg", {
    class: "size-4 shrink-0", viewBox: "0 0 24 24", fill: "none", stroke: "currentColor",
    "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true",
  });
  for (const d of icons[kind] || []) node.append(svg("path", { d }));
  return node;
}

function renderBrowser(loading) {
  const body = qs("[data-browse-rows]");
  if (!body) return;
  qs("[data-browse-title]").textContent = [browse.name, browse.container].filter(Boolean).join(" / ");
  const files = browse.rows.filter((row) => row.kind === "object").length;
  const folders = browse.rows.length - files;
  qs("[data-browse-count]").textContent = loading
    ? "reading…"
    : `${folders ? `${folders} folder${folders === 1 ? "" : "s"}` : ""}${folders && files ? " · " : ""}${files ? `${files} object${files === 1 ? "" : "s"}` : ""}` || "empty";
  renderPath();
  const more = qs("[data-browse-more]");
  if (more) more.classList.toggle("hidden", !browse.cursor);

  if (loading && !browse.rows.length) {
    body.replaceChildren(browseRow(4, "Reading the storage…"));
    return;
  }
  // The way back up is a row rather than only a breadcrumb: going up a level
  // is the most common thing to do in a folder, and it belongs where the
  // hands already are.
  const up = parentRow();
  if (!browse.rows.length) {
    body.replaceChildren(...(up ? [up] : []), browseRow(4, "Nothing here."));
    return;
  }
  body.replaceChildren(...(up ? [up] : []), ...browse.rows.map(objectRow));
}

// renderPath draws the breadcrumb: the storage, the bucket if there is one,
// and every prefix above this one, each of them a way back.
function renderPath() {
  const host = qs("[data-browse-path]");
  if (!host) return;
  const crumbs = [];
  const crumb = (label, container, prefix, last) => {
    const node = el("button", `cn-btn cn-btn-variant-ghost cn-btn-size-sm ${last ? "text-foreground" : "text-muted-foreground"}`, label);
    node.type = "button";
    node.dataset.browseCrumb = prefix;
    node.dataset.browseCrumbContainer = container;
    crumbs.push(node);
  };
  const segments = browse.prefix ? browse.prefix.replace(/\/$/, "").split("/") : [];
  crumb(browse.name, "", "", !browse.container && !segments.length);
  if (browse.container) {
    crumb(browse.container, browse.container, "", !segments.length);
  }
  let walked = "";
  segments.forEach((segment, index) => {
    walked += segment + "/";
    crumb(segment, browse.container, walked, index === segments.length - 1);
  });
  const separated = [];
  crumbs.forEach((node, index) => {
    if (index) separated.push(el("span", "text-muted-foreground/50", "/"));
    separated.push(node);
  });
  host.replaceChildren(...separated);
}

// parentRow is "../" — one level up, which is the enclosing prefix, or the
// bucket list when a prefix has run out. Nothing at the top of a storage,
// where the way out is the ← Storage button.
function parentRow() {
  const segments = browse.prefix ? browse.prefix.replace(/\/$/, "").split("/") : [];
  if (!segments.length && !browse.container) return null;
  const line = document.createElement("tr");
  line.className = "cn-table-row";
  const name = el("td", `${cellBase} font-medium`);
  const open = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm px-0 font-medium text-foreground");
  open.type = "button";
  open.dataset.browseUp = "1";
  open.append(label("up", "../", "font-mono"));
  name.append(open);
  const blank = () => el("td", `${cellBase} ${muted}`, "—");
  line.append(name, blank(), blank(), el("td", `${cellBase} text-right`));
  return line;
}

// up walks one level out: the enclosing prefix, or back to the bucket list
// when the prefix is gone and the storage holds more than one bucket.
function up() {
  const segments = browse.prefix ? browse.prefix.replace(/\/$/, "").split("/") : [];
  if (segments.length > 1) {
    browse.prefix = segments.slice(0, -1).join("/") + "/";
  } else if (segments.length === 1) {
    browse.prefix = "";
  } else {
    browse.container = "";
  }
  loadFolder(false);
}

function objectRow(row) {
  const line = document.createElement("tr");
  line.className = "cn-table-row";
  const name = el("td", `${cellBase} font-medium`);
  if (row.kind === "object") {
    name.append(label("file", row.name, "truncate text-foreground"));
  } else {
    const open = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm px-0 font-medium text-foreground");
    open.type = "button";
    open.dataset.browseOpen = row.kind;
    open.dataset.browseName = row.kind === "container" ? row.container : row.prefix;
    open.append(label("folder", `${row.name}/`, "truncate"));
    name.append(open);
  }
  const size = el("td", `${cellBase} tabular-nums ${muted}`, row.kind === "object" ? bytes(row.size) : "—");
  const when = el("td", `${cellBase} ${muted}`,
    row.modified ? new Date(row.modified).toLocaleString() : "—");
  const action = el("td", `${cellBase} text-right`);
  if (row.kind === "object") {
    const get = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm text-muted-foreground", "Download");
    get.type = "button";
    get.dataset.browseDownload = row.key;
    action.append(get);
  }
  line.append(name, size, when, action);
  return line;
}

const cellBase = "cn-table-cell cn-table-cell-aria";

function browseRow(columns, message) {
  const line = document.createElement("tr");
  line.className = "cn-table-row";
  const td = el("td", `${cellBase} ${muted}`, message);
  td.colSpan = columns;
  line.append(td);
  return line;
}

// download asks for a signed link and follows it. The link is minted on the
// press rather than sitting in the markup, and it expires — a page full of
// live download URLs would be a page that leaks by being open.
async function download(key) {
  try {
    const answer = await request("/api/cloud/storage/link", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({
        account_id: browse.account, storage_id: browse.storage,
        container: browse.container, key,
      }),
    });
    window.open(answer.url, "_blank", "noopener");
  } catch (failure) {
    browseStatus(failure.message);
  }
}

// ---- the create form ----

async function openForm() {
  const form = qs("[data-storage-form]");
  if (!form) return;
  form.hidden = false;
  const select = qs("[data-storage-account]", form);
  // Only the accounts that can hold object storage at all. An account at a
  // provider with none is not an empty dropdown here; it is not on the page.
  const [all] = await Promise.all([cloudAccounts(), cloudProviders()]);
  const list = all.filter((account) => can(account, "storage"));
  if (!list.length) {
    // Every select says why it is empty, and Create is refused. A form with
    // three blank dropdowns and a live button is a form that fails on press
    // and leaves the reader guessing which of the three was the problem.
    placeholder(select, "No accounts with storage");
    placeholder(qs("[data-storage-region]", form), "No account to ask");
    placeholder(qs("[data-storage-tier]", form), "No account to ask");
    creatable(false);
    formStatus("Add a cloud account key under Settings → Cloud accounts first.");
    return;
  }
  select.replaceChildren(...list.map((account) => new Option(account.name, String(account.id))));
  await fillRegions();
}

// placeholder leaves a select saying something rather than nothing. An empty
// dropdown reads as a page that failed to load; a dropdown with one line in it
// reads as an answer.
function placeholder(select, message) {
  if (!select) return;
  const option = new Option(message, "");
  option.disabled = true;
  option.selected = true;
  select.replaceChildren(option);
}

function creatable(allowed) {
  const button = qs("[data-storage-create]");
  if (button) button.disabled = !allowed;
}

async function fillRegions() {
  const accountID = Number(qs("[data-storage-account]").value);
  const select = qs("[data-storage-region]");
  if (!accountID) {
    placeholder(select, "Choose an account first");
    placeholder(qs("[data-storage-tier]"), "Choose a region first");
    creatable(false);
    return;
  }
  placeholder(select, "Asking the provider…");
  formStatus("Asking the provider where storage can live…");
  try {
    if (!options.has(accountID)) {
      options.set(accountID, await request(`/api/cloud/storage/options?account=${accountID}`, { headers: adminHeaders() }));
    }
    const answer = options.get(accountID) || {};
    const list = answer.regions || [];
    if (!list.length) {
      // The key answered and named nothing. That is the provider's answer,
      // not a bug here, and saying so is the difference between "try another
      // account" and "reload the page".
      placeholder(select, "This account offers no regions");
      placeholder(qs("[data-storage-tier]"), "No region to price");
      creatable(false);
      formStatus("The provider listed no object storage regions for this account.");
      return;
    }
    select.replaceChildren(...list.map((region) => {
      // A region that has stopped taking new subscriptions is listed and
      // disabled rather than hidden: "where did Sydney go" is a worse
      // question than "why is Sydney greyed out".
      const label = region.hostname ? `${region.name} — ${region.hostname}` : region.name;
      const option = new Option(label, region.id);
      option.disabled = !region.available;
      return option;
    }));
    // A select whose only options are disabled has no value, so the first one
    // that can actually be ordered is the one to land on.
    const first = [...select.options].find((option) => !option.disabled);
    if (first) first.selected = true;
    creatable(!!first);
    formStatus("");
    fillTiers();
    fillClasses();
  } catch (failure) {
    // The key is refused, or the provider is down. Either way the selects say
    // it rather than sitting blank under an error line nobody connects to them.
    placeholder(select, "Could not read the regions");
    placeholder(qs("[data-storage-tier]"), "Could not read the tiers");
    creatable(false);
    formStatus(failure.message);
  }
}

// fillTiers prices the chosen region. A provider with one price everywhere —
// R2 — has no tiers at all, and the field goes away rather than offering a
// choice of one.
function fillTiers() {
  const accountID = Number(qs("[data-storage-account]").value);
  const region = qs("[data-storage-region]").value;
  const select = qs("[data-storage-tier]");
  const field = qs("[data-storage-tier-field]");
  const answer = options.get(accountID) || {};
  const all = answer.tiers || [];
  field.hidden = !all.length;
  if (!all.length) {
    select.replaceChildren(new Option("Default", ""));
    return;
  }
  // A tier may be scoped to one region or offered everywhere.
  const tiers = all.filter((tier) => !tier.region || tier.region === region);
  if (!tiers.length) {
    // Accounts older than tiers get one option and a create call without a
    // tier, which is what the provider's own console does. It is selectable,
    // unlike the placeholders above, because it is a real answer: this region
    // has one price and the provider will pick it.
    select.replaceChildren(new Option("Default", ""));
    return;
  }
  select.replaceChildren(...tiers.map((tier) => {
    const label = [tier.name || `Tier ${tier.id}`, tier.storage_gb ? `${tier.storage_gb} GB` : "", tier.price ? `$${tier.price}/mo` : ""]
      .filter(Boolean).join(" · ");
    return new Option(label, String(tier.id));
  }));
}

// fillClasses offers the storage classes a provider charges by, where it has
// more than one. Which class a bucket should be is a decision about the data
// rather than about the bucket, so both are offered and neither is chosen.
function fillClasses() {
  const accountID = Number(qs("[data-storage-account]").value);
  const answer = options.get(accountID) || {};
  const classes = answer.classes || [];
  const field = qs("[data-storage-class-field]");
  const select = qs("[data-storage-class]");
  field.hidden = classes.length < 2;
  select.replaceChildren(...classes.map((name) => new Option(name, name)));
}

function formStatus(message) {
  const note = qs("[data-storage-form-status]");
  if (note) note.textContent = message || "";
}

async function create() {
  const accountID = Number(qs("[data-storage-account]").value);
  const region = qs("[data-storage-region]").value;
  const tier = qs("[data-storage-tier]").value;
  const storageClass = qs("[data-storage-class-field]").hidden ? "" : qs("[data-storage-class]").value;
  const label = qs("[data-storage-label]").value.trim();
  if (!label) { formStatus("A label is required."); return; }
  // A region can legitimately be empty — R2's "nearest to first write" is a
  // real choice — so only the account is required here.
  if (!accountID) { formStatus("Choose an account first."); return; }
  if (!await ask({
    title: `Create ${label}?`,
    body: "The provider bills this from the moment it is created, until it is deleted.",
    confirm: "Create it",
  })) return;
  formStatus("Ordering…");
  try {
    const made = await request("/api/cloud/storage", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ account_id: accountID, region, tier, class: storageClass, label }),
    });
    formStatus("");
    qs("[data-storage-form]").hidden = true;
    qs("[data-storage-label]").value = "";
    // Show what the provider just handed back rather than asking for the list
    // again. R2's listing is a second or two behind its own create, so a
    // refetch here would answer "nothing changed" about a bucket that exists —
    // which is the one moment somebody is certain it should be there. Refresh
    // reconciles whenever it is pressed.
    const owner = answered.find((entry) => entry.account.id === accountID);
    if (owner) {
      owner.storage = [...(owner.storage || []), made];
      rows.push({ account: owner.account, capabilities: owner.capabilities || {}, storage: made });
      render();
    } else {
      await refreshStorage();
    }
  } catch (failure) {
    formStatus(failure.message);
  }
}

// ---- what the presses do ----

document.addEventListener("change", (event) => {
  if (event.target.closest("[data-storage-account]")) { fillRegions(); return; }
  if (event.target.closest("[data-storage-region]")) { fillTiers(); fillClasses(); }
});

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-storage-browse-close]")) { closeBrowser(); return; }
  if (event.target.closest("[data-browse-refresh]")) { loadFolder(false); return; }
  if (event.target.closest("[data-browse-more]")) { loadFolder(true); return; }

  if (event.target.closest("[data-browse-up]")) { up(); return; }

  const open = event.target.closest("[data-browse-open]");
  if (open) {
    if (open.dataset.browseOpen === "container") {
      browse.container = open.dataset.browseName;
      browse.prefix = "";
    } else {
      browse.prefix = open.dataset.browseName;
    }
    loadFolder(false);
    return;
  }
  const crumb = event.target.closest("[data-browse-crumb]");
  if (crumb) {
    browse.container = crumb.dataset.browseCrumbContainer || "";
    browse.prefix = crumb.dataset.browseCrumb || "";
    loadFolder(false);
    return;
  }
  const get = event.target.closest("[data-browse-download]");
  if (get) { download(get.dataset.browseDownload); return; }

  if (event.target.closest("[data-storage-refresh]")) { refreshStorage(); return; }
  if (event.target.closest("[data-storage-new]")) { openForm(); return; }
  if (event.target.closest("[data-storage-cancel]")) { qs("[data-storage-form]").hidden = true; return; }
  if (event.target.closest("[data-storage-create]")) { create(); return; }

  const item = event.target.closest("[data-storage-id]");
  if (!item) return;
  const target = { account_id: Number(item.dataset.account), storage_id: item.dataset.storageId };

  // Opening one row. The whole head line is the target, chevron included — a
  // 28-pixel arrow is a small thing to ask somebody to hit — but not the
  // controls inside it, or opening a bucket and pressing a button on it would
  // be the same gesture.
  const head = event.target.closest("[data-card-head]");
  if (head && !event.target.closest("a, input, select, label, button:not([data-card-toggle])")) {
    const open = opened.delete(target.storage_id) ? false : (opened.add(target.storage_id), true);
    showBody(item, open);
    return;
  }

  if (event.target.closest("[data-storage-browse]")) { openBrowser(target, item); return; }
  if (event.target.closest("[data-storage-reveal]")) { reveal(target, item); return; }
  const copy = event.target.closest("[data-storage-copy]");
  if (copy) {
    const pair = revealed.get(target.storage_id);
    if (pair) navigator.clipboard?.writeText(copy.dataset.storageCopy === "secret" ? pair.secret : pair.access);
    return;
  }
  if (event.target.closest("[data-storage-rename]")) { rename(target, item); return; }
  if (event.target.closest("[data-storage-regenerate]")) { regenerate(target, item); return; }
  if (event.target.closest("[data-storage-delete]")) remove(target, item);
});

async function reveal(target, item) {
  try {
    const pair = await request("/api/cloud/storage/keys", {
      method: "POST", headers: adminHeaders(), body: JSON.stringify(target),
    });
    revealed.set(target.storage_id, { access: pair.s3_access_key, secret: pair.s3_secret_key });
    render();
  } catch (failure) {
    status(failure.message);
  }
}

async function rename(target, item) {
  const current = qs("[data-storage-name]", item).textContent;
  const label = prompt("New label", current);
  if (label === null || label.trim() === "" || label === current) return;
  try {
    await request("/api/cloud/storage/label", {
      method: "PUT", headers: adminHeaders(),
      body: JSON.stringify({ ...target, label: label.trim() }),
    });
    await refreshStorage();
  } catch (failure) {
    status(failure.message);
  }
}

async function regenerate(target, item) {
  const name = qs("[data-storage-name]", item).textContent;
  if (!await ask({
    title: `Rotate the keys for ${name}?`,
    body: "A new pair is issued immediately and the old one stops working — every deploy, backup job and uploader still holding the old secret fails from that moment.",
    confirm: "Rotate them",
    phrase: name,
  })) return;
  try {
    const pair = await request("/api/cloud/storage/regenerate", {
      method: "POST", headers: adminHeaders(), body: JSON.stringify(target),
    });
    revealed.set(target.storage_id, { access: pair.s3_access_key, secret: pair.s3_secret_key });
    await refreshStorage();
  } catch (failure) {
    status(failure.message);
  }
}

async function remove(target, item) {
  const name = qs("[data-storage-name]", item).textContent;
  if (!await ask({
    title: `Delete ${name}?`,
    body: "The subscription and everything in it are destroyed at the provider. Anything still pointed at that endpoint starts failing immediately, and there is no undo.",
    confirm: "Delete it",
    phrase: name,
  })) return;
  try {
    await request(`/api/cloud/storage?account=${target.account_id}&storage=${encodeURIComponent(target.storage_id)}`,
      { method: "DELETE", headers: adminHeaders() });
    revealed.delete(target.storage_id);
    // Same reason as create, in reverse: the provider has accepted the delete,
    // and its listing may still name the thing for a moment.
    rows = rows.filter((row) => row.storage.id !== target.storage_id);
    for (const entry of answered) {
      entry.storage = (entry.storage || []).filter((storage) => storage.id !== target.storage_id);
    }
    render();
  } catch (failure) {
    status(failure.message);
  }
}
