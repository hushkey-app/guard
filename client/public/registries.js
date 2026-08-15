// The registries: what the stored provider keys unlock, read live.
//
// One surface: a three-level drill — registries, one registry's
// repositories, one repository's tags — kept on one page so "back" is
// instant. The account keys behind it are stored under Settings → Cloud
// accounts, because the same key also answers for machines and buckets. Everything destructive goes through the same confirm dialog the
// cluster uses, and every request leaves from the server: this file only
// ever sees names, counts and digests.
//
// The tables update in place. A refetch keeps the rows that are showing
// until the new ones arrive — a table that blinks empty for a second reads
// as "everything was deleted", which is a terrible thing for a page with
// delete buttons to say by accident.
import { adminHeaders, el, muted, number, qs, qsa, relativeTime, request, text } from "./core.js";
import { ensure, get as stored } from "./store.js";
import { ask } from "./cluster.js";
import { can, cloudAccounts, cloudProviders } from "./cloud.js";

// The overview is the only thing fetched without being asked, and even then
// not often: it is a call out to the provider, not a read of guard's own
// database, so the live tick must not turn into a request every three
// seconds against somebody's API rate limit.
const overviewTTL = 60_000;
let overview = [];
let overviewAt = 0;
let overviewRequest = null;

// Where the reader is in the drill. Survives the live tick on purpose:
// a redraw must not pull someone out of a repository they are reading.
const drill = { account: 0, registry: null, registryName: "", repo: null, image: "" };

// What each account's provider can be asked for, from the overview answer.
// Whether a card gets a delete button, and whether the page gets a create
// one, is the provider's answer rather than this file's opinion.
const capabilities = new Map(); // accountID -> capabilities

// What a registry can be created as, per account: the provider's regions and
// its price list, read only when the form is opened.
const registryOptions = new Map(); // accountID -> {regions, plans}

// What the last fetch answered, kept so the tables can re-render — for the
// tag filter, for a selection change — without asking the registry again.
let repoList = [];
let tagList = [];
let tagFilter = "";

// What is ticked. Repos carry their image token along because that, not
// the name, is what the delete endpoint takes.
const selectedRepos = new Map(); // name -> image
const selectedTags = new Set();

const cellBase = "cn-table-cell cn-table-cell-aria";

export async function refreshRegistries(force = false) {
  if (qs("[data-registry-overview]")) await refreshExplorer(force);
}

// The overview lives in the store rather than in a module-private cache with a
// TTL. Same throttle — behind this is somebody's registry API, not guard's
// SQLite — but the value now survives navigation, so coming back to the page
// draws it before the provider has said anything.
// The overview lives in the store rather than in a module-private cache with a
// TTL. Same throttle — behind this is somebody's registry API, not guard's
// SQLite — but the value now survives navigation, and the table is drawn from
// what is known *inside* the callback rather than after the await. That is the
// whole difference: awaiting first means the page waits on somebody else's API
// before it draws rows it already had.
async function fetchOverview(force, draw) {
  const known = stored("registries.overview");
  if (!force && known && Date.now() - overviewAt < overviewTTL) {
    overview = known;
    draw();
    return overview;
  }
  return ensure("registries.overview", () => request("/api/registries"), (rows, stale) => {
    overview = rows || [];
    if (!stale) overviewAt = Date.now();
    draw();
  });
}

async function refreshExplorer(force) {
  // Refreshing behind an open drill would repaint a table someone is
  // reading with rows they did not ask for; the drill refetches itself on
  // every action instead.
  if (drill.registry && !force) return;
  const draw = () => {
    if (!drill.registry) renderOverview();
  };
  try {
    await fetchOverview(force, draw);
    status("");
  } catch (failure) {
    status(failure.message);
  }
}

function status(message) {
  const node = qs("[data-registry-status]");
  if (!node) return;
  node.textContent = message;
  node.classList.toggle("hidden", !message);
}

// bytes renders a storage figure the way registries talk about them.
function bytes(value) {
  if (!value) return "—";
  const units = ["B", "kB", "MB", "GB", "TB"];
  let unit = 0;
  let n = value;
  while (n >= 1024 && unit < units.length - 1) { n /= 1024; unit++; }
  return `${Number(n.toFixed(n >= 100 ? 0 : 2))} ${units[unit]}`;
}

