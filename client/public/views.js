// The views page: saved panels, and the builder that makes them.
//
// guard.js owns the signal pages and the live tick; this owns everything under
// /views and is imported by it, so the code only runs where it applies. The
// division is the same one the server makes — a view is a stored query, and
// this file is the only place that knows what a query looks like as a form.

import { adminHeaders, el, qs, qsa, request, text } from "./core.js";
import { bodyKind, describe, draw, fieldLabel } from "./charts.js";

// The catalogue is the running binary's own answer to "what can be built" —
// panels it can compile, aggregations it implements, fields it has seen. Cached
// for the session because the first two only change with the binary, and the
// third is refreshed whenever the builder opens.
let catalogue = null;
let views = [];
let editing = null;
let previewTimer;
let previewToken = 0;
let renderedSignature = "";

const panelSpec = (panel) => catalogue?.panels.find((item) => item.panel === panel);

async function loadCatalogue(force = false) {
  if (catalogue && !force) return catalogue;
  catalogue = await request("/api/views/catalogue");
  return catalogue;
}

// ---------------------------------------------------------------------------
// The dashboard
// ---------------------------------------------------------------------------

const signatureOf = (list) => list.map((view) => `${view.id}:${view.updated_at}:${view.width}`).join(",");

export async function refreshViews() {
  const grid = qs("[data-view-grid]");
  if (!grid) return;
  // The live tick keeps running while the layout is being changed, and
  // rebuilding the grid under a card someone is holding would drop it.
  if (dragging) return;
  // A refresh that was already in flight when the layout changed is answering
  // a question about the old order. Applying it would put the card back where
  // it was, a moment after it was moved — so the revision it started under is
  // checked against the one in force when it lands.
  const revision = layoutRevision;
  const fetched = await request("/api/views");
  if (revision !== layoutRevision) return;
  views = fetched;
  qs("[data-view-empty]")?.toggleAttribute("hidden", views.length > 0);

  // The panels are rebuilt only when the set of them changes. A live tick
  // redraws bodies, and rebuilding the cards underneath would throw away the
  // scroll position of every waterfall on the page every three seconds.
  const signature = signatureOf(views);
  if (signature !== renderedSignature) {
    renderedSignature = signature;
    const template = qs("[data-panel-template]");
    grid.replaceChildren(...views.map((view) => buildPanel(template, view)));
  }
  await Promise.allSettled(views.map((view) => loadPanel(view)));
}

function buildPanel(template, view) {
  const node = template.content.firstElementChild.cloneNode(true);
  node.dataset.panelId = view.id;
  node.style.cssText = gridSpan(view.width);
  qs("[data-panel-title]", node).textContent = view.name;
  qs("[data-panel-meta]", node).textContent = view.description || describe(view.query, view.panel);

  const body = qs("[data-panel-body]", node);
  body.appendChild(bodyFor(view.panel));
  return node;
}

// Written here rather than as a Tailwind class because the span comes from the
// database: `lg:col-span-7` assembled from a variable is a class the compiler
// never sees and therefore never emits.
function gridSpan(width) {
  const span = Math.min(Math.max(width || 6, 1), 12);
  return window.matchMedia("(min-width: 1024px)").matches ? `grid-column: span ${span} / span ${span};` : "";
}

function bodyFor(panel) {
  const kind = bodyKind(panel);
  const template = qs(`[data-panel-body-template="${kind}"]`);
  return template ? template.content.firstElementChild.cloneNode(true) : el("div", "h-full");
}

async function loadPanel(view) {
  const node = qs(`[data-panel-id="${view.id}"]`);
  if (!node) return;
  const error = qs("[data-panel-error]", node);
  const notes = qs("[data-panel-notes]", node);
  const params = new URLSearchParams({ id: String(view.id) });
  const range = qs('[data-builder="dashboard_range"]')?.value || "";
  if (range) params.set("range", range);
  try {
    const frame = await request(`/api/views/data?${params}`);
    error.textContent = "";
    notes.textContent = (frame.notes || []).join(" ");
    const body = qs("[data-panel-body]", node).firstElementChild || qs("[data-panel-body]", node);
    // The drill has to be against the window that was drawn, not the one that
    // was saved — the dashboard picker may have overridden it, and a list of
    // events from a different hour than the bar is worse than no list.
    const drawn = { ...view, query: range ? { ...view.query, range } : view.query };
    const legend = draw(body, frame, { view: drawn, ...interactions(drawn) });
    renderLegend(qs("[data-panel-legend]", node), legend);
  } catch (failure) {
    error.textContent = failure.message;
  }
}

