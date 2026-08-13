// The cluster: machines guard watches from the outside.
//
// Two surfaces, one renderer. The settings page lists them with controls; the
// overview shows the same rows with the controls hidden by CSS, because a
// second near-identical template drifts the first time a column is added.

import { adminHeaders, el, number, qs, qsa, relativeTime, request, text } from "./core.js";

const tones = {
  up: "border-primary/40 bg-primary/15 text-primary",
  down: "cn-badge-variant-destructive",
  unknown: "cn-badge-variant-secondary",
  paused: "border-warning/40 bg-warning/15 text-warning",
};

const colours = {
  up: "var(--primary)",
  down: "var(--destructive)",
  unknown: "var(--muted-foreground)",
};

let nodes = [];
let nodesAt = 0;
let nodesRequest = null;

// The node list, for callers that only need the names and states — the cluster
// filter on the signal pages. Cached briefly and shared, so three filter bars
// on three pages do not each ask.
export async function clusterNodes() {
  if (nodes.length && Date.now() - nodesAt < 15_000) return nodes;
  if (!nodesRequest) {
    nodesRequest = request("/api/cluster")
      .then((list) => {
        nodes = list;
        nodesAt = Date.now();
        return list;
      })
      .catch(() => nodes)
      .finally(() => { nodesRequest = null; });
  }
  return nodesRequest;
}

export async function refreshCluster() {
  if (qs("[data-topology]")) {
    const topology = await request("/api/cluster/topology");
    nodes = topology.groups.map((group) => group.node);
    renderTopology(topology);
    renderStat();
    return;
  }
  if (!qs("[data-cluster-rows]")) return;
  nodes = await request("/api/cluster");
  render();
}

