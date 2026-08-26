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
let incidentCheck = null;

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

function incidentDuration(seconds) {
  const minutes = Math.ceil((seconds || 0) / 60);
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (!hours) return `${minutes} mins`;
  return rest ? `${hours} hrs ${rest} mins` : `${hours} hrs`;
}

function fillMinuteOptions(select, maximum, selected) {
	const value = Math.min(Math.max(0, Number(selected) || 0), maximum);
	const options = [];
	for (let minute = 0; minute <= maximum; minute++) {
		const option = el("option", "", `${minute} min`);
		option.value = String(minute);
		options.push(option);
	}
	select.replaceChildren(...options);
	select.value = String(value);
	select.dataset.optionMax = String(maximum);
}

function incidentRow(incident, maximum) {
	const form = el("form", "border-b border-border py-2 last:border-b-0");
	form.dataset.incidentId = incident.id;
	const line = el("div", "flex items-center gap-2");
	const severity = el("select", "cn-native-select h-9 w-40 shrink-0 text-xs");
	severity.name = "severity";
	const partial = el("option", "", "⚠️ Partial outage");
	partial.value = "partial";
	const major = el("option", "", "❌ Major outage");
	major.value = "major";
	severity.append(partial, major);
	severity.value = incident.severity || "partial";
	const minutes = el("select", "cn-native-select h-9 w-24 shrink-0 text-xs tabular-nums");
	minutes.name = "minutes";
	fillMinuteOptions(minutes, maximum, incident.allocated_minutes || 0);
	const comment = el("input", "cn-input mt-2 h-9 w-full text-sm");
	comment.className = "cn-input h-9 min-w-32 flex-1 text-sm";
	comment.type = "text";
	comment.name = "comment";
	comment.maxLength = 500;
	comment.placeholder = "Related issue";
	comment.value = incident.comment || "";
	line.append(severity, minutes, comment);
	if (incident.events?.length) {
		const events = el("details", "relative shrink-0");
		const summary = el("summary", "cursor-pointer whitespace-nowrap text-xs text-muted-foreground", `${incident.events.length} errors`);
		const list = el("div", "absolute right-0 top-[calc(100%+6px)] z-30 max-h-64 w-80 space-y-2 overflow-y-auto rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-xl");
		for (const event of incident.events) {
			const row = el("div", "grid gap-1 border-t border-border pt-2 text-xs sm:grid-cols-[auto_1fr]");
			row.append(
				el("time", "font-mono text-muted-foreground", new Date(event.checked_at).toLocaleTimeString()),
				el("p", "min-w-0 break-words", event.error || (event.status_code ? `HTTP ${event.status_code}` : "Health check failed")),
			);
			list.append(row);
		}
		events.append(summary, list);
		line.append(events);
	}
	const state = el("p", "max-w-32 shrink-0 truncate text-xs text-muted-foreground", "");
	state.dataset.incidentStatus = "";
	line.append(state);
	if (!incident.confirmed && !incident.events?.length) {
		const remove = el("button", "cn-button cn-button-variant-ghost cn-button-size-icon-xs text-muted-foreground hover:text-destructive", "X");
		remove.type = "button";
		remove.dataset.incidentRemove = incident.id;
		remove.setAttribute("aria-label", "Remove incident");
		remove.title = "Remove incident";
		line.append(remove);
	}
	const save = el("button", "cn-button cn-button-variant-outline cn-button-size-icon-xs", "✓");
	save.type = "submit";
	save.setAttribute("aria-label", "Save incident");
	save.title = "Save incident";
	line.append(save);
	form.append(line);
	return form;
}

function utcDay(offset = 0) {
	const date = new Date();
	date.setUTCDate(date.getUTCDate() - offset);
	return date.toISOString().slice(0, 10);
}