function show(level) {
  const overviewHost = qs("[data-registry-overview]");
  const repos = qs("[data-registry-repos]");
  const tags = qs("[data-registry-tags]");
  if (overviewHost) overviewHost.classList.toggle("hidden", level !== "overview");
  if (repos) repos.hidden = level !== "repos";
  if (tags) tags.hidden = level !== "tags";
}

function renderOverview() {
  const host = qs("[data-registry-overview]");
  if (!host) return;
  show("overview");
  const emptyNote = qs("[data-registry-empty]");
  const sections = [];
  let count = 0;
  capabilities.clear();
  for (const row of overview) {
    capabilities.set(row.account.id, row.capabilities || {});
    const registries = row.error ? [] : (row.registries || []);
    count += registries.length;
    // Grouped by the account key, because that is what a registry is *under*:
    // two accounts on the same provider are two bills and two logins, and a
    // flat list of eight registries makes somebody read every URN to tell
    // which is which.
    sections.push(accountHeading(row.account, registries.length, row.error));
    // A failed account still gets its heading and a line saying so, because
    // "this key stopped working" is a thing the page exists to say.
    if (row.error) {
      sections.push(el("p", "rounded-xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive", row.error));
      continue;
    }
    if (!registries.length) {
      sections.push(el("p", `px-1 text-sm ${muted}`, "No registries on this account."));
      continue;
    }
    const list = el("div", "divide-y divide-border overflow-hidden rounded-xl border border-border");
    list.append(...registries.map((registry) => registryRow(row.account, registry, row.capabilities || {})));
    sections.push(list);
  }
  if (emptyNote) emptyNote.classList.toggle("hidden", sections.length > 0);
  host.replaceChildren(...(emptyNote ? [emptyNote] : []), ...sections);


  const summary = qs("[data-registry-summary]");
  if (summary) {
    summary.textContent = count
      ? `${count} registr${count === 1 ? "y" : "ies"} across the stored accounts`
      : "Nothing on the stored accounts";
  }
  // The create button appears only if some stored account's provider can
  // actually make one. With none, it would be a button whose every press
  // ends in "cloudflare cannot create registries".
  const create = qs("[data-registry-new]");
  if (create) {
    const possible = [...capabilities.values()].some((entry) => entry.registry_maker);
    create.classList.toggle("hidden", !possible);
  }
}

// One line per account: whose key these came from, and how many registries it
// answered for. The same heading the cluster page puts over a group.
function accountHeading(account, total, failed) {
  const row = el("div", "flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-border pb-2");
  row.append(el("h2", "text-sm font-semibold tracking-tight", account.name || "Account"));
  row.append(el("span", `text-xs ${muted}`, account.provider || ""));
  if (failed) row.append(el("span", "text-xs text-destructive", "key not answering"));
  else row.append(el("span", `text-xs ${muted}`, `${total} registr${total === 1 ? "y" : "ies"}`));
  return row;
}

// A row, not a card. The whole line opens the registry — so the delete control
// cannot be nested in a button, which is why the row is a div the click
// handler reads rather than a <button> wrapping everything.
function registryRow(account, registry, capable) {
  const row = el("div", "flex cursor-pointer flex-wrap items-center gap-x-4 gap-y-2 bg-card px-4 py-3 text-card-foreground hover:bg-muted/30");
  row.dataset.registryOpen = registry.id;
  row.dataset.registryAccount = account.id;
  row.dataset.registryName = registry.name;
  row.tabIndex = 0;
  row.setAttribute("role", "button");

  const names = el("div", "flex min-w-0 flex-1 flex-col gap-0.5");
  const title = el("div", "flex min-w-0 items-center gap-2");
  title.append(
    el("p", "truncate font-medium", registry.name),
    el("span", "cn-badge cn-badge-variant-secondary inline-flex w-fit shrink-0 items-center whitespace-nowrap uppercase", registry.region || account.provider),
  );
  if (registry.public) {
    title.append(el("span", "cn-badge cn-badge-variant-outline inline-flex w-fit shrink-0 items-center whitespace-nowrap", "public"));
  }
  names.append(title, el("p", `truncate font-mono text-xs ${muted}`, registry.urn));

  const used = registry.storage_used_bytes || 0;
  const allowed = registry.storage_allowed_bytes || 0;
  const share = allowed ? Math.min(1, used / allowed) : 0;

  // The figures in fixed columns, so they line up down the list — the whole
  // reason this is a list. The meter sits where the cluster row's check strip
  // does, and turns at ninety per cent because that is the row somebody opened
  // the page for.
  const storage = figure("Storage");
  storage.append(
    el("p", "font-mono text-sm tabular-nums", allowed ? bytes(used) : "—"),
    el("p", `text-[.6rem] ${muted}`, allowed ? `of ${bytes(allowed)}` : "no quota reported"),
  );
  const meter = el("div", "hidden h-1.5 w-20 shrink-0 overflow-hidden rounded-full bg-muted xl:flex");
  const fill = el("div", "h-full rounded-full");
  // Inline on purpose: a width assembled from a variable is a class Tailwind
  // never emits, and the fill colour turns with the level.
  fill.style.width = `${Math.round(share * 100)}%`;
  fill.style.background = share > 0.9 ? "var(--destructive)" : "var(--primary)";
  meter.appendChild(fill);

  const since = figure("Added");
  since.append(el("p", "text-sm", registry.created ? new Date(registry.created).toLocaleDateString() : "—"));

  row.append(names, storage, meter, since);
  if (capable.registry_maker) {
    const remove = el("button", "cn-btn cn-btn-variant-ghost cn-btn-size-sm shrink-0 text-muted-foreground hover:text-destructive", "Delete");
    remove.type = "button";
    remove.dataset.registryDelete = registry.id;
    remove.dataset.registryDeleteAccount = account.id;
    remove.dataset.registryDeleteName = registry.name;
    row.append(remove);
  }
  return row;
}