// What a mark does when it is hovered or clicked. One event opens directly;
// anything summarising several opens the list of them. guard.js owns the
// drawer both go to.
function interactions(view) {
  return {
    onEvent: (id) => globalThis.guardOpenDetail?.(id),
    onSelect: (selection, datum) => globalThis.guardOpenDrill?.(
      { panel: view.panel, query: view.query, selection: selection || {} },
      { title: datum?.title ? `${view.name || "Panel"} · ${datum.title}` : view.name },
    ),
  };
}

function renderLegend(host, entries) {
  if (!host) return;
  if (!entries?.length) return host.replaceChildren();
  // These class strings are literal so Tailwind can find them; the colour is
  // inline for the same reason it cannot be.
  host.replaceChildren(...entries.slice(0, 12).map((entry) => {
    const node = el("span", "inline-flex items-center gap-2 text-xs text-muted-foreground");
    const dot = el("span", "size-2 shrink-0 rounded-full");
    dot.style.background = entry.colour;
    node.append(dot, text(entry.label));
    return node;
  }));
}

// ---------------------------------------------------------------------------
// The builder
// ---------------------------------------------------------------------------

async function openBuilder(id) {
  // The shell opens before the catalogue is fetched, on purpose. If that
  // request fails there is nothing to populate the pickers from — and a button
  // that silently does nothing is the hardest kind of failure to report, so the
  // panel opens and says what went wrong instead.
  const shell = qs("[data-builder-shell]");
  editing = id === "new" ? null : views.find((view) => String(view.id) === String(id));
  qs("[data-builder-title]").textContent = editing ? editing.name : "New panel";
  qs("[data-builder-status]").textContent = "";
  shell.classList.add("open");
  document.body.classList.add("overflow-hidden");

  try {
    await loadCatalogue(true);
  } catch (failure) {
    qs("[data-builder-status]").textContent = `Could not load the field catalogue: ${failure.message}`;
    return;
  }

  populateChoices();
  const query = editing?.query || {};
  setValue("name", editing?.name || "");
  setValue("panel", editing?.panel || "timeseries");
  setValue("width", String(editing?.width || 6));
  setValue("signal", query.signal || "");
  setValue("range", query.range || "1h");
  setValue("agg", query.agg || "count");
  setValue("value", query.value || "");
  setValue("group_by", query.group_by || "");
  setValue("x", query.x || "timestamp");
  setValue("bucket", query.bucket || "auto");
  setValue("buckets", query.buckets || 24);
  setValue("limit", query.limit || 12);
  setValue("order", query.order === "slowest" || query.order === "latest" ? "value_desc" : query.order || "value_desc");
  setValue("max", query.max || "");
  setValue("warn", query.warn || "");
  setValue("critical", query.critical || "");
  setValue("trace_id", query.trace_id || "");
  setValue("trace_pick", query.order === "slowest" ? "slowest" : "latest");

  const filters = qs("[data-filters]");
  filters.replaceChildren(...(query.filters || []).map(addFilterRow));

  applyNeeds();
  preview();
}

function closeBuilder() {
  qs("[data-builder-shell]")?.classList.remove("open");
  document.body.classList.remove("overflow-hidden");
  editing = null;
}

function setValue(name, value) {
  const node = qs(`[data-builder="${name}"]`);
  if (node) node.value = value ?? "";
}

function readValue(name) {
  return qs(`[data-builder="${name}"]`)?.value ?? "";
}

// The pickers are populated from the catalogue rather than hardcoded: a panel
// this binary cannot compile can then never be offered, and a field nothing has
// ever sent cannot be grouped by.
function populateChoices() {
  fillSelect(qs('[data-builder="panel"]'), catalogue.panels.map((spec) => [spec.panel, spec.label]));
  fillSelect(qs('[data-builder="agg"]'), catalogue.aggregations.map((agg) => [agg, aggLabel(agg)]));

  const columns = catalogue.fields.columns || [];
  const attributes = catalogue.fields.attributes || [];
  const all = [...columns, ...attributes];
  const numeric = [...columns.filter((field) => field.type === "number"), ...attributes];

  fillSelect(qs('[data-builder="value"]'), numeric.map(fieldOption));
  fillSelect(qs('[data-builder="group_by"]'), [["", "Nothing — one series"], ...all.map(fieldOption)]);
  fillSelect(qs('[data-builder="x"]'), all.map(fieldOption));
  for (const select of qsa('[data-builder="filter_field"]')) fillSelect(select, all.map(fieldOption));
}

