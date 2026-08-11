const number = new Intl.NumberFormat();
const palette = ["#5bd8a6", "#60a5fa", "#c084fc", "#f59e0b", "#fb7185", "#22d3ee", "#a3e635", "#f472b6"];
const svgNS = "http://www.w3.org/2000/svg";
let live = localStorage.getItem("guard.live") !== "off";
let filterTimer;

const text = (value) => document.createTextNode(value ?? "");
const qs = (selector, root = document) => root.querySelector(selector);
const qsa = (selector, root = document) => [...root.querySelectorAll(selector)];

async function request(path, options = {}) {
  const response = await fetch(path, { headers: { Accept: "application/json", ...(options.headers || {}) }, ...options });
  if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
  if (response.status === 204) return null;
  return response.json();
}

function setStat(name, value) {
  for (const node of qsa(`[data-stat="${name}"]`)) node.textContent = number.format(value ?? 0);
}

function updateLiveControl(status = live ? "receiving" : "paused") {
  for (const node of qsa("[data-live-status]")) node.textContent = status;
  for (const node of qsa("[data-live-action]")) node.textContent = live ? "Pause" : "Resume";
  for (const node of qsa("[data-live-toggle]")) node.setAttribute("aria-label", live ? "Pause live refresh" : "Resume live refresh");
  for (const node of qsa("[data-live-dot]")) node.classList.toggle("opacity-30", !live);
}

function td(value, className = "") {
  const cell = document.createElement("td");
  cell.className = className;
  cell.appendChild(text(value));
  return cell;
}

function eventRow(event) {
  const row = document.createElement("tr");
  row.className = "event-row cursor-pointer";
  row.tabIndex = 0;
  row.dataset.eventId = event.id;
  return row;
}

function shortID(value) { return value ? `${value.slice(0, 12)}${value.length > 12 ? "…" : ""}` : "—"; }
function eventText(event) { return event.message || (event.value !== undefined ? `${event.name} = ${event.value}${event.unit ? ` ${event.unit}` : ""}` : event.name) || "telemetry event"; }
function timeText(value) { return new Date(value).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3 }); }

function badge(value, tone = "badge-ghost") {
  const node = document.createElement("span");
  node.className = `badge badge-sm ${tone}`;
  node.appendChild(text(value));
  return node;
}

function emptyRow(body, columns, message) {
  const row = document.createElement("tr");
  const cell = td(message, "py-12 text-center text-base-content/40");
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
    severity.appendChild(badge(event.severity || "INFO", /error|fatal/i.test(event.severity || "") ? "badge-error" : /warn/i.test(event.severity || "") ? "badge-warning" : "badge-ghost"));
    row.append(td(timeText(event.timestamp), "whitespace-nowrap font-mono text-xs text-base-content/50"), severity,
      td(event.service, "font-medium"), td(event.message || "—", "max-w-xl truncate"), td(shortID(event.trace_id), "font-mono text-[.65rem] text-base-content/40"));
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
    status.appendChild(badge((event.severity || "OK").toLowerCase(), /error/i.test(event.severity || "") ? "badge-error" : "badge-success"));
    row.append(td(timeText(event.timestamp), "whitespace-nowrap font-mono text-xs text-base-content/50"), status,
      td(event.service, "font-medium"), td(event.name || "unnamed span", "max-w-sm truncate"), td((event.kind || "internal").toLowerCase(), "text-xs text-base-content/60"),
      td(`${number.format(event.duration_ms || 0)} ms`, "whitespace-nowrap font-mono text-xs"), td(shortID(event.trace_id), "font-mono text-[.65rem] text-base-content/40"));
    return row;
  }));
}