// figure is one fixed column of the row — the same width the cluster list
// uses, because the two pages are read the same way.
function figure(label) {
  const box = el("div", "hidden w-24 shrink-0 flex-col md:flex");
  box.title = label;
  box.append(el("p", `text-[.6rem] uppercase tracking-[.14em] ${muted}`, label));
  return box;
}

async function openRegistry(accountID, registryID, name) {
  drill.account = Number(accountID);
  drill.registry = registryID;
  drill.registryName = name;
  drill.repo = null;
  repoList = [];
  selectedRepos.clear();
  await refreshRepos();
}

// fetchInto keeps the table alive across a refetch: a placeholder only when
// there is nothing yet, a dimmed copy of the current rows while new ones
// are on the way, and one swap when they land.
async function fetchInto(body, columns, fetcher) {
  if (!body) return null;
  if (!body.querySelector("td")) emptyRow(body, columns, "Asking the registry…");
  else body.classList.add("opacity-60");
  try {
    return await fetcher();
  } finally {
    body.classList.remove("opacity-60");
  }
}

async function refreshRepos() {
  show("repos");
  qs("[data-repos-title]").textContent = drill.registryName;
  const body = qs("[data-repo-rows]");
  try {
    const repos = await fetchInto(body, 6, () =>
      request(`/api/registries/repos?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}`));
    repoList = repos || [];
    // A selection can only name rows that still exist.
    const names = new Set(repoList.map((repo) => repo.name));
    for (const name of [...selectedRepos.keys()]) if (!names.has(name)) selectedRepos.delete(name);
    renderRepos();
    status("");
  } catch (failure) {
    emptyRow(body, 6, failure.message);
  }
}

function renderRepos() {
  const body = qs("[data-repo-rows]");
  if (!body) return;
  qs("[data-repos-count]").textContent = `${repoList.length} ${repoList.length === 1 ? "repository" : "repositories"}`;
  if (!repoList.length) emptyRow(body, 6, "Nothing pushed to this registry yet.");
  else body.replaceChildren(...repoList.map(repoRow));
  syncSelection("repos");
}

function repoRow(repo) {
  const row = document.createElement("tr");
  row.className = "cn-table-row cursor-pointer focus:bg-accent focus:outline-none";
  row.tabIndex = 0;
  row.dataset.repoOpen = repo.name;
  row.dataset.repoImage = repo.image;
  row.append(
    selectCell("repos", repo.name, selectedRepos.has(repo.name), { image: repo.image }),
    td(repo.name, "font-mono text-xs font-medium"),
    td(number.format(repo.artifact_count || 0), "tabular-nums"),
    td(number.format(repo.pull_count || 0), "tabular-nums"),
    td(repo.updated_at ? relativeTime(repo.updated_at) : "—", `text-xs ${muted}`),
    actionCell("Delete", { repoDelete: repo.name, repoDeleteImage: repo.image }),
  );
  return row;
}