// An indexed attribute is a generated column with an index behind it; grouping
// by anything else means a JSON extract over every candidate row. The
// difference is worth a word in the picker.
function fieldOption(field) {
  return [field.ref, field.indexed ? `${field.label} · indexed` : field.label];
}

function aggLabel(agg) {
  return { count: "count of events", sum: "sum", avg: "average", min: "minimum", max: "maximum" }[agg] || agg;
}

function fillSelect(select, options) {
  if (!select) return;
  const current = select.value;
  select.replaceChildren(...options.map(([value, labelText]) => {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = labelText;
    return option;
  }));
  if (options.some(([value]) => value === current)) select.value = current;
}

// Show the controls this panel actually reads. The list comes from the panel's
// own spec on the server, so a form control and the compiler cannot disagree
// about whether a bucket size matters.
function applyNeeds() {
  const spec = panelSpec(readValue("panel"));
  const needs = new Set(spec?.needs || []);
  // A count needs no field to count, so the value picker follows the
  // aggregation rather than the panel alone.
  const countOnly = readValue("agg") === "count" && !["distribution", "heatmap", "ohlc", "scatter"].includes(spec?.shape);
  for (const node of qsa("[data-needs]")) {
    const need = node.dataset.needs;
    const wanted = needs.has(need) && !(need === "value" && countOnly);
    node.toggleAttribute("hidden", !wanted);
  }
  qs("[data-builder-hint]").textContent = spec?.hint || "";
}

function addFilterRow(condition = {}) {
  const template = qs("[data-filter-row-template]");
  const row = template.content.firstElementChild.cloneNode(true);
  const fields = [...(catalogue.fields.columns || []), ...(catalogue.fields.attributes || [])];
  fillSelect(qs('[data-builder="filter_field"]', row), fields.map(fieldOption));
  if (condition.field) qs('[data-builder="filter_field"]', row).value = condition.field;
  if (condition.op) qs('[data-builder="filter_op"]', row).value = condition.op;
  if (condition.value) qs('[data-builder="filter_value"]', row).value = condition.value;
  return row;
}

// collect turns the form into the object the API takes. Numbers are sent as
// numbers and blanks as absent, because the Go side distinguishes "zero" from
// "not set" — a gauge with max 0 scales itself, a gauge with max unset does too,
// but a gauge with max 100 does not.
function collect() {
  const panel = readValue("panel");
  const spec = panelSpec(panel);
  const needs = new Set(spec?.needs || []);
  const query = {
    signal: readValue("signal") || undefined,
    range: readValue("range") || undefined,
    filters: qsa("[data-filter-row]").map((row) => ({
      field: qs('[data-builder="filter_field"]', row).value,
      op: qs('[data-builder="filter_op"]', row).value,
      value: qs('[data-builder="filter_value"]', row).value,
    })).filter((condition) => condition.field && condition.op),
  };
  if (needs.has("agg")) query.agg = readValue("agg");
  if (needs.has("value") && !(query.agg === "count" && !["distribution", "heatmap", "ohlc", "scatter"].includes(spec?.shape))) {
    query.value = readValue("value") || undefined;
  }
  if (needs.has("group")) query.group_by = readValue("group_by") || undefined;
  if (needs.has("x")) query.x = readValue("x") || undefined;
  if (needs.has("bucket")) query.bucket = readValue("bucket") || undefined;
  if (needs.has("buckets")) query.buckets = Number(readValue("buckets")) || undefined;
  if (needs.has("limit")) {
    query.limit = Number(readValue("limit")) || undefined;
    query.order = readValue("order") || undefined;
  }
  if (needs.has("thresholds")) {
    query.max = Number(readValue("max")) || undefined;
    query.warn = Number(readValue("warn")) || undefined;
    query.critical = Number(readValue("critical")) || undefined;
  }
  if (needs.has("trace")) {
    query.trace_id = readValue("trace_id") || undefined;
    query.order = readValue("trace_pick");
  }
  return {
    id: editing?.id || 0,
    name: readValue("name"),
    panel,
    width: Number(readValue("width")) || 6,
    query,
  };
}

