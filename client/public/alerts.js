// The alerts page: where events go, and the rules that send them there.
//
// Two lists, one module, because they are one decision. A destination with no
// rule sends nothing; a rule needs a destination to exist before it can be
// saved. Splitting them across two pages would mean a feature nobody can
// finish configuring without navigating twice.
//
// Every row saves itself. There is no page-wide save button on purpose: these
// are independent rows that other people's machines are judged by, and a single
// "save everything" is how somebody's half-typed threshold reaches production
// alongside the change they meant to make.

import { adminHeaders, el, qs, qsa, relativeTime, request } from "./core.js";
import { ensure, forget } from "./store.js";
import { ask } from "./cluster.js";

let webhooks = [];
let monitors = [];
let metrics = [];
let nodes = [];
let categories = [];
let jobs = [];
let viewRules = [];

export async function refreshAlerts() {
  if (!qs("[data-webhooks]")) return;
  // One request answers both lists and the vocabulary to read them with: the
  // rule editor cannot label a threshold box without knowing the metric's
  // unit, and a second round trip for eight constants is a second round trip
  // for eight constants.
  // Two keys, not one. The rules change when somebody edits them; the machines
  // change every three seconds, because a health check moved a latency by a
  // millisecond. Folding both into one value would mean this page rebuilt its
  // rows — losing a half-typed threshold and the cursor in it — every time the
  // prober did its job. So the machine half is reduced to what this page
  // actually uses: an id and a name.
  await Promise.all([
    ensure("alerts.catalogue",
      () => request("/api/cluster/monitors", { headers: adminHeaders() }),
      (catalogue) => {
        webhooks = catalogue.webhooks || [];
        monitors = catalogue.monitors || [];
        metrics = catalogue.metrics || [];
        categories = catalogue.categories || [];
        jobs = catalogue.jobs || [];
        viewRules = catalogue.views || [];
        loaded.catalogue = true;
        renderAll();
      }),
    ensure("cluster.names",
      async () => (await request("/api/cluster")).map((node) => ({ id: node.id, name: node.name })),
      (list) => {
        nodes = list || [];
        loaded.names = true;
        renderAll();
      }),
  ]);
}

// Both halves or neither: a rule row needs the metric vocabulary and the
// machine list at once, and drawing it with half of them would offer a picker
// with nothing in it.
const loaded = { catalogue: false, names: false };

function renderAll() {
  if (!loaded.catalogue || !loaded.names) return;
  renderWebhooks();
  renderMonitors();
}

// ---------------------------------------------------------------------------
// Destinations
// ---------------------------------------------------------------------------

function renderWebhooks() {
  const host = qs("[data-webhook-rows]");
  const template = qs("[data-webhook-row-template]");
  if (!host || !template) return;
  host.replaceChildren(...webhooks.map((hook) => webhookRow(template, hook)));
  qs("[data-webhook-empty]").hidden = webhooks.length > 0;
}

function webhookRow(template, hook) {
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.webhookId = hook.id || 0;
  qs('[data-webhook-field="name"]', row).value = hook.name || "";
  qs('[data-webhook-field="url"]', row).value = hook.url || "";
  qs('[data-webhook-field="header"]', row).value = hook.header || "";

  // A stored token is dots and a Change button, the same bargain a machine's
  // password makes: what is on screen is not the token, so there is nothing to
  // edit in place — changing it means typing the new one in full.
  const token = qs('[data-webhook-field="token"]', row);
  const change = qs("[data-webhook-change]", row);
  if (hook.has_token) {
    token.value = "••••••••••••";
    token.readOnly = true;
    change.hidden = false;
  }

  const state = qs("[data-webhook-state]", row);
  if (hook.last_error) {
    state.textContent = `last delivery failed · ${hook.last_error}`;
    state.className = "text-[.65rem] text-destructive empty:hidden";
  } else if (hook.last_sent_at && !hook.last_sent_at.startsWith("0001")) {
    state.textContent = `last sent ${relativeTime(hook.last_sent_at)}`;
    state.className = "text-[.65rem] text-muted-foreground empty:hidden";
  }
  // A destination that has never been saved cannot be tested: the server sends
  // from the stored row, on purpose, so that what is tested is what will fire.
  if (!hook.id) qs("[data-webhook-test]", row).disabled = true;
  return row;
}