async function openRepo(name, image) {
  drill.repo = name;
  drill.image = image;
  tagList = [];
  tagFilter = "";
  const filter = qs("[data-tag-filter]");
  if (filter) filter.value = "";
  selectedTags.clear();
  await refreshTags();
}

async function refreshTags() {
  show("tags");
  qs("[data-tags-title]").textContent = drill.repo;
  const body = qs("[data-tag-rows]");
  try {
    const tags = await fetchInto(body, 5, () =>
      request(`/api/registries/tags?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}&repo=${encodeURIComponent(drill.repo)}`));
    tagList = tags || [];
    const names = new Set(tagList.map((tag) => tag.name));
    for (const name of [...selectedTags]) if (!names.has(name)) selectedTags.delete(name);
    renderTags();
    status("");
  } catch (failure) {
    emptyRow(body, 5, failure.message);
  }
}

function visibleTags() {
  const needle = tagFilter.trim().toLowerCase();
  if (!needle) return tagList;
  return tagList.filter((tag) => tag.name.toLowerCase().includes(needle) || (tag.digest || "").includes(needle));
}

function renderTags() {
  const body = qs("[data-tag-rows]");
  if (!body) return;
  const shown = visibleTags();
  qs("[data-tags-count]").textContent = shown.length === tagList.length
    ? `${tagList.length} ${tagList.length === 1 ? "tag" : "tags"}`
    : `${shown.length} of ${tagList.length} tags`;
  if (!tagList.length) emptyRow(body, 5, "This repository has no tags.");
  else if (!shown.length) emptyRow(body, 5, "No tag matches this filter.");
  else body.replaceChildren(...shown.map(tagRow));
  syncSelection("tags");
}

function tagRow(tag) {
  const row = document.createElement("tr");
  row.className = "cn-table-row";
  const digest = (tag.digest || "").replace(/^sha256:/, "");
  row.append(
    selectCell("tags", tag.name, selectedTags.has(tag.name)),
    td(tag.name, "max-w-sm truncate font-mono text-xs font-medium"),
    td(digest ? digest.slice(0, 12) : "—", `font-mono text-[.65rem] ${muted}`),
    td(tag.size_bytes ? bytes(tag.size_bytes) : "—", "whitespace-nowrap font-mono text-xs"),
    actionCell("Delete", { tagDelete: tag.name }),
  );
  return row;
}

function td(value, className = "") {
  const cell = document.createElement("td");
  cell.className = `${cellBase} ${className}`.trim();
  cell.appendChild(text(value));
  return cell;
}

function selectCell(scope, name, checked, extra = {}) {
  const cell = document.createElement("td");
  cell.className = `${cellBase} w-10`;
  const box = document.createElement("input");
  box.type = "checkbox";
  box.className = "size-4 cursor-pointer align-middle accent-primary";
  box.checked = checked;
  box.dataset.select = scope;
  box.dataset.selectName = name;
  for (const [key, value] of Object.entries(extra)) box.dataset[key] = value;
  box.setAttribute("aria-label", `Select ${name}`);
  cell.appendChild(box);
  return cell;
}

function actionCell(label, dataset) {
  const cell = document.createElement("td");
  cell.className = `${cellBase} text-right`;
  const control = el("button", "cn-button cn-button-variant-outline cn-button-size-sm text-destructive hover:text-destructive", label);
  control.type = "button";
  for (const [key, value] of Object.entries(dataset)) control.dataset[key] = value;
  cell.appendChild(control);
  return cell;
}

function emptyRow(body, columns, message) {
  const row = document.createElement("tr");
  const cell = td(message, `py-10 text-center ${muted}`);
  cell.colSpan = columns;
  row.appendChild(cell);
  body.replaceChildren(row);
}

// syncSelection makes the chrome agree with the sets: the select-all box,
// and the batch button that appears the moment anything is ticked.
function syncSelection(scope) {
  const set = scope === "repos" ? selectedRepos : selectedTags;
  const boxes = qsa(`[data-select="${scope}"]`);
  const all = qs(`[data-select-all="${scope}"]`);
  if (all) {
    const ticked = boxes.filter((box) => box.checked).length;
    all.checked = boxes.length > 0 && ticked === boxes.length;
    all.indeterminate = ticked > 0 && ticked < boxes.length;
  }
  const batch = qs(`[data-batch-delete="${scope}"]`);
  if (batch && !batch.disabled) {
    batch.classList.toggle("hidden", set.size === 0);
    batch.textContent = `Delete ${set.size} selected`;
  }
}