function renderMetricRows(events) {
  const body = qs("[data-metric-rows]");
  if (!body) return;
  if (!events.length) return emptyRow(body, 6, "No metric points match this view.");
  body.replaceChildren(...events.map((event) => {
    const row = eventRow(event);
    row.append(td(timeText(event.timestamp), "whitespace-nowrap font-mono text-xs text-base-content/50"), td(event.name, "font-mono text-xs"),
      td(event.service, "font-medium"), td(event.metric_type || "number", "text-xs text-base-content/60"),
      td(`${number.format(event.value ?? 0)}${event.unit ? ` ${event.unit}` : ""}`, "whitespace-nowrap font-mono text-xs"),
      td(`${Object.keys(event.attributes || {}).length} fields`, "text-xs text-base-content/45"));
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
    const label = event.signal === "logs" ? (event.severity || "log").toLowerCase() : event.signal.replace(/s$/, "");
    const tone = event.signal === "traces" ? "badge-secondary" : event.signal === "metrics" ? "badge-info" : /error|fatal/i.test(event.severity || "") ? "badge-error" : "badge-ghost";
    signal.appendChild(badge(label, tone));
    row.append(td(timeText(event.timestamp), "font-mono text-xs text-base-content/50"), signal, td(event.service), td(eventText(event), "max-w-lg truncate"), td(shortID(event.trace_id), "font-mono text-[.65rem] text-base-content/40"));
    return row;
  }));
}

function renderInstances(instances) {
  const list = qs("[data-instance-list]");
  if (!list) return;
  if (!instances.length) { list.replaceChildren(); return; }
  list.replaceChildren(...instances.map((instance) => {
    const row = document.createElement("div");
    row.className = "flex items-center gap-3 rounded-xl bg-base-100/55 p-3";
    const dot = document.createElement("span"); dot.className = "signal-dot";
    const names = document.createElement("div"); names.className = "min-w-0 flex-1";
    const service = document.createElement("p"); service.className = "truncate text-sm font-medium"; service.appendChild(text(instance.service));
    const id = document.createElement("p"); id.className = "truncate font-mono text-[.65rem] text-base-content/40"; id.appendChild(text(instance.instance || "default"));
    names.append(service, id);
    const seen = document.createElement("span"); seen.className = "text-xs text-base-content/40"; seen.appendChild(text(relativeTime(instance.last_seen)));
    row.append(dot, names, seen); return row;
  }));
}

function relativeTime(value) {
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  return `${Math.floor(seconds / 3600)}h ago`;
}

function filterParams(signal) {
  const root = qs(`[data-filter-signal="${signal}"]`);
  const params = new URLSearchParams({ signal, limit: signal === "metrics" ? "500" : "250" });
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
    setStat("logs", data.logs); setStat("errors", data.errors); setStat("spans", data.spans); setStat("metrics", data.metrics); setStat("instances", data.instances?.length);
    renderInstances(data.instances || []); renderOverview(data.recent || []);
    updateLiveControl();
  } catch { updateLiveControl("reconnecting"); }
}

async function refreshSignal(signal) {
	if (!qs(`[data-filter-signal="${signal}"]`)) return;
  const params = filterParams(signal);
  const events = await request(`/api/events?${params}`);
  if (signal === "logs") renderLogs(events);
  if (signal === "traces") renderTraces(events);
  if (signal === "metrics") renderMetricRows(events);
}

function updateSelect(select, values, label) {
  if (!select) return;
  const current = select.value;
  const initial = document.createElement("option"); initial.value = ""; initial.textContent = label;
  select.replaceChildren(initial, ...values.map((value) => { const option = document.createElement("option"); option.value = value; option.textContent = value; return option; }));
  select.value = current;
}

async function refreshFacets() {
  if (!qs("[data-filter-signal]")) return;
  const facets = await request("/api/facets");
  for (const select of qsa('[data-filter="service"]')) updateSelect(select, facets.services || [], "All services");
  for (const root of qsa('[data-filter-signal="logs"]')) updateSelect(qs('[data-filter="severity"]', root), facets.severities || [], "All levels");
  for (const root of qsa('[data-filter-signal="traces"]')) updateSelect(qs('[data-filter="severity"]', root), ["OK", "ERROR"], "All statuses");
  for (const select of qsa('[data-filter="name"]')) updateSelect(select, facets.metric_names || [], "All metrics");
}

async function refreshMetrics() {
  if (!qs('[data-filter-signal="metrics"]')) return;
  const params = filterParams("metrics");
  const group = qs('[data-filter-signal="metrics"] [data-filter="group"]')?.value || "";
  params.set("group_by", group); params.set("limit", "5000");
  const series = await request(`/api/metrics/series?${params}`);
  renderChart(series); renderMetricCards(series);
}

