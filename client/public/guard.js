// The dashboard's own client code: the signal tables, the filter bar, the live
// tick and the detail panel.
//
// The shared vocabulary lives in core.js, the panel renderers in charts.js, and
// everything under /views in views.js. This file imports all three; none of
// them imports this one, so the dependency runs one way.
import { adminHeaders, muted, number, palette, qs, qsa, relativeTime, request, shortID, svgNS, text, timeText } from "./core.js";
import { drawWaterfall } from "./charts.js";
import { mountViews, refreshViews, unmountViews } from "./views.js";

const pageSize = 50;
const signalPages = new Map([["logs", 0], ["traces", 0], ["metrics", 0]]);
const signalRequests = new Map();
const facetsTTL = 60_000;
let facetsCache = null;
let facetsFetchedAt = 0;
let facetsRequest = null;
let live = localStorage.getItem("guard.live") !== "off";
let filterTimer;
let initializedPage = null;
let initialRefresh = null;
let liveTimer;
let refreshInFlight = false;
let lastInteraction = 0;
let renderGeneration = 0;
let summarySignature = "";
const signalSignatures = new Map();
let metricsSignature = "";

function setStat(name, value) {
  for (const node of qsa(`[data-stat="${name}"]`)) node.textContent = number.format(value ?? 0);
}

function updateLiveControl(status = live ? "receiving" : "paused") {
  for (const node of qsa("[data-live-status]")) node.textContent = status;
  for (const node of qsa("[data-live-action]")) node.textContent = live ? "Pause" : "Resume";
  for (const node of qsa("[data-live-toggle]")) node.setAttribute("aria-label", live ? "Pause live refresh" : "Resume live refresh");
  for (const node of qsa("[data-live-dot]")) node.classList.toggle("opacity-30", !live);
}

// The class strings below are written out in full on purpose: Tailwind scans
// this file (see the @source list in client/styles/app.css) and only emits the
// utilities it can see literally.
const cellBase = "cn-table-cell cn-table-cell-aria";

function td(value, className = "") {
  const cell = document.createElement("td");
  cell.className = `${cellBase} ${className}`.trim();
  cell.appendChild(text(value));
  return cell;
}

function eventRow(event) {
  const row = document.createElement("tr");
  row.className = "cn-table-row cursor-pointer focus:bg-accent focus:outline-none";
  row.tabIndex = 0;
  row.dataset.eventId = event.id;
  return row;
}

function eventText(event) { return event.message || (event.value !== undefined ? `${event.name} = ${event.value}${event.unit ? ` ${event.unit}` : ""}` : event.name) || "telemetry event"; }

// style-nova ships default/secondary/destructive/outline badges; severity and
// span status need two more tones, built from the same theme tokens.
const tones = {
  neutral: "cn-badge-variant-secondary",
  error: "cn-badge-variant-destructive",
  warning: "border-warning/40 bg-warning/15 text-warning",
  ok: "border-primary/40 bg-primary/15 text-primary",
  trace: "border-chart-3/40 bg-chart-3/15 text-chart-3",
  metric: "border-chart-2/40 bg-chart-2/15 text-chart-2",
};