// deleteMany runs the deletes one at a time — the registry is somebody's
// production Harbor, and forty concurrent deletes is a stampede — with the
// progress written on the button that started it. Failures are collected
// rather than aborting: the fifth of nine failing must not strand the four
// already gone unreported.
async function deleteMany(scope, items, urlFor) {
  const batch = qs(`[data-batch-delete="${scope}"]`);
  batch.disabled = true;
  const failures = [];
  let done = 0;
  for (const item of items) {
    batch.textContent = `Deleting ${++done} of ${items.length}…`;
    try {
      await request(urlFor(item), { method: "DELETE", headers: adminHeaders() });
    } catch (failure) {
      failures.push(`${item.name}: ${failure.message}`);
    }
  }
  batch.disabled = false;
  overviewAt = 0;
  status(failures.length ? `${failures.length} of ${items.length} failed — ${failures.join(" · ")}` : "");
  return failures.length < items.length;
}

async function batchDeleteRepos() {
  const items = [...selectedRepos].map(([name, image]) => ({ name, image }));
  if (!items.length) return;
  const yes = await ask({
    title: `Delete ${items.length} ${items.length === 1 ? "repository" : "repositories"} from ${drill.registryName}?`,
    body: "Every tag and artifact in each of them is removed at the provider. There is no undo.",
    detail: items.map((item) => item.name).join("\n"),
    confirm: "Delete them",
    phrase: drill.registryName,
  });
  if (!yes) return;
  await deleteMany("repos", items, (item) =>
    `/api/registries/repos?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}&image=${encodeURIComponent(item.image)}&name=${encodeURIComponent(item.name)}`);
  selectedRepos.clear();
  await refreshRepos();
}

async function batchDeleteTags() {
  const items = [...selectedTags].map((name) => ({ name }));
  if (!items.length) return;
  const yes = await ask({
    title: `Delete ${items.length} ${items.length === 1 ? "tag" : "tags"} from ${drill.repo}?`,
    body: "The registry deletes the image behind each tag, so any other tag pointing at the same digest disappears with it.",
    detail: items.map((item) => item.name).join("\n"),
    confirm: "Delete them",
  });
  if (!yes) return;
  await deleteMany("tags", items, (item) =>
    `/api/registries/tags?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}&repo=${encodeURIComponent(drill.repo)}&tag=${encodeURIComponent(item.name)}`);
  selectedTags.clear();
  await refreshTags();
}

// ---- creating and deleting a registry ----

async function openForm() {
  const form = qs("[data-registry-form]");
  if (!form) return;
  form.hidden = false;
  const select = qs("[data-registry-account]", form);
  const [all] = await Promise.all([cloudAccounts(), cloudProviders()]);
  const list = all.filter((account) => can(account, "registry_maker"));
  if (!list.length) {
    placeholder(select, "No accounts that can create one");
    placeholder(qs("[data-registry-region]", form), "No account to ask");
    creatable(false);
    formStatus("No stored account is at a provider guard can order a registry from.");
    return;
  }
  select.replaceChildren(...list.map((account) => new Option(account.name, String(account.id))));
  await fillRegistryOptions();
}

// placeholder leaves a select saying something rather than nothing. An empty
// dropdown reads as a page that failed to load; a dropdown with one line in
// it reads as an answer.
function placeholder(select, message) {
  if (!select) return;
  const option = new Option(message, "");
  option.disabled = true;
  option.selected = true;
  select.replaceChildren(option);
}

function creatable(allowed) {
  const button = qs("[data-registry-create]");
  if (button) button.disabled = !allowed;
}

function formStatus(message) {
  const note = qs("[data-registry-form-status]");
  if (note) note.textContent = message || "";
}

