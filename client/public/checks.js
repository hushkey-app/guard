// Service health, shared by /checks and the overview Status widget.
//
// A machine is optional context, never the target. The URL is what Guard
// checks; machine names are loaded only when a check is actually attached or
// the editor is open, so unattached services never grow cluster furniture.
import { adminHeaders, el, number, qs, qsa, relativeTime, request, text } from "./core.js";

const colours = {
  up: "var(--success)",
  down: "var(--destructive)",
  unknown: "var(--muted-foreground)",
  paused: "var(--warning)",
};

let checks = [];
let inFlight = null;
let machines = [];
let machinesRequest = null;

function stateOf(check) { return check.enabled ? (check.status || "unknown") : "paused"; }
function hasReading(check) {
  const checkedAt = Date.parse(check.checked_at || "");
  return Number.isFinite(checkedAt) && checkedAt > 0;
}
function stateWords(state) {
  return state === "up" ? "Operational" : state === "down" ? "Down" : state === "paused" ? "Paused" : "Waiting";
}

function overall(rows) {
  const active = rows.filter((check) => check.enabled);
  if (!active.length) return { state: "unknown", headline: "No active checks" };
  if (active.some((check) => check.status === "down")) return { state: "down", headline: "Some services are down" };
  if (active.some((check) => check.status !== "up")) return { state: "unknown", headline: "Some services are unknown" };
  return { state: "up", headline: "All services operational" };
}

function serviceReading(check, wide = false) {
  const state = stateOf(check);
  const row = el("div", "min-w-0 rounded-lg border border-border bg-muted/20 p-3");
  const head = el("div", "flex items-center gap-2");
  const dot = el("span", "size-2 shrink-0 rounded-full");
  dot.style.background = colours[state];
  const name = el("span", "min-w-0 flex-1 truncate text-sm font-medium", check.name);
  const words = el("span", "shrink-0 text-xs", stateWords(state));
  words.style.color = colours[state];
  head.append(dot, name, words);
  row.append(head);
  if (wide) {
    const meta = el("div", "mt-2 flex items-center justify-between gap-3 text-xs text-muted-foreground");
    const uptime = check.checks ? `${number.format(check.uptime)}% uptime · ${number.format(check.checks)} checks` : "No checks yet";
    const latency = hasReading(check) ? `${number.format(Math.round(check.latency_ms || 0))} ms · ${relativeTime(check.checked_at)}` : "Waiting";
    meta.append(text(uptime), text(latency));
    row.append(meta);
    const strip = el("div", "mt-2 flex h-3 items-end gap-px");
    fillStrip(strip, check.history || []);
    row.append(strip);
  }
  return row;
}

function renderWidgets() {
  const summary = overall(checks);
  for (const widget of qsa("[data-status-widget]")) {
    for (const dot of qsa("[data-status-dot]", widget)) dot.style.background = colours[summary.state];
    for (const headline of qsa("[data-status-headline]", widget)) headline.textContent = summary.headline;
    const active = checks.filter((check) => check.enabled);
    const down = active.filter((check) => check.status === "down").length;
    const count = active.length ? `${number.format(active.length)} active · ${number.format(down)} down` : "Add a service check to begin";
    for (const node of qsa("[data-status-count]", widget)) node.textContent = count;
    qsa("[data-status-services]", widget).forEach((host, index) => {
      host.replaceChildren(...checks.slice(0, index ? 8 : 4).map((check) => serviceReading(check, index > 0)));
    });
  }
}

function fillStrip(host, history) {
  host.replaceChildren(...history.slice(-60).map((ok) => {
    const bar = el("span", "h-full min-w-px flex-1 rounded-[1px]");
    bar.style.background = ok ? colours.up : colours.down;
    bar.style.opacity = ok ? ".65" : "1";
    return bar;
  }));
}

function machineName(id) {
  return machines.find((machine) => Number(machine.id) === Number(id))?.name || `Machine ${id}`;
}