async function saveWebhook(row) {
  const id = Number(row.dataset.webhookId) || 0;
  const token = qs('[data-webhook-field="token"]', row);
  const body = {
    id,
    name: qs('[data-webhook-field="name"]', row).value.trim(),
    url: qs('[data-webhook-field="url"]', row).value.trim(),
    header: qs('[data-webhook-field="header"]', row).value.trim(),
  };
  // Absent leaves the stored token alone; a value replaces it. The dots are
  // never sent back as a token — that is the one thing that would turn a
  // rename into a credential nobody can use.
  if (!token.readOnly) body.token = token.value;
  await request("/api/webhooks", { method: "PUT", headers: adminHeaders(), body: JSON.stringify(body) });
  forget("alerts.catalogue");
  await refreshAlerts();
}

async function testWebhook(row, control) {
  const id = Number(row.dataset.webhookId) || 0;
  const state = qs("[data-webhook-state]", row);
  state.textContent = "sending…";
  state.className = "text-[.65rem] text-muted-foreground empty:hidden";
  const previous = control.textContent;
  control.disabled = true;
  try {
    const result = await request("/api/webhooks/test", {
      method: "POST", headers: adminHeaders(), body: JSON.stringify({ id }),
    });
    // A refusal comes back as a 200 carrying the reason, because "the webhook
    // answered 401" is the answer to the question that was asked.
    state.textContent = result.ok ? "test delivered" : `test failed · ${result.error}`;
    state.className = result.ok
      ? "text-[.65rem] text-muted-foreground empty:hidden"
      : "text-[.65rem] text-destructive empty:hidden";
  } finally {
    control.disabled = false;
    control.textContent = previous;
  }
}