async function fillRegistryOptions() {
  const accountID = Number(qs("[data-registry-account]").value);
  const regionSelect = qs("[data-registry-region]");
  const planSelect = qs("[data-registry-plan]");
  if (!accountID) {
    placeholder(regionSelect, "Choose an account first");
    creatable(false);
    return;
  }
  placeholder(regionSelect, "Asking the provider…");
  formStatus("Asking the provider where a registry can live…");
  try {
    if (!registryOptions.has(accountID)) {
      registryOptions.set(accountID, await request(`/api/registries/options?account=${accountID}`, { headers: adminHeaders() }));
    }
    const answer = registryOptions.get(accountID) || {};
    const regions = answer.regions || [];
    if (!regions.length) {
      placeholder(regionSelect, "This account offers no regions");
      creatable(false);
      formStatus("The provider listed no registry regions for this account.");
      return;
    }
    regionSelect.replaceChildren(...regions.map((region) => {
      const option = new Option(region.name, region.id);
      option.disabled = !region.available;
      return option;
    }));
    const first = [...regionSelect.options].find((option) => !option.disabled);
    if (first) first.selected = true;

    const plans = answer.plans || [];
    qs("[data-registry-plan-field]").hidden = !plans.length;
    planSelect.replaceChildren(...plans.map((plan) => {
      const label = [plan.name || plan.id, plan.storage_gb ? `${plan.storage_gb} GB` : "", plan.price ? `$${plan.price}/mo` : ""]
        .filter(Boolean).join(" · ");
      return new Option(label, plan.id);
    }));
    creatable(!!first);
    formStatus("");
  } catch (failure) {
    placeholder(regionSelect, "Could not read the regions");
    creatable(false);
    formStatus(failure.message);
  }
}

async function createRegistry() {
  const accountID = Number(qs("[data-registry-account]").value);
  const region = qs("[data-registry-region]").value;
  const plan = qs("[data-registry-plan-field]").hidden ? "" : qs("[data-registry-plan]").value;
  const isPublic = qs("[data-registry-public]").checked;
  const name = qs("[data-registry-label]").value.trim();
  if (!name) { formStatus("A name is required."); return; }
  if (!accountID || !region) { formStatus("Choose an account and a region first."); return; }
  const yes = await ask({
    title: `Create ${name}?`,
    body: isPublic
      ? "The provider bills this registry from the moment it is created. It will be public: anybody can pull from it without a credential."
      : "The provider bills this registry from the moment it is created, until it is deleted.",
    confirm: "Create it",
  });
  if (!yes) return;
  formStatus("Ordering…");
  try {
    await request("/api/registries", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ account_id: accountID, name, region, plan, public: isPublic }),
    });
    formStatus("");
    qs("[data-registry-form]").hidden = true;
    qs("[data-registry-label]").value = "";
    qs("[data-registry-public]").checked = false;
    await refreshExplorer(true);
  } catch (failure) {
    formStatus(failure.message);
  }
}

async function deleteRegistry(accountID, registryID, name) {
  const yes = await ask({
    title: `Delete ${name}?`,
    body: "Every repository, every tag and every artifact in this registry is destroyed at the provider, along with the subscription behind them. Anything still pulling from it starts failing immediately, and there is no undo.",
    confirm: "Delete the registry",
    phrase: name,
  });
  if (!yes) return;
  try {
    await request(`/api/registries?account=${accountID}&registry=${encodeURIComponent(registryID)}&name=${encodeURIComponent(name)}`,
      { method: "DELETE", headers: adminHeaders() });
    await refreshExplorer(true);
  } catch (failure) {
    status(failure.message);
  }
}

document.addEventListener("input", (event) => {
  if (!event.target.matches("[data-tag-filter]")) return;
  tagFilter = event.target.value;
  renderTags();
});

document.addEventListener("change", (event) => {
  if (event.target.closest("[data-registry-account]")) { fillRegistryOptions(); return; }
  const all = event.target.closest("[data-select-all]");
  if (all) {
    const scope = all.dataset.selectAll;
    const set = scope === "repos" ? selectedRepos : selectedTags;
    // Select-all means "all the rows I can see": under a tag filter that is
    // the filtered list, which is exactly what makes filter-then-select-all
    // a way to say "everything matching this".
    for (const box of qsa(`[data-select="${scope}"]`)) {
      box.checked = all.checked;
      if (scope === "repos") {
        if (all.checked) selectedRepos.set(box.dataset.selectName, box.dataset.image);
        else selectedRepos.delete(box.dataset.selectName);
      } else if (all.checked) set.add(box.dataset.selectName);
      else set.delete(box.dataset.selectName);
    }
    syncSelection(scope);
    return;
  }
  const box = event.target.closest("[data-select]");
  if (box) {
    const scope = box.dataset.select;
    if (scope === "repos") {
      if (box.checked) selectedRepos.set(box.dataset.selectName, box.dataset.image);
      else selectedRepos.delete(box.dataset.selectName);
    } else if (box.checked) selectedTags.add(box.dataset.selectName);
    else selectedTags.delete(box.dataset.selectName);
    syncSelection(scope);
  }
});