function incidentDaySection(day, label, incidents, available) {
	const section = el("section", "space-y-2");
	section.dataset.incidentDay = day;
	section.dataset.availableMinutes = String(available);
	const head = el("div", "flex items-center justify-between gap-3");
	const title = el("div", "min-w-0");
	const items = incidents.filter((incident) => incident.day === day);
	const assigned = items.reduce((sum, incident) => sum + (incident.allocated_minutes || 0), 0);
	const remaining = Math.max(0, available - assigned);
	title.append(
		el("h3", "text-sm font-semibold", label),
		el("p", "text-[.65rem] text-muted-foreground", `${day} · ${available} min available · `),
	);
	const remainingText = el("span", "text-[.65rem] text-muted-foreground", `${remaining} remaining`);
	remainingText.dataset.incidentRemaining = "";
	title.lastElementChild.append(remainingText);
	const add = el("button", "cn-button cn-button-variant-ghost cn-button-size-icon-xs", "+");
	add.type = "button";
	add.dataset.incidentAdd = day;
	add.setAttribute("aria-label", `Add incident for ${day}`);
	head.append(title, add);
	section.append(head);
	if (items.length) {
		const selected = items.map((incident) => incident.allocated_minutes || 0);
		const firstUnallocated = selected.findIndex((minutes) => minutes === 0);
		if (firstUnallocated >= 0 && remaining > 0) selected[firstUnallocated] = remaining;
		const rows = items.map((incident, index) => {
			const usedByOthers = selected.reduce((sum, minutes, other) => sum + (other === index ? 0 : minutes), 0);
			return incidentRow({ ...incident, allocated_minutes: selected[index] }, Math.max(0, available - usedByOthers));
		});
		section.append(...rows);
		validateIncidentDay(section);
	} else {
		section.append(el("p", "rounded-lg border border-dashed border-border px-3 py-4 text-center text-xs text-muted-foreground", "No incidents"));
	}
	return section;
}

function validateIncidentDay(section) {
	const inputs = qsa('[name="minutes"]', section);
	const available = Number(section.dataset.availableMinutes) || 0;
	const values = inputs.map((input) => Number(input.value) || 0);
	for (const input of inputs) {
		const index = inputs.indexOf(input);
		const usedByOthers = values.reduce((sum, value, other) => sum + (other === index ? 0 : value), 0);
		const maximum = Math.max(0, available - usedByOthers);
		if (Number(input.dataset.optionMax) !== maximum) fillMinuteOptions(input, maximum, values[index]);
	}
	const total = inputs.reduce((sum, input) => sum + (Number(input.value) || 0), 0);
	const remaining = qs("[data-incident-remaining]", section);
	if (remaining) remaining.textContent = `${Math.max(0, available - total)} remaining`;
}

async function loadIncidentList() {
	const host = qs("[data-incident-list]");
	host.replaceChildren(el("p", "text-sm text-muted-foreground", "Loading incidents…"));
	try {
		const board = await request(`/api/checks/incidents?check_id=${encodeURIComponent(incidentCheck.id)}`);
		const incidents = board.incidents || [];
		const available = board.available_minutes || {};
		host.replaceChildren(
			incidentDaySection(utcDay(0), "Today", incidents, available[utcDay(0)] || 0),
			incidentDaySection(utcDay(1), "Yesterday", incidents, available[utcDay(1)] || 0),
			incidentDaySection(utcDay(2), "2 days ago", incidents, available[utcDay(2)] || 0),
		);
	} catch (failure) {
		host.replaceChildren(el("p", "text-sm text-destructive", failure.message));
	}
}

async function openIncidents(check) {
	incidentCheck = check;
	document.getElementById("check-dialog").checked = false;
	qs("[data-incident-dialog-title]").textContent = `${check.name} incidents`;
	document.getElementById("health-incident-dialog").checked = true;
	await loadIncidentList();
}