// The preview is the whole point of the builder: the panel on screen is drawn
// by the same renderer, from the same compiler, as the panel you would save.
async function preview() {
  const view = collect();
  const host = qs("[data-preview-body]");
  const status = qs("[data-preview-status]");
  const error = qs("[data-preview-error]");
  const notes = qs("[data-preview-notes]");
  if (!host) return;

  host.replaceChildren(bodyFor(view.panel));
  status.textContent = "running…";
  const token = ++previewToken;
  try {
    const frame = await request("/api/views/preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ panel: view.panel, query: view.query }),
    });
    // A slower earlier preview must not overwrite a faster later one.
    if (token !== previewToken) return;
    error.textContent = "";
    notes.textContent = (frame.notes || []).join(" ");
    status.textContent = `${frame.rows.length} row${frame.rows.length === 1 ? "" : "s"}`;
    // The preview is clickable too: checking that a bar contains what you
    // meant it to is most of what building a panel is.
    renderLegend(qs("[data-preview-legend]"), draw(host.firstElementChild || host, frame, { view, ...interactions(view) }));
  } catch (failure) {
    if (token !== previewToken) return;
    status.textContent = "";
    notes.textContent = "";
    error.textContent = failure.message;
    qs("[data-preview-legend]").replaceChildren();
  }
}

function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(preview, 250);
}

async function saveView(form) {
  const view = collect();
  const status = qs("[data-builder-status]", form);
  if (!view.name.trim()) {
    status.textContent = "A panel needs a name.";
    return;
  }
  status.textContent = "Saving…";
  try {
    const path = view.id ? `/api/views/${view.id}` : "/api/views";
    await request(path, { method: view.id ? "PUT" : "POST", headers: adminHeaders(), body: JSON.stringify(view) });
    closeBuilder();
    renderedSignature = "";
    await refreshViews();
  } catch (failure) {
    status.textContent = failure.message;
  }
}

async function deleteView(id) {
  const view = views.find((item) => String(item.id) === String(id));
  if (!view || !confirm(`Delete “${view.name}”?`)) return;
  await request(`/api/views/${id}`, { method: "DELETE", headers: adminHeaders() });
  renderedSignature = "";
  await refreshViews();
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// One set of document-level listeners, installed once. The views page can be
// navigated away from and back to — this module is imported once per document,
// so binding on mount would add a second copy of every handler each visit.
document.addEventListener("click", (event) => {
  const open = event.target.closest("[data-builder-open]");
  if (open) {
    openBuilder(open.dataset.builderOpen).catch((failure) => console.error(failure));
    return;
  }
  if (event.target.closest("[data-builder-close]")) {
    closeBuilder();
    return;
  }
  const edit = event.target.closest("[data-panel-edit]");
  if (edit) {
    openBuilder(edit.closest("[data-panel-id]").dataset.panelId).catch((failure) => console.error(failure));
    return;
  }
  const remove = event.target.closest("[data-panel-delete]");
  if (remove) {
    deleteView(remove.closest("[data-panel-id]").dataset.panelId).catch((failure) => {
      qs("[data-panel-error]", remove.closest("[data-panel-id]")).textContent = failure.message;
    });
    return;
  }
  if (event.target.closest("[data-filter-add]")) {
    qs("[data-filters]")?.appendChild(addFilterRow());
    return;
  }
  const removeFilter = event.target.closest("[data-filter-remove]");
  if (removeFilter) {
    removeFilter.closest("[data-filter-row]").remove();
    schedulePreview();
    return;
  }
  if (event.target.closest("[data-views-refresh]")) refreshViews().catch(() => {});
  const samples = event.target.closest("[data-views-samples]");
  if (samples) {
    samples.disabled = true;
    addSamples().finally(() => { samples.disabled = false; });
  }
});

// One panel per visualisation, built from this instance's own telemetry. The
// difference between a state timeline and a status history is obvious once you
// have seen both drawn from your own data, and unclear in any number of words.
async function addSamples() {
  const empty = qs("[data-view-empty]");
  try {
    const created = await request("/api/views/samples", { method: "POST", headers: adminHeaders() });
    renderedSignature = "";
    await refreshViews();
    if (!created.length && empty) empty.textContent = "Those sample panels already exist.";
  } catch (failure) {
    if (empty) empty.textContent = failure.message;
  }
}

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && qs("[data-builder-shell]")?.classList.contains("open")) closeBuilder();
});