// What runs where. Each machine heads a group of the services whose telemetry
// says it served them; anything guard could not place is listed apart, under
// its own heading, because a service filed under the wrong machine would be
// worse than one filed under none.
function renderTopology(topology) {
  const host = qs("[data-topology]");
  const groupTemplate = qs("[data-topology-group-template]");
  if (!host || !groupTemplate) return;

  const sections = topology.groups.map((group) => {
    const section = groupTemplate.content.firstElementChild.cloneNode(true);
    const node = group.node;
    const status = node.enabled ? node.status : "paused";
    qs("[data-node-dot]", section).style.background = node.enabled ? colours[node.status] : "var(--warning)";
    qs("[data-node-name]", section).textContent = node.name;

    const badge = qs("[data-node-badge]", section);
    badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${tones[status]}`;
    badge.textContent = node.checked_at ? `${status} · ${Math.round(node.latency_ms)}ms` : status;

    const icon = qs("[data-node-icon]", section);
    if (node.has_icon) {
      icon.src = `/api/cluster/${node.id}/icon`;
      icon.hidden = false;
      icon.onerror = () => { icon.hidden = true; };
    }
    // The evidence, not just the verdict: a grouping that looks wrong should be
    // arguable rather than only disbelievable.
    qs("[data-node-hosts]", section).textContent = group.hosts?.length ? group.hosts.join(" · ") : hostOf(node.url);
    qs("[data-node-count]", section).textContent = count(group.instances.length);
    fillInstances(qs("[data-group-instances]", section), group.instances,
      "Nothing has reported from this machine yet.");
    return section;
  });

  if (topology.unassigned?.length) {
    const section = groupTemplate.content.firstElementChild.cloneNode(true);
    qs("[data-node-dot]", section).style.background = "var(--muted-foreground)";
    qs("[data-node-name]", section).textContent = "Not placed";
    qs("[data-node-badge]", section).remove();
    qs("[data-node-hosts]", section).textContent = "their telemetry names no host guard is watching";
    qs("[data-node-count]", section).textContent = count(topology.unassigned.length);
    fillInstances(qs("[data-group-instances]", section), topology.unassigned, "");
    sections.push(section);
  }

  host.replaceChildren(...sections);
  const empty = qs("[data-cluster-empty]");
  if (empty) empty.hidden = sections.length > 0;
}

function fillInstances(host, instances, emptyMessage) {
  const template = qs("[data-topology-instance-template]");
  if (!host || !template) return;
  if (!instances.length) {
    host.replaceChildren(el("p", "px-4 py-3 text-xs text-muted-foreground", emptyMessage));
    return;
  }
  host.replaceChildren(...instances.map((instance) => {
    const row = template.content.firstElementChild.cloneNode(true);
    row.dataset.service = instance.service;
    row.dataset.instance = instance.instance || "";
    qs("[data-instance-service]", row).textContent = instance.service;
    qs("[data-instance-id]", row).textContent = instance.instance || "default";
    qs("[data-instance-counts]", row).textContent = [
      instance.spans ? `${number.format(instance.spans)} spans` : "",
      instance.logs ? `${number.format(instance.logs)} logs` : "",
      instance.errors ? `${number.format(instance.errors)} errors` : "",
    ].filter(Boolean).join(" · ");
    qs("[data-instance-seen]", row).textContent = relativeTime(instance.last_seen);

    const select = qs("[data-instance-node]", row);
    if (select) {
      select.replaceChildren(...[
        ["0", "Work it out"],
        ...nodes.map((node) => [String(node.id), node.name]),
      ].map(([value, label]) => {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = label;
        return option;
      }));
      // Only a hand-made placement preselects a machine. A guessed one leaves
      // the box on "Work it out", so the control shows what was *decided*
      // rather than what was merely inferred.
      select.value = instance.placement === "assigned" ? String(instance.node_id) : "0";
    }
    const placement = qs("[data-instance-placement]", row);
    if (placement) {
      placement.textContent = { assigned: "pinned", host: "by host" }[instance.placement] || "";
      placement.title = instance.placement === "host"
        ? "Worked out from the hosts in its telemetry"
        : instance.placement === "assigned" ? "Set by hand" : "";
    }
    return row;
  }));
}

const count = (n) => `${n} ${n === 1 ? "service" : "services"}`;

function hostOf(raw) {
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}

function renderStat() {
  for (const stat of qsa('[data-stat="cluster"]')) {
    stat.textContent = nodes.length ? `${nodes.filter((node) => node.status === "up").length}/${nodes.length}` : "—";
  }
}

function render() {
  // The dashboard refreshes every three seconds. Rebuilding a row while its
  // interval box is being typed into would replace the box mid-edit and throw
  // away what was typed.
  if (document.activeElement?.matches?.("[data-node-interval]")) return;
  for (const host of qsa("[data-cluster-rows]")) {
    host.replaceChildren(...nodes.map(row).filter(Boolean));
  }
  for (const node of qsa("[data-cluster-empty]")) node.hidden = nodes.length > 0;

  const summary = qs("[data-cluster-summary]");
  if (summary) {
    const up = nodes.filter((node) => node.status === "up").length;
    const down = nodes.filter((node) => node.status === "down").length;
    summary.textContent = nodes.length
      ? `${up} of ${nodes.length} reachable${down ? ` · ${down} down` : ""}`
      : "Nothing watched yet";
  }
  // The overview's stat tile. Reachable over watched, because "3" alone does
  // not say whether that is all of them.
  for (const stat of qsa('[data-stat="cluster"]')) {
    stat.textContent = nodes.length ? `${nodes.filter((node) => node.status === "up").length}/${nodes.length}` : "—";
  }
}

function row(node) {
  const template = qs("[data-cluster-row-template]");
  if (!template) return null;
  const item = template.content.firstElementChild.cloneNode(true);
  item.dataset.nodeId = node.id;

  const status = node.enabled ? node.status : "paused";
  qs("[data-node-dot]", item).style.background = node.enabled ? colours[node.status] : "var(--warning)";
  qs("[data-node-dot]", item).title = status;
  qs("[data-node-name]", item).textContent = node.name;

  const badge = qs("[data-node-badge]", item);
  badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${tones[status]}`;
  badge.textContent = node.enabled ? (node.status_code ? `${status} · ${node.status_code}` : status) : "paused";

  const link = qs("[data-node-url]", item);
  link.textContent = node.url;
  link.href = node.url;

  const icon = qs("[data-node-icon]", item);
  if (icon && node.has_icon) {
    icon.src = `/api/cluster/${node.id}/icon`;
    icon.hidden = false;
    // The bytes were an image when guard stored them; a broken one now means
    // the node changed under us, and an alt box is worse than the dot alone.
    icon.onerror = () => { icon.hidden = true; };
  }

  // A node that has never answered has no error to show and no latency to
  // report; saying "0 ms" would be a measurement it never took.
  qs("[data-node-error]", item).textContent = node.error || "";
  qs("[data-node-latency]", item).textContent = node.checked_at ? `${number.format(Math.round(node.latency_ms))} ms` : "—";
  qs("[data-node-checked]", item).textContent = node.checked_at ? relativeTime(node.checked_at) : "not checked yet";
  // A decimal on a whole number is noise: 100% and 0% are the two readings a
  // reader sees most, and "0.0%" says nothing "0%" does not.
  qs("[data-node-uptime]", item).textContent = node.checks
    ? `${node.uptime.toFixed(Number.isInteger(node.uptime) ? 0 : 1)}%`
    : "—";
  // How many checks that percentage is made of. 100% of two checks and 100% of
  // twenty thousand are the same number and not the same claim.
  qs("[data-node-checks]", item).textContent = node.checks
    ? `over ${number.format(node.checks)} check${node.checks === 1 ? "" : "s"}`
    : "24h uptime";

  const interval = qs("[data-node-interval]", item);
  if (interval) interval.value = node.interval_seconds || 3;

  strip(qs("[data-node-strip]", item), node.history || []);

  const pause = qs("[data-node-pause-icon]", item);
  if (pause) pause.textContent = node.enabled ? "⏸" : "▶";
  qs("[data-node-pause]", item)?.setAttribute("title", node.enabled ? "Pause watching" : "Resume watching");
  return item;
}

// The last sixty checks, oldest first. A single "up" badge cannot show a node
// that has been flapping all morning; sixty bars can.
function strip(host, history) {
  if (!host) return;
  host.replaceChildren(...history.slice(-60).map((ok) => {
    const bar = el("span", "w-full rounded-[1px]");
    bar.style.height = ok ? "100%" : "45%";
    bar.style.background = ok ? colours.up : colours.down;
    bar.style.opacity = ok ? "0.65" : "1";
    return bar;
  }));
  if (!history.length) host.replaceChildren(el("span", "text-[.6rem] text-muted-foreground", "no history"));
}

function readForm(form) {
  return {
    name: qs('[data-node="name"]', form).value.trim(),
    url: qs('[data-node="url"]', form).value.trim(),
    interval_seconds: Number(qs('[data-node="interval"]', form).value) || 3,
  };
}

async function addNode(form) {
  const status = qs("[data-cluster-status]", form);
  const node = readForm(form);
  status.textContent = "Saving…";
  try {
    await request("/api/cluster", { method: "POST", headers: adminHeaders(), body: JSON.stringify(node) });
    form.reset();
    status.textContent = `Watching ${node.name}. The first check runs within the interval.`;
    await refreshCluster();
    // Ask for its state immediately rather than leaving a new row saying
    // "unknown" for half a minute, which reads as broken.
    await checkNow();
  } catch (failure) {
    status.textContent = failure.message;
  }
}

async function checkNow() {
  const button = qs("[data-cluster-check]");
  if (button) button.disabled = true;
  try {
    nodes = await request("/api/cluster/check", { method: "POST", headers: adminHeaders() });
    render();
  } catch (failure) {
    const status = qs("[data-cluster-status]");
    if (status) status.textContent = failure.message;
  } finally {
    if (button) button.disabled = false;
  }
}

async function updateNode(id, changes) {
  const node = nodes.find((item) => String(item.id) === String(id));
  if (!node) return;
  await request(`/api/cluster/${id}`, {
    method: "PUT",
    headers: adminHeaders(),
    body: JSON.stringify({
      name: node.name,
      url: node.url,
      enabled: node.enabled,
      interval_seconds: node.interval_seconds,
      ...changes,
    }),
  });
  await refreshCluster();
}

// Pin an instance to a machine, or hand it back to the automatic match.
// The server answers with the whole new arrangement, so the page settles on
// what was stored rather than on its own guess at the consequence.
async function assign(service, instance, nodeID) {
  const topology = await request("/api/cluster/assign", {
    method: "PUT",
    headers: adminHeaders(),
    body: JSON.stringify({ service, instance, node_id: nodeID }),
  });
  nodes = topology.groups.map((group) => group.node);
  renderTopology(topology);
  renderStat();
}

async function deleteNode(id) {
  const node = nodes.find((item) => String(item.id) === String(id));
  if (!node || !confirm(`Stop watching ${node.name}?`)) return;
  await request(`/api/cluster/${id}`, { method: "DELETE", headers: adminHeaders() });
  await refreshCluster();
}

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-cluster-form]")) return;
  event.preventDefault();
  addNode(event.target);
});