function badge(value, tone = tones.neutral) {
  const node = document.createElement("span");
  node.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${tone}`;
  node.appendChild(text(value));
  return node;
}

function emptyRow(body, columns, message) {
  const row = document.createElement("tr");
  const cell = td(message, `py-12 text-center ${muted}`);
  cell.colSpan = columns;
  row.appendChild(cell);
  body.replaceChildren(row);
}

function renderLogs(events) {
  const body = qs("[data-log-rows]");
  if (!body) return;
  if (!events.length) return emptyRow(body, 5, "No logs match this view.");
  body.replaceChildren(...events.map((event) => {
    const row = eventRow(event);
    const severity = document.createElement("td");
    severity.className = cellBase;
    severity.appendChild(badge(event.severity || "INFO", /error|fatal/i.test(event.severity || "") ? tones.error : /warn/i.test(event.severity || "") ? tones.warning : tones.neutral));
    row.append(td(timeText(event.timestamp), `whitespace-nowrap font-mono text-xs ${muted}`), severity,
      td(event.service, "font-medium"), td(event.message || "—", "max-w-xl truncate"), td(shortID(event.trace_id), `font-mono text-[.65rem] ${muted}`));
    return row;
  }));
}

function renderTraces(events) {
  const body = qs("[data-trace-rows]");
  if (!body) return;
  if (!events.length) return emptyRow(body, 7, "No spans match this view.");
  body.replaceChildren(...events.map((event) => {
    const row = eventRow(event);
    const status = document.createElement("td");
    status.className = cellBase;
    status.appendChild(badge((event.severity || "OK").toLowerCase(), /error/i.test(event.severity || "") ? tones.error : tones.ok));
    row.append(td(timeText(event.timestamp), `whitespace-nowrap font-mono text-xs ${muted}`), status,
      td(event.service, "font-medium"), td(event.name || "unnamed span", "max-w-sm truncate"), td((event.kind || "internal").toLowerCase(), `text-xs ${muted}`),
      td(`${number.format(event.duration_ms || 0)} ms`, "whitespace-nowrap font-mono text-xs"), td(shortID(event.trace_id), `font-mono text-[.65rem] ${muted}`));
    return row;
  }));
}

function renderMetricRows(events) {
  const body = qs("[data-metric-rows]");
  if (!body) return;
  if (!events.length) return emptyRow(body, 6, "No metric points match this view.");
  body.replaceChildren(...events.map((event) => {
    const row = eventRow(event);
    row.append(td(timeText(event.timestamp), `whitespace-nowrap font-mono text-xs ${muted}`), td(event.name, "font-mono text-xs"),
      td(event.service, "font-medium"), td(event.metric_type || "number", `text-xs ${muted}`),
      td(`${number.format(event.value ?? 0)}${event.unit ? ` ${event.unit}` : ""}`, "whitespace-nowrap font-mono text-xs"),
      td(`${Object.keys(event.attributes || {}).length} fields`, `text-xs ${muted}`));
    return row;
  }));
}

function renderOverview(events) {
  const body = qs('[data-event-rows][data-signal=""]');
  if (!body) return;
  qs("[data-recent-empty]")?.remove();
  if (!events.length) return emptyRow(body, 5, "Telemetry will appear here as it arrives.");
  body.replaceChildren(...events.map((event) => {
    const row = eventRow(event);
    const signal = document.createElement("td");
    signal.className = cellBase;
    const label = event.signal === "logs" ? (event.severity || "log").toLowerCase() : event.signal.replace(/s$/, "");
    const tone = event.signal === "traces" ? tones.trace : event.signal === "metrics" ? tones.metric : /error|fatal/i.test(event.severity || "") ? tones.error : tones.neutral;
    signal.appendChild(badge(label, tone));
    row.append(td(timeText(event.timestamp), `font-mono text-xs ${muted}`), signal, td(event.service), td(eventText(event), "max-w-lg truncate"), td(shortID(event.trace_id), `font-mono text-[.65rem] ${muted}`));
    return row;
  }));
}

function renderInstances(instances) {
  const list = qs("[data-instance-list]");
  if (!list) return;
  if (!instances.length) { list.replaceChildren(); return; }
  list.replaceChildren(...instances.map((instance) => {
    const row = document.createElement("div");
    row.className = "flex items-center gap-3 rounded-xl bg-muted/40 p-3";
    const dot = document.createElement("span"); dot.className = "signal-dot";
    const names = document.createElement("div"); names.className = "min-w-0 flex-1";
    const service = document.createElement("p"); service.className = "truncate text-sm font-medium"; service.appendChild(text(instance.service));
    const id = document.createElement("p"); id.className = `truncate font-mono text-[.65rem] ${muted}`; id.appendChild(text(instance.instance || "default"));
    names.append(service, id);
    const seen = document.createElement("span"); seen.className = `text-xs ${muted}`; seen.appendChild(text(relativeTime(instance.last_seen)));
    row.append(dot, names, seen); return row;
  }));
}

function filterParams(signal, paginate = false) {
  const root = qs(`[data-filter-signal="${signal}"]`);
  const params = new URLSearchParams({ signal, limit: paginate ? String(pageSize + 1) : signal === "metrics" ? "500" : "250" });
  if (paginate) params.set("offset", String((signalPages.get(signal) || 0) * pageSize));
  if (!root) return params;
  const value = (name) => qs(`[data-filter="${name}"]`, root)?.value || "";
  for (const name of ["service", "severity", "name"]) if (value(name)) params.set(name, value(name));
  if (value("query")) params.set("q", value("query"));
  const range = value("range");
  const durations = { "15m": 15 * 60e3, "1h": 3600e3, "6h": 6 * 3600e3, "24h": 24 * 3600e3, "7d": 7 * 86400e3 };
  if (durations[range]) params.set("from", new Date(Date.now() - durations[range]).toISOString());
  if (range === "custom") {
    if (value("from")) params.set("from", new Date(value("from")).toISOString());
    if (value("to")) params.set("to", new Date(value("to")).toISOString());
  }
  return params;
}

async function refreshSummary() {
  try {
    const data = await request("/api/summary");
    const signature = `${renderGeneration}:${data.logs}:${data.errors}:${data.spans}:${data.metrics}:${(data.instances || []).map((item) => `${item.service}/${item.instance}/${item.last_seen}`).join(",")}:${(data.recent || []).map((event) => event.id).join(",")}`;
    if (signature === summarySignature) return;
    summarySignature = signature;
    setStat("logs", data.logs); setStat("errors", data.errors); setStat("spans", data.spans); setStat("metrics", data.metrics); setStat("instances", data.instances?.length);
    renderInstances(data.instances || []); renderOverview(data.recent || []);
    updateLiveControl();
  } catch { updateLiveControl("reconnecting"); }
}

async function refreshSignal(signal) {
	if (!qs(`[data-filter-signal="${signal}"]`)) return;
  const requestID = (signalRequests.get(signal) || 0) + 1;
  signalRequests.set(signal, requestID);
  const params = filterParams(signal, true);
  const events = await request(`/api/events?${params}`);
  if (signalRequests.get(signal) !== requestID) return;
  const page = signalPages.get(signal) || 0;
  if (!events.length && page > 0) {
    signalPages.set(signal, page - 1);
    return refreshSignal(signal);
  }
  const hasNext = events.length > pageSize;
  const visible = events.slice(0, pageSize);
  // Events are immutable; IDs fully describe whether this page needs repainting.
  const signature = `${renderGeneration}:${params}:${hasNext}:${visible.map((event) => event.id).join(",")}`;
  if (signalSignatures.get(signal) === signature) return;
  signalSignatures.set(signal, signature);
  if (signal === "logs") renderLogs(visible);
  if (signal === "traces") renderTraces(visible);
  if (signal === "metrics") renderMetricRows(visible);
  renderPagination(signal, visible.length, hasNext);
}

function renderPagination(signal, count, hasNext) {
  const root = qs(`[data-pagination="${signal}"]`);
  if (!root) return;
  const page = signalPages.get(signal) || 0;
  const first = count ? page * pageSize + 1 : 0;
  const last = page * pageSize + count;
  qs("[data-page-summary]", root).textContent = count ? `Showing ${number.format(first)}–${number.format(last)} · ${pageSize} per page` : "No matching events";
  qs("[data-page-number]", root).textContent = `Page ${number.format(page + 1)}`;
  qs('[data-page-action="previous"]', root).disabled = page === 0;
  qs('[data-page-action="next"]', root).disabled = !hasNext;
}

function updateSelect(select, values, label) {
  if (!select) return;
  const current = select.value;
  const initial = document.createElement("option"); initial.value = ""; initial.textContent = label;
  select.replaceChildren(initial, ...values.map((value) => { const option = document.createElement("option"); option.value = value; option.textContent = value; return option; }));
  select.value = current;
}

function applyFacets(facets) {
  if (!qs("[data-filter-signal]")) return;
  for (const select of qsa('[data-filter="service"]')) updateSelect(select, facets.services || [], "All services");
  for (const root of qsa('[data-filter-signal="logs"]')) updateSelect(qs('[data-filter="severity"]', root), facets.severities || [], "All levels");
  for (const root of qsa('[data-filter-signal="traces"]')) updateSelect(qs('[data-filter="severity"]', root), ["OK", "ERROR"], "All statuses");
  for (const select of qsa('[data-filter="name"]')) updateSelect(select, facets.metric_names || [], "All metrics");
}

async function refreshFacets(force = false) {
  if (!qs("[data-filter-signal]")) return;
  if (facetsCache) applyFacets(facetsCache);
  if (!force && facetsCache && Date.now() - facetsFetchedAt < facetsTTL) return;
  if (!facetsRequest) {
    facetsRequest = request("/api/facets")
      .then((facets) => {
        facetsCache = facets;
        facetsFetchedAt = Date.now();
        return facets;
      })
      .finally(() => { facetsRequest = null; });
  }
  applyFacets(await facetsRequest);
}

async function refreshMetrics() {
  if (!qs('[data-filter-signal="metrics"]')) return;
  const params = filterParams("metrics");
  const group = qs('[data-filter-signal="metrics"] [data-filter="group"]')?.value || "";
  params.set("group_by", group); params.set("limit", "5000");
  const series = await request(`/api/metrics/series?${params}`);
  // Avoid serialising thousands of points on every live tick. Metric points are
  // append-only, so the count and final point identify a changed series.
  const signature = `${renderGeneration}:${params}:${series.map((item) => {
    const last = item.points?.at(-1);
    return `${item.key}/${item.points?.length || 0}/${last?.timestamp || ""}/${last?.value ?? ""}`;
  }).join(",")}`;
  if (signature === metricsSignature) return;
  metricsSignature = signature;
  renderChart(series); renderMetricCards(series);
}

function renderChart(series) {
  const host = qs("[data-metric-chart]");
  if (!host) return;
  const shown = series.filter((item) => item.points?.length).slice(0, palette.length);
  qs("[data-metric-series-count]").textContent = `${shown.length} ${shown.length === 1 ? "series" : "series"}`;
  if (!shown.length) { host.replaceChildren(Object.assign(document.createElement("div"), { className: `grid min-h-72 place-items-center ${muted}`, textContent: "Choose a metric or wait for points to arrive." })); qs("[data-metric-legend]").replaceChildren(); return; }
  const all = shown.flatMap((item) => item.points);
  let minX = Math.min(...all.map((p) => new Date(p.timestamp).getTime())), maxX = Math.max(...all.map((p) => new Date(p.timestamp).getTime()));
  let minY = Math.min(...all.map((p) => p.value)), maxY = Math.max(...all.map((p) => p.value));
  if (minX === maxX) { minX -= 1000; maxX += 1000; }
  if (minY === maxY) { minY -= Math.abs(minY || 1) * .1; maxY += Math.abs(maxY || 1) * .1; }
  const width = 900, height = 280, pad = { left: 64, right: 18, top: 18, bottom: 34 };
  const x = (v) => pad.left + ((new Date(v).getTime() - minX) / (maxX - minX)) * (width - pad.left - pad.right);
  const y = (v) => pad.top + (1 - (v - minY) / (maxY - minY)) * (height - pad.top - pad.bottom);
  const svg = document.createElementNS(svgNS, "svg"); svg.setAttribute("viewBox", `0 0 ${width} ${height}`); svg.setAttribute("class", "h-72 w-full overflow-visible"); svg.setAttribute("role", "img"); svg.setAttribute("aria-label", "Metric series chart");
  for (let i = 0; i < 5; i++) {
    const gy = pad.top + i * (height - pad.top - pad.bottom) / 4;
    const line = document.createElementNS(svgNS, "line"); for (const [key, value] of Object.entries({ x1: pad.left, x2: width - pad.right, y1: gy, y2: gy })) line.setAttribute(key, value); line.setAttribute("stroke", "currentColor"); line.setAttribute("class", "text-border"); svg.appendChild(line);
    const label = document.createElementNS(svgNS, "text"); label.setAttribute("x", pad.left - 10); label.setAttribute("y", gy + 4); label.setAttribute("text-anchor", "end"); label.setAttribute("class", "fill-muted-foreground text-[11px]"); label.textContent = number.format(maxY - i * (maxY - minY) / 4); svg.appendChild(label);
  }
  shown.forEach((item, index) => {
    const polyline = document.createElementNS(svgNS, "polyline"); polyline.setAttribute("fill", "none"); polyline.setAttribute("stroke", palette[index]); polyline.setAttribute("stroke-width", "2.5"); polyline.setAttribute("stroke-linejoin", "round"); polyline.setAttribute("stroke-linecap", "round"); polyline.setAttribute("points", item.points.map((p) => `${x(p.timestamp)},${y(p.value)}`).join(" ")); svg.appendChild(polyline);
    for (const point of item.points) { const circle = document.createElementNS(svgNS, "circle"); circle.setAttribute("cx", x(point.timestamp)); circle.setAttribute("cy", y(point.value)); circle.setAttribute("r", "3"); circle.setAttribute("fill", palette[index]); svg.appendChild(circle); }
  });
  const start = document.createElementNS(svgNS, "text"); start.setAttribute("x", pad.left); start.setAttribute("y", height - 8); start.setAttribute("class", "fill-muted-foreground text-[11px]"); start.textContent = new Date(minX).toLocaleTimeString(); svg.appendChild(start);
  const end = document.createElementNS(svgNS, "text"); end.setAttribute("x", width - pad.right); end.setAttribute("y", height - 8); end.setAttribute("text-anchor", "end"); end.setAttribute("class", "fill-muted-foreground text-[11px]"); end.textContent = new Date(maxX).toLocaleTimeString(); svg.appendChild(end);
  host.replaceChildren(svg);
  const legend = qs("[data-metric-legend]"); legend.replaceChildren(...shown.map((item, index) => { const node = document.createElement("span"); node.className = `inline-flex items-center gap-2 text-xs ${muted}`; const dot = document.createElement("span"); dot.className = "size-2 rounded-full"; dot.style.background = palette[index]; node.append(dot, text(item.key)); return node; }));
}

function renderMetricCards(series) {
  const host = qs("[data-metric-summary]"); if (!host) return;
  host.replaceChildren(...series.slice(0, 12).map((item) => {
    const values = item.points.map((p) => p.value); const latest = values.at(-1) ?? 0; const avg = values.reduce((a, b) => a + b, 0) / Math.max(values.length, 1);
    const card = document.createElement("article"); card.className = "rounded-xl bg-card p-4 text-card-foreground ring-1 ring-foreground/10";
    const title = document.createElement("p"); title.className = `truncate font-mono text-xs ${muted}`; title.appendChild(text(item.key));
    const value = document.createElement("p"); value.className = "mt-3 text-2xl font-semibold tabular-nums"; value.appendChild(text(`${number.format(latest)}${item.unit ? ` ${item.unit}` : ""}`));
    const meta = document.createElement("p"); meta.className = `mt-2 text-xs ${muted}`; meta.appendChild(text(`${item.type || "number"} · avg ${number.format(avg)} · min ${number.format(Math.min(...values))} · max ${number.format(Math.max(...values))} · ${values.length} points`));
    card.append(title, value, meta); return card;
  }));
}

function detailField(label, value) {
  const node = document.createElement("div"); node.className = "min-w-0 bg-card p-3";
  const key = document.createElement("p"); key.className = `text-[.65rem] font-semibold uppercase tracking-[.15em] ${muted}`; key.appendChild(text(label));
  const content = document.createElement("p"); content.className = "mt-1 break-words font-mono text-xs"; content.appendChild(text(value || "—"));
  node.append(key, content); return node;
}

async function openDetail(id) {
  const event = await request(`/api/events/${id}`);
  qs("[data-detail-eyebrow]").textContent = `${event.signal.replace(/s$/, "")} detail`;
  qs("[data-detail-title]").textContent = eventText(event);
  // 1px of the border colour shows between the cells as the grid gap.
  const grid = document.createElement("div"); grid.className = "grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-border bg-border";
  const fields = [["Timestamp", new Date(event.timestamp).toLocaleString()], ["Received", new Date(event.received_at).toLocaleString()], ["Service", event.service], ["Instance", event.instance], ["Scope", event.scope], ["Severity / status", event.severity], ["Span kind", event.kind], ["Duration", event.duration_ms ? `${event.duration_ms} ms` : ""], ["Metric type", event.metric_type], ["Value", event.value !== undefined ? `${event.value}${event.unit ? ` ${event.unit}` : ""}` : ""], ["Trace ID", event.trace_id], ["Span ID", event.span_id], ["Parent span", event.parent_span_id]];
  grid.append(...fields.filter(([, value]) => value !== "" && value !== undefined).map(([label, value]) => detailField(label, value)));
  const attrs = document.createElement("section"); const heading = document.createElement("h3"); heading.className = "mb-3 font-medium"; heading.appendChild(text(`Attributes · ${Object.keys(event.attributes || {}).length}`));
  const pre = document.createElement("pre"); pre.className = "overflow-x-auto rounded-xl bg-code p-4 font-mono text-xs leading-6 text-code-foreground"; pre.appendChild(text(JSON.stringify(event.attributes || {}, null, 2))); attrs.append(heading, pre);
  qs("[data-detail-content]").replaceChildren(grid, attrs); qs("[data-detail-shell]").classList.add("open"); document.body.classList.add("overflow-hidden");
  // Arriving from a drill-down leaves a list to go back to; arriving from a
  // table does not, and an inert button would be worse than none.
  qs("[data-detail-footer]").hidden = !lastDrill;
  if (event.trace_id) showTrace(event.trace_id).catch(() => {});
}

// Opening a span opens its whole trace above the table.
//
// A span list answers "what happened"; only the timeline answers "what was
// waiting on what", which is the question a slow request actually raises. The
// card is on the traces page only — the same renderer serves the waterfall
// panel on /views, from the same endpoint.
async function showTrace(traceID) {
  const card = qs("[data-trace-card]");
  if (!card) return;
  const trace = await request(`/api/traces/${encodeURIComponent(traceID)}`);
  qs("[data-trace-id]", card).textContent = trace.trace_id;
  qs("[data-trace-duration]", card).textContent = `${number.format(Math.round(trace.duration_ms))} ms · ${trace.spans.length} spans`;
  qs("[data-trace-services]", card).textContent = `${trace.services.join(" · ")}${trace.errors ? ` · ${trace.errors} errored` : ""}`;
  drawWaterfall(qs("[data-waterfall]", card), trace, { onSpan: (id) => openDetail(id).catch(() => {}) });
  card.hidden = false;
}

function closeDetail() {
  qs("[data-detail-shell]")?.classList.remove("open");
  document.body.classList.remove("overflow-hidden");
  lastDrill = null;
  qs("[data-detail-footer]").hidden = true;
}

// The drawer's second mode: the events behind one mark on a chart.
//
// A bar is an aggregate — 217 events, not one — so clicking it cannot open
// "the" event. It opens the list, and a row in that list opens the event. The
// list is remembered so the detail view has somewhere to go back to.
let lastDrill = null;

async function openDrill(request, datum = {}) {
  const drill = await request_("/api/views/drill", request);
  lastDrill = { request, datum, drill };
  renderDrill();
  qs("[data-detail-shell]").classList.add("open");
  document.body.classList.add("overflow-hidden");
}

function request_(path, body) {
  return request(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
}

function renderDrill() {
  if (!lastDrill) return;
  const { datum, drill } = lastDrill;
  const shown = drill.events.length;
  qs("[data-detail-eyebrow]").textContent = shown === drill.total
    ? `${number.format(drill.total)} ${drill.total === 1 ? "event" : "events"}`
    : `${number.format(shown)} of ${number.format(drill.total)} events`;
  qs("[data-detail-title]").textContent = datum.title || "Selected events";
  qs("[data-detail-footer]").hidden = true;

  const template = qs("[data-drill-row-template]");
  const list = document.createElement("div");
  list.className = "-mt-2";
  if (!shown) {
    list.appendChild(Object.assign(document.createElement("p"), {
      className: `py-8 text-center text-sm ${muted}`,
      textContent: "No events behind this mark.",
    }));
  }
  for (const event of drill.events) {
    const row = template.content.firstElementChild.cloneNode(true);
    row.dataset.eventId = event.id;
    qs("[data-drill-time]", row).textContent = timeText(event.timestamp);
    const badge = qs("[data-drill-badge]", row);
    const label = event.signal === "logs" ? (event.severity || "log").toLowerCase() : event.signal.replace(/s$/, "");
    badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${
      event.signal === "traces" ? tones.trace : event.signal === "metrics" ? tones.metric
        : /error|fatal/i.test(event.severity || "") ? tones.error : tones.neutral}`;
    badge.textContent = label;
    qs("[data-drill-text]", row).textContent = `${event.service} · ${eventText(event)}`;
    qs("[data-drill-value]", row).textContent = event.duration_ms
      ? `${number.format(Math.round(event.duration_ms))} ms`
      : event.value !== undefined ? number.format(event.value) : "";
    list.appendChild(row);
  }
  const content = qs("[data-detail-content]");
  content.replaceChildren(list);
  content.scrollTop = 0;
}