document.addEventListener("input", (event) => {
  if (!event.target.closest("[data-builder-form]")) return;
  if (event.target.matches("[data-builder]")) schedulePreview();
});

document.addEventListener("change", (event) => {
  if (event.target.matches('[data-builder="dashboard_range"]')) {
    Promise.allSettled(views.map((view) => loadPanel(view)));
    return;
  }
  if (!event.target.closest("[data-builder-form]")) return;
  if (event.target.matches('[data-builder="panel"]') || event.target.matches('[data-builder="agg"]')) applyNeeds();
  if (event.target.matches("[data-builder]")) schedulePreview();
});

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-builder-form]")) return;
  event.preventDefault();
  saveView(event.target);
});

// ---------------------------------------------------------------------------
// Reordering
// ---------------------------------------------------------------------------
//
// Native drag and drop, driven from the handle. The alternative — pointer
// events and a floating clone — is a few hundred lines to reimplement what the
// platform already does, including the drag image, the cursor, and the escape
// key cancelling the whole thing.
//
// The order is applied to the DOM as you drag and sent once on drop. Sending
// per-move would be a request per pixel; waiting for the response to move the
// card would be a card that lags the pointer.

let dragging = null;
// Bumped whenever the layout changes locally, so a refresh that crosses a
// reorder can tell that its answer is out of date.
let layoutRevision = 0;

function panelOf(node) {
  return node?.closest("[data-panel-id]");
}

document.addEventListener("dragstart", (event) => {
  const handle = event.target.closest("[data-panel-drag]");
  if (!handle) return;
  dragging = panelOf(handle);
  if (!dragging) return;
  dragging.classList.add("opacity-40");
  event.dataTransfer.effectAllowed = "move";
  // Firefox will not start a drag without data on the transfer.
  event.dataTransfer.setData("text/plain", dragging.dataset.panelId);
});

document.addEventListener("dragover", (event) => {
  if (!dragging) return;
  const over = panelOf(event.target);
  event.preventDefault();
  event.dataTransfer.dropEffect = "move";
  if (!over || over === dragging) return;
  // Compare against the midpoint of the card the pointer is over, so a card
  // swaps once you are past half of it rather than the moment you touch it.
  const box = over.getBoundingClientRect();
  const after = event.clientX > box.left + box.width / 2;
  over.parentNode.insertBefore(dragging, after ? over.nextSibling : over);
});

document.addEventListener("drop", (event) => {
  if (dragging) event.preventDefault();
});

document.addEventListener("dragend", () => {
  if (!dragging) return;
  dragging.classList.remove("opacity-40");
  dragging = null;
  saveOrder();
});

// Arrow keys on a focused handle. Same operation, no pointer.
document.addEventListener("keydown", (event) => {
  const handle = event.target.closest?.("[data-panel-drag]");
  if (!handle) return;
  const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
  if (!step) return;
  event.preventDefault();
  const panel = panelOf(handle);
  const sibling = step > 0 ? panel.nextElementSibling : panel.previousElementSibling;
  if (!sibling) return;
  panel.parentNode.insertBefore(step > 0 ? sibling : panel, step > 0 ? panel : sibling);
  // Moving a node detaches it, and a detached node loses focus — which would
  // end the keyboard interaction after one press.
  handle.focus();
  saveOrder();
});

async function saveOrder() {
  const ids = qsa("[data-panel-id]").map((node) => Number(node.dataset.panelId));
  if (!ids.length) return;
  // The grid already shows the new order, so nothing here re-renders on
  // success — only a failure has anything to say.
  layoutRevision++;
  views = ids.map((id) => views.find((view) => view.id === id)).filter(Boolean);
  renderedSignature = signatureOf(views);
  try {
    views = await request("/api/views/order", {
      method: "PUT",
      headers: adminHeaders(),
      body: JSON.stringify({ ids }),
    });
    renderedSignature = signatureOf(views);
  } catch (failure) {
    // Put the dashboard back the way the server still has it, rather than
    // leaving a layout on screen that will not survive a reload.
    layoutRevision++;
    renderedSignature = "";
    await refreshViews();
    const empty = qs("[data-view-empty]");
    if (empty) empty.textContent = failure.message;
  }
}

export function mountViews() {
  renderedSignature = "";
  return refreshViews();
}

export function unmountViews() {
  clearTimeout(previewTimer);
  previewTimer = undefined;
  // Any preview still in flight belongs to a page that is gone.
  previewToken++;
  closeBuilder();
}