// change, not input: a number box fires input on every keystroke, and saving
// "1" on the way to typing "120" would briefly hammer the machine at one
// second.
document.addEventListener("change", (event) => {
  const select = event.target.closest?.("[data-instance-node]");
  if (select) {
    const row = select.closest("[data-service]");
    assign(row.dataset.service, row.dataset.instance, Number(select.value)).catch(reportOn(select));
    return;
  }
  const box = event.target.closest?.("[data-node-interval]");
  if (!box) return;
  const row = box.closest("[data-node-id]");
  const seconds = Math.min(3600, Math.max(1, Number(box.value) || 3));
  box.value = seconds;
  updateNode(row.dataset.nodeId, { interval_seconds: seconds }).catch(reportOn(box));
});

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-cluster-check]")) {
    checkNow();
    return;
  }
  const pause = event.target.closest("[data-node-pause]");
  if (pause) {
    const id = pause.closest("[data-node-id]").dataset.nodeId;
    const node = nodes.find((item) => String(item.id) === String(id));
    updateNode(id, { enabled: !node?.enabled }).catch(reportOn(pause));
    return;
  }
  const remove = event.target.closest("[data-node-delete]");
  if (remove) {
    deleteNode(remove.closest("[data-node-id]").dataset.nodeId).catch(reportOn(remove));
  }
});

// A failed write has to say so somewhere the reader is already looking — most
// often it is a missing admin token, which is fixable but invisible.
function reportOn(node) {
  return (failure) => {
    const status = qs("[data-cluster-status]") || qs("[data-cluster-summary]");
    if (status) status.textContent = failure.message;
    else node.title = failure.message;
  };
}