function fillCard(card, check) {
  card.dataset.checkId = check.id;
  const state = stateOf(check);
  qs("[data-check-dot]", card).style.background = colours[state];
  qs("[data-check-title]", card).textContent = check.name;
  qs("[data-check-url]", card).textContent = check.url;
  const badge = qs("[data-check-badge]", card);
  badge.textContent = stateWords(state);
  badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${state === "down" ? "cn-badge-variant-destructive" : "cn-badge-variant-secondary"}`;
  qs("[data-check-error]", card).textContent = check.error || "";
  qs("[data-check-uptime]", card).textContent = check.checks ? `${number.format(check.uptime)}%` : "—";
  qs("[data-check-latency]", card).textContent = hasReading(check) ? `${number.format(Math.round(check.latency_ms || 0))} ms` : "—";
  qs("[data-check-checked]", card).textContent = hasReading(check) ? relativeTime(check.checked_at) : "not checked";
  const machine = qs("[data-check-machine]", card);
  machine.hidden = !check.node_id;
  machine.classList.toggle("flex", Boolean(check.node_id));
  if (check.node_id) qs("[data-check-machine-name]", machine).textContent = machineName(check.node_id);
  fillStrip(qs("[data-check-strip]", card), check.history || []);
}

function renderPage() {
  const host = qs("[data-check-rows]");
  if (!host) return;
  const template = qs("[data-check-row-template]");
  host.replaceChildren(...checks.map((check) => {
    const card = template.content.firstElementChild.cloneNode(true);
    fillCard(card, check);
    return card;
  }));
  const empty = qs("[data-check-empty]");
  if (empty) empty.hidden = checks.length > 0;
}

async function loadMachines() {
  if (machines.length) return machines;
  if (!machinesRequest) {
    machinesRequest = request("/api/cluster").then((rows) => {
      machines = rows || [];
      return machines;
    }).finally(() => { machinesRequest = null; });
  }
  return machinesRequest;
}

function fillMachineSelect(selected = 0) {
  const form = qs("[data-check-form]");
  if (!form) return;
  const field = qs("[data-check-machine-field]", form);
  const select = qs('[data-check-field="node_id"]', form);
  field.hidden = machines.length === 0;
  const empty = el("option", "", "Not attached to a machine");
  empty.value = "";
  const options = machines.map((machine) => {
    const option = el("option", "", machine.name);
    option.value = String(machine.id);
    return option;
  });
  select.replaceChildren(empty, ...options);
  select.value = selected ? String(selected) : "";
}

export async function refreshHealthChecks(force = false) {
  if (!qs("[data-check-rows]") && !qs("[data-status-widget]")) return [];
  if (!inFlight || force) {
    inFlight = request("/api/checks").then((rows) => { checks = rows || []; return checks; }).finally(() => { inFlight = null; });
  }
  await inFlight;
  // No association means no cluster request and no cluster UI. Attached cards
  // are the only closed-page reason to resolve a machine name.
  if (qs("[data-check-rows]") && checks.some((check) => check.node_id)) await loadMachines();
  renderPage();
  renderWidgets();
  return checks;
}

function formField(name) { return qs(`[data-check-field="${name}"]`, qs("[data-check-form]")); }

function prepareNew() {
  const form = qs("[data-check-form]");
  if (!form) return;
  form.reset();
  form.dataset.checkId = "";
  formField("interval").value = "3";
  formField("enabled").checked = true;
  qs("[data-check-dialog-title]").textContent = "Add a health check";
  qs("[data-check-submit]", form).textContent = "Add check";
  qs("[data-check-form-status]", form).textContent = "";
  fillMachineSelect(0);
}

async function openEditor(check) {
  const form = qs("[data-check-form]");
  if (!form) return;
  await loadMachines();
  form.dataset.checkId = String(check.id);
  for (const name of ["name", "url", "public_name"]) formField(name).value = check[name] || "";
  formField("interval").value = check.interval_seconds || 3;
  formField("public").checked = !!check.public;
  formField("enabled").checked = !!check.enabled;
  fillMachineSelect(check.node_id || 0);
  qs("[data-check-dialog-title]").textContent = `Edit ${check.name}`;
  qs("[data-check-submit]", form).textContent = "Save changes";
  qs("[data-check-form-status]", form).textContent = "";
  document.getElementById("check-dialog").checked = true;
}

function bodyFromForm(form) {
  const field = (name) => qs(`[data-check-field="${name}"]`, form);
  return {
    name: field("name").value.trim(),
    url: field("url").value.trim(),
    interval_seconds: Number(field("interval").value) || 3,
    public_name: field("public_name").value.trim(),
    public: field("public").checked,
    enabled: field("enabled").checked,
    node_id: Number(field("node_id").value) || 0,
  };
}

async function saveCheck(form) {
  const status = qs("[data-check-form-status]", form);
  const submit = qs("[data-check-submit]", form);
  const id = Number(form.dataset.checkId) || 0;
  status.textContent = "Saving…";
  submit.disabled = true;
  try {
    const body = bodyFromForm(form);
    await request(id ? `/api/checks/${id}` : "/api/checks", {
      method: id ? "PUT" : "POST", headers: adminHeaders(), body: JSON.stringify(body),
    });
    document.getElementById("check-dialog").checked = false;
    await refreshHealthChecks(true);
  } catch (failure) {
    status.textContent = failure.message;
  } finally {
    submit.disabled = false;
  }
}

async function runCheck(card) {
  const note = qs("[data-check-note]", card);
  note.textContent = "Checking…";
  try {
    await request("/api/checks/run", { method: "POST", headers: adminHeaders(), body: JSON.stringify({ check_id: Number(card.dataset.checkId) }) });
    await refreshHealthChecks(true);
  } catch (failure) { note.textContent = failure.message; }
}

async function deleteCheck(card) {
  const check = checks.find((item) => String(item.id) === card.dataset.checkId);
  if (!confirm(`Delete ${check?.name || "this check"} and its history?`)) return;
  await request(`/api/checks/${card.dataset.checkId}`, { method: "DELETE", headers: adminHeaders() });
  await refreshHealthChecks(true);
}

document.addEventListener("submit", (event) => {
  const form = event.target.closest?.("[data-check-form]");
  if (!form) return;
  event.preventDefault();
  saveCheck(form);
});

document.addEventListener("click", (event) => {
  if (event.target.closest?.('[data-check-open="new"]')) {
    prepareNew();
    document.getElementById("check-dialog").checked = true;
    loadMachines().then(() => fillMachineSelect(0));
  }
  const card = event.target.closest?.("[data-check-row]");
  if (!card) return;
  const check = checks.find((item) => String(item.id) === card.dataset.checkId);
  if (event.target.closest("[data-check-edit]") && check) openEditor(check);
  if (event.target.closest("[data-check-run]")) runCheck(card);
  if (event.target.closest("[data-check-delete]")) deleteCheck(card).catch((failure) => { qs("[data-check-note]", card).textContent = failure.message; });
});

document.addEventListener("change", (event) => {
  if (event.target?.id !== "check-dialog" || !event.target.checked) return;
  const selected = Number(qs("[data-check-form]")?.dataset.checkId) || 0;
  loadMachines().then(() => fillMachineSelect(checks.find((check) => check.id === selected)?.node_id || 0));
});