function renderChart(series) {
  const host = qs("[data-metric-chart]");
  if (!host) return;
  const shown = series.filter((item) => item.points?.length).slice(0, palette.length);
  qs("[data-metric-series-count]").textContent = `${shown.length} ${shown.length === 1 ? "series" : "series"}`;
  if (!shown.length) { host.replaceChildren(Object.assign(document.createElement("div"), { className: "grid min-h-72 place-items-center text-base-content/40", textContent: "Choose a metric or wait for points to arrive." })); qs("[data-metric-legend]").replaceChildren(); return; }
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
    const line = document.createElementNS(svgNS, "line"); for (const [key, value] of Object.entries({ x1: pad.left, x2: width - pad.right, y1: gy, y2: gy })) line.setAttribute(key, value); line.setAttribute("stroke", "currentColor"); line.setAttribute("class", "text-base-content/10"); svg.appendChild(line);
    const label = document.createElementNS(svgNS, "text"); label.setAttribute("x", pad.left - 10); label.setAttribute("y", gy + 4); label.setAttribute("text-anchor", "end"); label.setAttribute("class", "fill-base-content/45 text-[11px]"); label.textContent = number.format(maxY - i * (maxY - minY) / 4); svg.appendChild(label);
  }
  shown.forEach((item, index) => {
    const polyline = document.createElementNS(svgNS, "polyline"); polyline.setAttribute("fill", "none"); polyline.setAttribute("stroke", palette[index]); polyline.setAttribute("stroke-width", "2.5"); polyline.setAttribute("stroke-linejoin", "round"); polyline.setAttribute("stroke-linecap", "round"); polyline.setAttribute("points", item.points.map((p) => `${x(p.timestamp)},${y(p.value)}`).join(" ")); svg.appendChild(polyline);
    for (const point of item.points) { const circle = document.createElementNS(svgNS, "circle"); circle.setAttribute("cx", x(point.timestamp)); circle.setAttribute("cy", y(point.value)); circle.setAttribute("r", "3"); circle.setAttribute("fill", palette[index]); svg.appendChild(circle); }
  });
  const start = document.createElementNS(svgNS, "text"); start.setAttribute("x", pad.left); start.setAttribute("y", height - 8); start.setAttribute("class", "fill-base-content/45 text-[11px]"); start.textContent = new Date(minX).toLocaleTimeString(); svg.appendChild(start);
  const end = document.createElementNS(svgNS, "text"); end.setAttribute("x", width - pad.right); end.setAttribute("y", height - 8); end.setAttribute("text-anchor", "end"); end.setAttribute("class", "fill-base-content/45 text-[11px]"); end.textContent = new Date(maxX).toLocaleTimeString(); svg.appendChild(end);
  host.replaceChildren(svg);
  const legend = qs("[data-metric-legend]"); legend.replaceChildren(...shown.map((item, index) => { const node = document.createElement("span"); node.className = "inline-flex items-center gap-2 text-xs text-base-content/60"; const dot = document.createElement("span"); dot.className = "size-2 rounded-full"; dot.style.background = palette[index]; node.append(dot, text(item.key)); return node; }));
}

function renderMetricCards(series) {
  const host = qs("[data-metric-summary]"); if (!host) return;
  host.replaceChildren(...series.slice(0, 12).map((item) => {
    const values = item.points.map((p) => p.value); const latest = values.at(-1) ?? 0; const avg = values.reduce((a, b) => a + b, 0) / Math.max(values.length, 1);
    const card = document.createElement("article"); card.className = "metric-card";
    const title = document.createElement("p"); title.className = "truncate font-mono text-xs text-base-content/55"; title.appendChild(text(item.key));
    const value = document.createElement("p"); value.className = "mt-3 text-2xl font-bold tabular-nums"; value.appendChild(text(`${number.format(latest)}${item.unit ? ` ${item.unit}` : ""}`));
    const meta = document.createElement("p"); meta.className = "mt-2 text-xs text-base-content/45"; meta.appendChild(text(`${item.type || "number"} · avg ${number.format(avg)} · min ${number.format(Math.min(...values))} · max ${number.format(Math.max(...values))} · ${values.length} points`));
    card.append(title, value, meta); return card;
  }));
}

function detailField(label, value) {
  const node = document.createElement("div"); node.className = "detail-field";
  const key = document.createElement("p"); key.className = "detail-label"; key.appendChild(text(label));
  const content = document.createElement("p"); content.className = "detail-value"; content.appendChild(text(value || "—"));
  node.append(key, content); return node;
}