async function addIncident(day) {
	await request("/api/checks/incidents", {
		method: "POST", headers: adminHeaders(), body: JSON.stringify({ check_id: incidentCheck.id, day }),
	});
	await loadIncidentList();
}

document.addEventListener("submit", (event) => {
	const incident = event.target.closest?.("[data-incident-id]");
	if (incident) {
		event.preventDefault();
		const status = qs("[data-incident-status]", incident);
		const submit = qs('[type="submit"]', incident);
		const minutes = qs('[name="minutes"]', incident);
		if ((Number(minutes.value) || 0) === 0) {
			minutes.classList.add("border-destructive", "ring-2", "ring-destructive");
			minutes.focus();
			status.textContent = "";
			return;
		}
		minutes.classList.remove("border-destructive", "ring-2", "ring-destructive");
		status.textContent = "Saving…";
		submit.disabled = true;
		request(`/api/checks/incidents/${incident.dataset.incidentId}`, {
			method: "PUT", headers: adminHeaders(), body: JSON.stringify({
				comment: qs('[name="comment"]', incident).value.trim(),
				severity: qs('[name="severity"]', incident).value,
				allocated_minutes: Number(qs('[name="minutes"]', incident).value) || 0,
				confirmed: true,
			}),
		}).then(() => loadIncidentList()).catch((failure) => { status.textContent = failure.message; }).finally(() => { submit.disabled = false; });
		return;
	}
  const form = event.target.closest?.("[data-check-form]");
  if (!form) return;
  event.preventDefault();
  saveCheck(form);
});

document.addEventListener("click", (event) => {
	const remove = event.target.closest?.("[data-incident-remove]");
	if (remove) {
		remove.disabled = true;
		request(`/api/checks/incidents/${remove.dataset.incidentRemove}`, { method: "DELETE", headers: adminHeaders() })
			.then(() => loadIncidentList())
			.catch((failure) => { qs("[data-incident-status]", remove.closest("form")).textContent = failure.message; })
			.finally(() => { remove.disabled = false; });
		return;
	}
	const add = event.target.closest?.("[data-incident-add]");
	if (add) {
		add.disabled = true;
		addIncident(add.dataset.incidentAdd).catch((failure) => {
			const host = qs("[data-incident-list]");
			host.prepend(el("p", "text-sm text-destructive", failure.message));
		}).finally(() => { add.disabled = false; });
		return;
	}
  if (event.target.closest?.('[data-check-open="new"]')) {
		document.getElementById("health-incident-dialog").checked = false;
    prepareNew();
    document.getElementById("check-dialog").checked = true;
    loadMachines().then(() => fillMachineSelect(0));
  }
  const card = event.target.closest?.("[data-check-row]");
  if (!card) return;
  const check = checks.find((item) => String(item.id) === card.dataset.checkId);
	if (event.target.closest("[data-check-edit]") && check) {
		document.getElementById("health-incident-dialog").checked = false;
		openEditor(check);
	}
	if (event.target.closest("[data-check-incidents]") && check) openIncidents(check);
  if (event.target.closest("[data-check-run]")) runCheck(card);
  if (event.target.closest("[data-check-delete]")) deleteCheck(card).catch((failure) => { qs("[data-check-note]", card).textContent = failure.message; });
});

document.addEventListener("change", (event) => {
	if (event.target?.matches?.('[name="minutes"]')) {
		event.target.classList.remove("border-destructive", "ring-2", "ring-destructive");
		validateIncidentDay(event.target.closest("[data-available-minutes]"));
	}
  if (event.target?.id !== "check-dialog" || !event.target.checked) return;
  const selected = Number(qs("[data-check-form]")?.dataset.checkId) || 0;
  loadMachines().then(() => fillMachineSelect(checks.find((check) => check.id === selected)?.node_id || 0));
});

document.addEventListener("input", (event) => {
	if (event.target?.matches?.('[name="minutes"]')) validateIncidentDay(event.target.closest("[data-available-minutes]"));
});