// views.js opens the drawer when a chart is clicked — one drawer per document,
// owned here, reachable from there. Both entry points are published rather than
// imported, because guard.js imports views.js and the reverse would be a cycle.
globalThis.guardOpenDetail = (id) => openDetail(id).catch(() => {});
globalThis.guardOpenDrill = (request, datum) => openDrill(request, datum).catch((failure) => {
  qs("[data-detail-eyebrow]").textContent = "Could not read these events";
  qs("[data-detail-title]").textContent = failure.message;
  qs("[data-detail-content]").replaceChildren();
  qs("[data-detail-shell]").classList.add("open");
});

async function loadSettings() {
  const form = qs("[data-settings-form]"); if (!form) return;
	if (form.dataset.loaded === "true") return;
  const settings = await request("/api/settings");
  for (const key of ["database_path", "retention_hours", "max_events"]) qs(`[data-setting="${key}"]`, form).value = settings[key] ?? "";
  qs('[data-setting="token"]', form).value = sessionStorage.getItem("guard.token") || "";
	form.dataset.loaded = "true";
}

async function saveSettings(form) {
  const status = qs("[data-settings-status]", form); status.textContent = "Saving…";
  try {
    const value = { retention_hours: Number(qs('[data-setting="retention_hours"]', form).value), max_events: Number(qs('[data-setting="max_events"]', form).value) };
    await request("/api/settings", { method: "PUT", headers: adminHeaders(), body: JSON.stringify(value) }); status.textContent = "Saved and cleanup applied."; await refreshSummary();
  } catch (error) { status.textContent = error.message; }
}