async function openDetail(id) {
  const event = await request(`/api/events/${id}`);
  qs("[data-detail-eyebrow]").textContent = `${event.signal.replace(/s$/, "")} detail`;
  qs("[data-detail-title]").textContent = eventText(event);
  const grid = document.createElement("div"); grid.className = "detail-grid";
  const fields = [["Timestamp", new Date(event.timestamp).toLocaleString()], ["Received", new Date(event.received_at).toLocaleString()], ["Service", event.service], ["Instance", event.instance], ["Scope", event.scope], ["Severity / status", event.severity], ["Span kind", event.kind], ["Duration", event.duration_ms ? `${event.duration_ms} ms` : ""], ["Metric type", event.metric_type], ["Value", event.value !== undefined ? `${event.value}${event.unit ? ` ${event.unit}` : ""}` : ""], ["Trace ID", event.trace_id], ["Span ID", event.span_id], ["Parent span", event.parent_span_id]];
  grid.append(...fields.filter(([, value]) => value !== "" && value !== undefined).map(([label, value]) => detailField(label, value)));
  const attrs = document.createElement("section"); const heading = document.createElement("h3"); heading.className = "mb-3 font-semibold"; heading.appendChild(text(`Attributes · ${Object.keys(event.attributes || {}).length}`));
  const pre = document.createElement("pre"); pre.className = "overflow-x-auto rounded-xl bg-base-200 p-4 font-mono text-xs leading-6 text-base-content/70"; pre.appendChild(text(JSON.stringify(event.attributes || {}, null, 2))); attrs.append(heading, pre);
  qs("[data-detail-content]").replaceChildren(grid, attrs); qs("[data-detail-shell]").classList.add("open"); document.body.classList.add("overflow-hidden");
}

function closeDetail() { qs("[data-detail-shell]")?.classList.remove("open"); document.body.classList.remove("overflow-hidden"); }

async function loadSettings() {
  const form = qs("[data-settings-form]"); if (!form) return;
	if (form.dataset.loaded === "true") return;
  const settings = await request("/api/settings");
  for (const key of ["database_path", "retention_hours", "max_events"]) qs(`[data-setting="${key}"]`, form).value = settings[key] ?? "";
  qs('[data-setting="token"]', form).value = sessionStorage.getItem("guard.token") || "";
	form.dataset.loaded = "true";
}

function adminHeaders() {
  const token = qs('[data-setting="token"]')?.value || sessionStorage.getItem("guard.token") || "";
  if (token) sessionStorage.setItem("guard.token", token);
  return { "Content-Type": "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}) };
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

async function refreshPage() {
  await Promise.allSettled([refreshSummary(), refreshFacets(), refreshSignal("logs"), refreshSignal("traces"), refreshSignal("metrics"), refreshMetrics(), loadSettings()]);
}

document.addEventListener("click", (event) => {
  const toggle = event.target.closest("[data-live-toggle]");
  if (toggle) { live = !live; localStorage.setItem("guard.live", live ? "on" : "off"); updateLiveControl(); if (live) refreshPage(); return; }
  if (event.target.closest("[data-detail-close]")) { closeDetail(); return; }
  const row = event.target.closest("[data-event-id]"); if (row) { openDetail(row.dataset.eventId).catch(() => {}); return; }
  if (event.target.closest("[data-refresh-now]")) { refreshPage(); return; }
  if (event.target.closest("[data-purge-now]")) purgeNow();
});

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeDetail();
  if ((event.key === "Enter" || event.key === " ") && event.target.matches("[data-event-id]")) { event.preventDefault(); openDetail(event.target.dataset.eventId).catch(() => {}); }
});

document.addEventListener("input", (event) => {
  if (!event.target.matches("[data-filter]")) return;
  clearTimeout(filterTimer); filterTimer = setTimeout(refreshPage, 180);
});

document.addEventListener("change", (event) => {
  if (event.target.matches('[data-filter="range"]')) qs("[data-custom-range]", event.target.closest("[data-filter-signal]"))?.classList.toggle("hidden", event.target.value !== "custom");
  if (event.target.matches("[data-filter]")) refreshPage();
});

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-settings-form]")) return;
  event.preventDefault(); saveSettings(event.target);
});

updateLiveControl();
refreshPage();
setInterval(() => { if (live) refreshPage(); }, 3000);
