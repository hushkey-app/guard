const fmt = new Intl.NumberFormat();
const set = (name, value) => {
  for (const node of document.querySelectorAll(`[data-stat="${name}"]`)) node.textContent = fmt.format(value ?? 0);
};

async function refreshSummary() {
  try {
    const response = await fetch("/api/summary", { headers: { Accept: "application/json" } });
    if (!response.ok) throw new Error(response.statusText);
    const data = await response.json();
    set("logs", data.logs);
    set("errors", data.errors);
    set("spans", data.spans);
    set("metrics", data.metrics);
    set("instances", data.instances?.length);
    renderInstances(data.instances || []);
    for (const node of document.querySelectorAll("[data-live-status]")) node.textContent = "receiving";
  } catch {
    for (const node of document.querySelectorAll("[data-live-status]")) node.textContent = "reconnecting";
  }
}

const escapeText = (value) => document.createTextNode(value ?? "");

function renderLogs(events) {
  const body = document.querySelector("[data-log-rows]");
  if (!body) return;
  body.replaceChildren(...events.map((event) => {
    const row = document.createElement("tr");
    row.className = "event-row";
    const values = [
      new Date(event.timestamp).toLocaleTimeString(),
      event.severity || "INFO",
      event.service,
      event.message || event.name || "—",
    ];
    for (const [index, value] of values.entries()) {
      const cell = document.createElement("td");
      cell.className = index === 3 ? "max-w-xl truncate" : index < 3 ? "font-mono text-xs" : "";
      cell.appendChild(escapeText(value));
      row.appendChild(cell);
    }
    return row;
  }));
}

function td(value, className = "") {
  const cell = document.createElement("td");
  cell.className = className;
  cell.appendChild(escapeText(value));
  return cell;
}

function labelFor(event) {
  return event.signal === "logs" && event.severity ? event.severity.toLowerCase() : event.signal.replace(/s$/, "");
}

function textFor(event) {
  if (event.message) return event.message;
  if (event.value !== undefined) return `${event.name} = ${event.value}`;
  return event.name || "telemetry event";
}

function renderEventRows(body, events) {
  if (events.length) document.querySelector("[data-recent-empty]")?.remove();
  body.replaceChildren(...events.map((event) => {
    const row = document.createElement("tr");
    row.className = "event-row";
    const badge = document.createElement("span");
    badge.className = `badge badge-sm ${event.signal === "traces" ? "badge-secondary" : event.signal === "metrics" ? "badge-info" : /error|fatal/i.test(event.severity || "") ? "badge-error" : "badge-ghost"}`;
    badge.appendChild(escapeText(labelFor(event)));
    const signal = document.createElement("td");
    signal.appendChild(badge);
    const service = document.createElement("td");
    service.appendChild(escapeText(event.service));
    row.append(
      td(new Date(event.timestamp).toLocaleTimeString(), "whitespace-nowrap font-mono text-xs text-base-content/50"),
      signal,
      service,
      td(textFor(event), "max-w-lg truncate"),
      td(event.trace_id ? `${event.trace_id.slice(0, 12)}…` : "", "font-mono text-[.65rem] text-base-content/40"),
    );
    return row;
  }));
}

function renderInstances(instances) {
  const list = document.querySelector("[data-instance-list]");
  if (!list || !instances.length) return;
  list.replaceChildren(...instances.map((instance) => {
    const row = document.createElement("div");
    row.className = "flex items-center gap-3 rounded-xl bg-base-100/55 p-3";
    const dot = document.createElement("span");
    dot.className = "signal-dot";
    const names = document.createElement("div");
    names.className = "min-w-0 flex-1";
    const service = document.createElement("p");
    service.className = "truncate text-sm font-medium";
    service.appendChild(escapeText(instance.service));
    const id = document.createElement("p");
    id.className = "truncate font-mono text-[.65rem] text-base-content/40";
    id.appendChild(escapeText(instance.instance || "default"));
    names.append(service, id);
    const seen = document.createElement("span");
    seen.className = "text-xs text-base-content/40";
    seen.appendChild(escapeText("just now"));
    row.append(dot, names, seen);
    return row;
  }));
}

async function refreshEventTables() {
  for (const body of document.querySelectorAll("[data-event-rows]")) {
    const signal = body.dataset.signal || "";
    try {
      const response = await fetch(`/api/events?limit=100&signal=${encodeURIComponent(signal)}`);
      if (response.ok) renderEventRows(body, await response.json());
    } catch { /* summary status communicates connectivity */ }
  }
}

async function refreshLogs() {
  const body = document.querySelector("[data-log-rows]");
  if (!body) return;
  const query = document.querySelector("[data-log-query]")?.value || "";
  try {
    const response = await fetch(`/api/logs?limit=100&q=${encodeURIComponent(query)}`);
    if (response.ok) renderLogs(await response.json());
  } catch { /* summary status communicates connectivity */ }
}

let searchTimer;
document.addEventListener("input", (event) => {
  if (!event.target.matches("[data-log-query]")) return;
  clearTimeout(searchTimer);
  searchTimer = setTimeout(refreshLogs, 150);
});

refreshSummary();
refreshLogs();
refreshEventTables();
setInterval(refreshSummary, 3000);
setInterval(refreshLogs, 3000);
setInterval(refreshEventTables, 3000);