async function purgeNow() {
  const status = qs("[data-settings-status]"); status.textContent = "Cleaning…";
  try { const value = await request("/api/settings/purge", { method: "POST", headers: adminHeaders() }); status.textContent = `Cleanup complete · ${number.format(value.removed)} removed.`; await refreshSummary(); }
  catch (error) { status.textContent = error.message; }
}

async function refreshPage({ facets = false } = {}) {
  const work = [refreshSignal("logs"), refreshSignal("traces"), refreshSignal("metrics"), refreshMetrics(), loadSettings()];
  if (qs("[data-stat]") || qs("[data-instance-list]")) work.push(refreshSummary());
  if (qs("[data-view-grid]")) work.push(refreshViews());
  if (facets) work.push(refreshFacets());
  await Promise.allSettled(work);
}

function updatePageTitle() {
  const label = document.title.replace(/\s+[—-]\s+Guard$/, "");
  for (const node of qsa("[data-page-title]")) node.textContent = label;
}

async function liveTick() {
  if (live && !refreshInFlight && document.visibilityState === "visible" && Date.now() - lastInteraction > 500) {
    refreshInFlight = true;
    try { await refreshPage(); } finally { refreshInFlight = false; }
  }
  liveTimer = setTimeout(liveTick, 3000);
}