document.addEventListener("click", (event) => {
  // A tick is a tick, not a navigation: the change listener above owns it,
  // and falling through would open the row the checkbox sits in.
  if (event.target.closest("[data-select],[data-select-all]")) return;

  if (event.target.closest("[data-registry-refresh]")) { refreshExplorer(true); return; }
  if (event.target.closest("[data-registry-new]")) { openForm(); return; }
  if (event.target.closest("[data-registry-cancel]")) { qs("[data-registry-form]").hidden = true; return; }
  if (event.target.closest("[data-registry-create]")) { createRegistry(); return; }

  const removeRegistry = event.target.closest("[data-registry-delete]");
  if (removeRegistry) {
    deleteRegistry(
      Number(removeRegistry.dataset.registryDeleteAccount),
      removeRegistry.dataset.registryDelete,
      removeRegistry.dataset.registryDeleteName,
    );
    return;
  }

  const batch = event.target.closest("[data-batch-delete]");
  if (batch && !batch.disabled) {
    if (batch.dataset.batchDelete === "repos") batchDeleteRepos();
    else batchDeleteTags();
    return;
  }

  const back = event.target.closest("[data-registry-back]");
  if (back) {
    if (back.dataset.registryBack === "overview") {
      drill.registry = null;
      drill.repo = null;
      renderOverview();
    } else {
      drill.repo = null;
      refreshRepos();
    }
    return;
  }

  const removeRepo = event.target.closest("[data-repo-delete]");
  if (removeRepo) {
    const name = removeRepo.dataset.repoDelete;
    const image = removeRepo.dataset.repoDeleteImage;
    const short = name.split("/").pop();
    ask({
      title: `Delete ${name}?`,
      body: "Every tag and artifact in this repository is removed at the provider. There is no undo.",
      confirm: "Delete the repository",
      phrase: short,
    }).then(async (yes) => {
      if (!yes) return;
      try {
        await request(`/api/registries/repos?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}&image=${encodeURIComponent(image)}&name=${encodeURIComponent(name)}`,
          { method: "DELETE", headers: adminHeaders() });
        overviewAt = 0;
        selectedRepos.delete(name);
        await refreshRepos();
      } catch (failure) {
        status(failure.message);
      }
    });
    return;
  }

  const removeTag = event.target.closest("[data-tag-delete]");
  if (removeTag) {
    const tag = removeTag.dataset.tagDelete;
    ask({
      title: `Delete ${drill.repo}:${tag}?`,
      body: "The registry deletes the image behind the tag, so any other tag pointing at the same digest disappears with it.",
      confirm: "Delete the tag",
    }).then(async (yes) => {
      if (!yes) return;
      try {
        await request(`/api/registries/tags?account=${drill.account}&registry=${encodeURIComponent(drill.registry)}&repo=${encodeURIComponent(drill.repo)}&tag=${encodeURIComponent(tag)}`,
          { method: "DELETE", headers: adminHeaders() });
        overviewAt = 0;
        selectedTags.delete(tag);
        await refreshTags();
      } catch (failure) {
        status(failure.message);
      }
    });
    return;
  }

  const openCard = event.target.closest("[data-registry-open]");
  if (openCard) {
    openRegistry(openCard.dataset.registryAccount, openCard.dataset.registryOpen, openCard.dataset.registryName);
    return;
  }
  const repoRowNode = event.target.closest("[data-repo-open]");
  if (repoRowNode) openRepo(repoRowNode.dataset.repoOpen, repoRowNode.dataset.repoImage);
});

document.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" && event.key !== " ") return;
  if (event.target.matches("[data-repo-open]")) {
    event.preventDefault();
    openRepo(event.target.dataset.repoOpen, event.target.dataset.repoImage);
    return;
  }
  // The registry rows are divs the click handler reads, so they are only
  // reachable from the keyboard if the keyboard opens them too.
  if (event.target.matches("[data-registry-open]")) {
    event.preventDefault();
    openRegistry(event.target.dataset.registryAccount, event.target.dataset.registryOpen, event.target.dataset.registryName);
  }
});