async function removeWebhook(row) {
  const id = Number(row.dataset.webhookId) || 0;
  if (!id) {
    row.remove();
    return;
  }
  const hook = webhooks.find((candidate) => candidate.id === id);
  const using = monitors.filter((monitor) => monitor.webhook_id === id).length;
  const yes = await ask({
    title: `Remove ${hook?.name || "this destination"}?`,
    body: using
      ? `${using} rule${using === 1 ? "" : "s"} point here, and they go with it — a rule with nowhere to send is a rule that decides something is wrong and tells no one.`
      : "Nothing points at it yet.",
    confirm: "Remove",
  });
  if (!yes) return;
  await request(`/api/webhooks/${id}`, { method: "DELETE", headers: adminHeaders() });
  forget("alerts.catalogue");
  await refreshAlerts();
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// The rules, under the heading each belongs to. A dozen rows reading
// "cpu_percent above 90" say nothing about which are watching the service and
// which are watching the box; grouped, they do — and the two categories guard
// edits elsewhere (a backup's budget, a panel's line) are listed here too, so
// "what is being watched" has one answer rather than three pages.
function renderMonitors() {
  const host = qs("[data-monitor-rows]");
  const template = qs("[data-monitor-row-template]");
  if (!host || !template) return;

  const sections = [];
  for (const category of categories) {
    const own = monitors.filter((monitor) => categoryOf(monitor) === category);
    const foreign = category === "Jobs" ? jobs : category === "Views" ? viewRules : [];
    if (!own.length && !foreign.length) continue;
    sections.push(categoryHeading(category, own.length + foreign.length));
    sections.push(...own.map((monitor) => monitorRow(template, monitor)));
    sections.push(...foreign.map((rule) => foreignRow(category, rule)));
  }
  // A rule being added has no category yet — it sits at the end until saved.
  host.replaceChildren(...sections);
  qs("[data-monitor-empty]").hidden = sections.length > 0;
}

function categoryOf(monitor) {
  return metrics.find((metric) => metric.key === monitor.metric)?.category || "Service";
}

const CATEGORY_HINTS = {
  Service: "answered by the health check, from outside, with no login",
  Machine: "answered by the box itself over SSH — only where guard has a way in",
  Jobs: "stored commands that stopped succeeding · edited under Settings → Cluster",
  Views: "saved panels with a line across them · edited in the view's drawer",
};

function categoryHeading(category, count) {
  const row = el("div", "flex flex-wrap items-baseline gap-x-3 gap-y-1 bg-muted/30 px-5 py-2");
  row.append(
    el("p", "text-[.68rem] font-semibold uppercase tracking-[.18em] text-muted-foreground", category),
    el("span", "text-[.65rem] text-muted-foreground", `${count} rule${count === 1 ? "" : "s"}`),
    el("span", "text-[.65rem] text-muted-foreground", CATEGORY_HINTS[category] || ""),
  );
  return row;
}

// A rule guard owns somewhere else: shown so the list is the whole answer, and
// linked rather than edited so there is one place each rule is written.
function foreignRow(category, rule) {
  const row = el("div", "flex flex-wrap items-center gap-3 px-5 py-3");
  const label = category === "Jobs"
    ? `${rule.node} · ${rule.action}`
    : rule.view;
  const detail = category === "Jobs"
    ? `no success in ${Math.round(rule.stale_after_seconds / 60)} min${rule.schedule ? ` · ${rule.schedule}` : ""}`
    : `${rule.op} ${rule.threshold}${rule.value !== undefined ? ` · reading ${round(rule.value)}` : ""}`;
  const where = el("div", "min-w-0 flex-1");
  where.append(
    el("p", "truncate text-sm", label),
    el("p", "truncate text-[.65rem] text-muted-foreground", detail),
  );
  const state = el("span", rule.firing
    ? "shrink-0 text-[.65rem] text-destructive"
    : "shrink-0 text-[.65rem] text-muted-foreground", rule.firing ? "firing" : "ok");
  const link = el("a", "shrink-0 text-[.65rem] underline underline-offset-2 hover:text-foreground",
    category === "Jobs" ? "Settings → Cluster" : "Open the view");
  link.href = category === "Jobs" ? "/cluster" : "/views";
  row.append(where, state, link);
  return row;
}

function monitorRow(template, monitor) {
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.monitorId = monitor.id || 0;

  const machine = qs('[data-monitor-field="node"]', row);
  machine.replaceChildren(
    // Zero is not "none": it is every machine, including the ones added next
    // month, which is how one disk rule covers a fleet.
    el("option", "", "Every machine"),
    ...nodes.map((node) => {
      const option = el("option", "", node.name);
      option.value = node.id;
      return option;
    }),
  );
  machine.firstElementChild.value = "0";
  machine.value = String(monitor.node_id || 0);

  const metric = qs('[data-monitor-field="metric"]', row);
  // Grouped in the picker for the same reason the list is: "CPU" and "Response
  // time" are answered by different things, and one of them is silent on a
  // machine with no login.
  const options = [...qs("[data-metric-options]").content.cloneNode(true).children];
  metric.replaceChildren(...categories.flatMap((category) => {
    const own = options.filter((option) => option.dataset.category === category);
    if (!own.length) return [];
    const group = document.createElement("optgroup");
    group.label = category;
    group.append(...own);
    return [group];
  }));
  metric.value = monitor.metric || metrics[0]?.key || "";

  qs('[data-monitor-field="op"]', row).value = monitor.op || "above";
  qs('[data-monitor-field="threshold"]', row).value = monitor.threshold ?? "";
  qs('[data-monitor-field="for"]', row).value = monitor.for_seconds ? Math.round(monitor.for_seconds / 60) : "";
  qs('[data-monitor-field="enabled"]', row).checked = monitor.id ? !!monitor.enabled : true;

  const destination = qs('[data-monitor-field="webhook"]', row);
  destination.replaceChildren(...webhooks.map((hook) => {
    const option = el("option", "", hook.name);
    option.value = hook.id;
    return option;
  }));
  if (!webhooks.length) destination.replaceChildren(el("option", "", "add a destination first"));
  destination.value = String(monitor.webhook_id || webhooks[0]?.id || "");

  shapeForMetric(row);
  qs("[data-monitor-state]", row).textContent = firingLine(monitor);
  qs("[data-monitor-state]", row).className = (monitor.states || []).some((state) => state.firing)
    ? "text-[.65rem] text-destructive empty:hidden"
    : "text-[.65rem] text-muted-foreground empty:hidden";
  return row;
}

// What the rule is currently reading, which is the only way to tell a rule that
// is watching from a rule that is watching nothing.
function firingLine(monitor) {
  const states = monitor.states || [];
  if (!states.length) return monitor.id ? "nothing measured yet" : "";
  const firing = states.filter((state) => state.firing);
  if (firing.length) {
    return `firing on ${firing.map((state) => `${state.nodeName || state.node_name || "?"} (${round(state.value)})`).join(", ")}`;
  }
  const worst = states.slice().sort((a, b) => b.value - a.value)[0];
  return `ok · ${worst.node_name || ""} ${round(worst.value)}`.trim();
}

const round = (value) => (Number.isInteger(value) ? value : Number(value).toFixed(1));

// A state rule — "health check failing" — has nothing to put in a threshold
// box, so the box goes away rather than sitting there wanting a number.
function shapeForMetric(row) {
  const metric = qs('[data-monitor-field="metric"]', row);
  const option = metric.selectedOptions[0];
  const isState = option?.dataset.state === "1";
  const threshold = qs('[data-monitor-field="threshold"]', row);
  threshold.hidden = isState;
  threshold.closest("label").hidden = isState;
  qs('[data-monitor-field="op"]', row).hidden = isState;
  qs("[data-monitor-unit]", row).textContent = option?.dataset.unit || "";
}

async function saveMonitor(row) {
  const metric = qs('[data-monitor-field="metric"]', row);
  const body = {
    id: Number(row.dataset.monitorId) || 0,
    node_id: Number(qs('[data-monitor-field="node"]', row).value) || 0,
    metric: metric.value,
    op: qs('[data-monitor-field="op"]', row).value,
    threshold: Number(qs('[data-monitor-field="threshold"]', row).value) || 0,
    // Typed in minutes, stored in seconds — a hold measured in seconds is a
    // field people put 5 in.
    for_seconds: Math.round(Number(qs('[data-monitor-field="for"]', row).value || 0) * 60),
    webhook_id: Number(qs('[data-monitor-field="webhook"]', row).value) || 0,
    enabled: qs('[data-monitor-field="enabled"]', row).checked,
  };
  await request("/api/cluster/monitors", { method: "PUT", headers: adminHeaders(), body: JSON.stringify(body) });
  forget("alerts.catalogue");
  await refreshAlerts();
}

async function removeMonitor(row) {
  const id = Number(row.dataset.monitorId) || 0;
  if (!id) {
    row.remove();
    return;
  }
  await request(`/api/cluster/monitors/${id}`, { method: "DELETE", headers: adminHeaders() });
  forget("alerts.catalogue");
  await refreshAlerts();
}

// ---------------------------------------------------------------------------

function reportOn(control) {
  return (failure) => {
    const row = control.closest("[data-webhook], [data-monitor]");
    const state = row && (qs("[data-webhook-state]", row) || qs("[data-monitor-state]", row));
    if (state) {
      state.textContent = failure.message;
      state.className = "text-[.65rem] text-destructive empty:hidden";
      return;
    }
    console.error(failure);
  };
}

document.addEventListener("click", (event) => {
  const add = event.target.closest?.("[data-webhook-add]");
  if (add) {
    qs("[data-webhook-rows]").append(webhookRow(qs("[data-webhook-row-template]"), {}));
    qs("[data-webhook-empty]").hidden = true;
    return;
  }
  const addRule = event.target.closest?.("[data-monitor-add]");
  if (addRule) {
    qs("[data-monitor-rows]").append(monitorRow(qs("[data-monitor-row-template]"), {}));
    qs("[data-monitor-empty]").hidden = true;
    return;
  }
  const change = event.target.closest?.("[data-webhook-change]");
  if (change) {
    const token = qs('[data-webhook-field="token"]', change.closest("[data-webhook]"));
    token.readOnly = false;
    token.value = "";
    token.focus();
    change.hidden = true;
    return;
  }
  for (const [selector, handler] of [
    ["[data-webhook-save]", saveWebhook],
    ["[data-webhook-remove]", removeWebhook],
    ["[data-monitor-save]", saveMonitor],
    ["[data-monitor-remove]", removeMonitor],
  ]) {
    const control = event.target.closest?.(selector);
    if (control) {
      handler(control.closest("[data-webhook], [data-monitor]")).catch(reportOn(control));
      return;
    }
  }
  const test = event.target.closest?.("[data-webhook-test]");
  if (test && !test.disabled) {
    testWebhook(test.closest("[data-webhook]"), test).catch(reportOn(test));
  }
});

// Changing the measurement reshapes the row, because "health check failing"
// has no threshold and "disk above" does.
document.addEventListener("change", (event) => {
  const metric = event.target.closest?.('[data-monitor-field="metric"]');
  if (!metric) return;
  const row = metric.closest("[data-monitor]");
  const option = metric.selectedOptions[0];
  if (option?.dataset.op) qs('[data-monitor-field="op"]', row).value = option.dataset.op;
  shapeForMetric(row);
});