globalThis.guardPageMount = (page) => {
  // The cold document can start fetching as soon as guard.js executes, without
  // waiting for the WASM binary. Its later Mount consumes that same promise;
  // AOT navigations start a fresh page-specific load here.
  updatePageTitle();
  renderGeneration++;
  if (page === initializedPage && initialRefresh) {
    const pending = initialRefresh;
    initialRefresh = null;
    return pending;
  }
  initializedPage = page;
  if (page === "views") return mountViews();
  return refreshPage({ facets: true });
};
globalThis.guardPageUnmount = () => {
  clearTimeout(filterTimer);
  filterTimer = undefined;
  for (const signal of signalRequests.keys()) signalRequests.set(signal, signalRequests.get(signal) + 1);
  unmountViews();
};

for (const eventName of ["scroll", "wheel", "touchmove", "pointermove"]) {
  document.addEventListener(eventName, () => { lastInteraction = Date.now(); }, { passive: true });
}

document.addEventListener("click", (event) => {
  // The sidebar is a CSS-only drawer below lg; navigating has to close it,
  // because on those widths nothing else will.
  if (event.target.closest("[data-nav-link]")) { const drawer = qs("#nav-drawer"); if (drawer) drawer.checked = false; }
  const toggle = event.target.closest("[data-live-toggle]");
  if (toggle) { live = !live; localStorage.setItem("guard.live", live ? "on" : "off"); updateLiveControl(); if (live) refreshPage(); return; }
  if (event.target.closest("[data-detail-back]")) { renderDrill(); return; }
  if (event.target.closest("[data-detail-close]")) { closeDetail(); return; }
  if (event.target.closest("[data-trace-close]")) { qs("[data-trace-card]").hidden = true; return; }
  const pageButton = event.target.closest("[data-page-action]");
  if (pageButton && !pageButton.disabled) {
    const signal = pageButton.closest("[data-pagination]").dataset.pagination;
    const direction = pageButton.dataset.pageAction === "next" ? 1 : -1;
    signalPages.set(signal, Math.max(0, (signalPages.get(signal) || 0) + direction));
    refreshSignal(signal).catch(() => {});
    return;
  }
  const row = event.target.closest("[data-event-id]"); if (row) { openDetail(row.dataset.eventId).catch(() => {}); return; }
  if (event.target.closest("[data-refresh-now]")) { refreshPage({ facets: true }); return; }
  if (event.target.closest("[data-purge-now]")) purgeNow();
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeDetail();
  if ((event.key === "Enter" || event.key === " ") && event.target.matches("[data-event-id]")) { event.preventDefault(); openDetail(event.target.dataset.eventId).catch(() => {}); }
});

document.addEventListener("input", (event) => {
  if (!event.target.matches("[data-filter]")) return;
  const signal = event.target.closest("[data-filter-signal]")?.dataset.filterSignal;
  if (signal) signalPages.set(signal, 0);
  clearTimeout(filterTimer); filterTimer = setTimeout(refreshPage, 180);
});

document.addEventListener("change", (event) => {
  if (event.target.matches('[data-filter="range"]')) qs("[data-custom-range]", event.target.closest("[data-filter-signal]"))?.classList.toggle("hidden", event.target.value !== "custom");
  if (event.target.matches("[data-filter]")) {
    const signal = event.target.closest("[data-filter-signal]")?.dataset.filterSignal;
    if (signal) signalPages.set(signal, 0);
    refreshPage();
  }
});

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-settings-form]")) return;
  event.preventDefault(); saveSettings(event.target);
});

updateLiveControl();
initializedPage = location.pathname === "/" ? "home" : location.pathname.split("/").filter(Boolean)[0];
initialRefresh = refreshPage({ facets: true });
liveTimer = setTimeout(liveTick, 3000);
