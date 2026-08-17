// The cluster: machines guard watches from the outside, and — for the ones that
// gave guard a way in — the commands somebody keeps for them.
//
// Two surfaces, one renderer. The settings page lists them with controls; the
// overview shows the same rows with the controls hidden by CSS, because a
// second near-identical template drifts the first time a column is added.

import { adminHeaders, el, number, qs, qsa, relativeTime, request, text } from "./core.js";
import { ensure as swr, forget, set as remember } from "./store.js";
// The cloud half of a machine — the provider strip, the link, the snapshots.
// It imports `ask` back from here, which is a cycle ES modules are fine with
// so long as nothing is called while the modules are still evaluating: both
// sides only ever reach across inside a function.
import { fillCloudCard, fillCloudDetail } from "./cloud.js";

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

// What a stored password looks like. It is not the password and it is not its
// length — the server never sends either — so the count of dots says nothing
// except "there is one".
const MASK = "••••••••••••";

// The ten tag colours, in the order they are offered. The names are what is
// stored — internal/telemetry/model.TagColours is the same list, and the
// server refuses anything else — and this is the one place they become
// pixels, so a theme that moves changes them here rather than in the
// database. Chosen to stay apart on the dark surface: no two blues, no two
// greens, and nothing so dim that a chip reads as disabled.
const TAG_COLOURS = {
  slate: "#94a3b8",
  red: "#f87171",
  orange: "#fb923c",
  amber: "#fbbf24",
  green: "#4ade80",
  teal: "#2dd4bf",
  blue: "#60a5fa",
  indigo: "#818cf8",
  violet: "#c084fc",
  pink: "#f472b6",
};

const tagColour = (name) => TAG_COLOURS[name] || TAG_COLOURS.slate;

// Which machines are opened out, remembered across navigations.
//
// Open is what gets stored, and shut is the default: the page is a list, and a
// list is for finding the machine you want among twenty. Everything needed to
// find one is in the row — state, name, tags, address, five numbers — and
// everything needed to act on it is one click away. Storing the *open* ones
// also means a machine added later arrives shut, which is the right way round:
// a new row should lengthen the list by one line, not by a screenful.
const opened = new Set(JSON.parse(localStorage.getItem("guard.cluster.open") || "[]"));

function rememberOpened() {
  localStorage.setItem("guard.cluster.open", JSON.stringify([...opened]));
}

let nodes = [];
// What the telemetry says runs on each machine, keyed by node id. Read with the
// cluster on the operational page and empty everywhere else.
let instancesByNode = new Map();
let nodesAt = 0;
let nodesRequest = null;

// Which machines have their panel open, which have edits that have not been
// saved, and what the last command printed.
//
// The page refreshes every three seconds. Without the first two sets, opening a
// panel would close it a moment later and typing into it would be thrown away
// mid-word by a redraw that has no idea anybody is typing.
const expanded = new Set();
const dirty = new Set();
const outputs = new Map();

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
  if (!qs("[data-cluster-rows]") && !qs("[data-cluster-cards]")) return;
  if (qs("[data-cluster-cards]")) {
    // Two reads for the operational page: the machines, and what the telemetry
    // says runs on each. Together, because the question a card answers is "is
    // this box fine and is the thing on it still reporting" — and the second
    // half of that used to live on a different page.
    //
    // Through swr, so walking back to this page draws the fleet from the last
    // visit in the same frame as the navigation and then corrects it. The
    // status dots are the one thing that must not be trusted stale, which is
    // why the live pass follows immediately rather than on the next tick.
    await swr("cluster.fleet", async () => {
      const [list, topology] = await Promise.all([
        request("/api/cluster"),
        request("/api/cluster/topology").catch(() => ({ groups: [] })),
      ]);
      return { list, groups: topology.groups || [] };
    }, ({ list, groups }, cached) => {
      nodes = list;
      instancesByNode = new Map(groups
        .filter((group) => group.node)
        .map((group) => [group.node.id, group.instances || []]));
      render();
      const summary = qs("[data-cluster-summary]");
      if (summary && cached) summary.textContent = "from your last visit — checking…";
    });
    return;
  }
  nodes = await swr("cluster.nodes", () => request("/api/cluster"), (list) => {
    nodes = list;
    render();
  });
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
      icon.src = `/api/cluster/icon/${node.id}`;
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

// Whether the browser reading this page has any hope of opening an address.
// Loopback and the private ranges are guard's side of the network, not the
// reader's, and https pages block plain http to them anyway.
const privateHost = /^(localhost$|127\.|0\.0\.0\.0$|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|\[?::1\]?$|.*\.local$|.*\.internal$)/i;

function reachableFromBrowser(raw) {
  try {
    return !privateHost.test(new URL(raw).hostname);
  } catch {
    return false;
  }
}

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
  // The dashboard refreshes every three seconds, and rebuilding a row while
  // somebody is typing into it would replace the box mid-word. So a redraw
  // waits for a focused *field*, and for edits that have not been saved.
  //
  // A focused button is not typing. It used to count as one, which meant that
  // pressing Lock left the focus on the Lock button and held the row at its old
  // state until the page was reloaded — the write had gone through, and the
  // only thing missing was the redraw that would have shown it.
  if (typing()) return;
  if (dirty.size) return;
  for (const host of qsa("[data-cluster-rows]")) {
    host.replaceChildren(...nodes.map(row).filter(Boolean));
  }
  for (const node of qsa("[data-cluster-empty]")) node.hidden = nodes.length > 0;
  renderCards();

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

// The cloud module fetches on its own clock — a press, a mount, a sixty-second
// staleness — and says so when an answer lands. Redrawing from here keeps one
// renderer for the cards rather than two writing into the same nodes.
document.addEventListener("guard:cloud-updated", () => render());

// Whether the cursor is in something that holds text. Buttons, links and the
// page at large are not, so a click that changes a machine is free to redraw it.
function typing() {
  const focused = document.activeElement;
  if (!focused || !focused.matches) return false;
  if (!focused.matches("input, textarea, select")) return false;
  return !!focused.closest("[data-node-row], [data-cluster-form]");
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

  const lockBadge = qs("[data-node-lock-badge]", item);
  if (lockBadge) {
    lockBadge.hidden = !node.locked;
    lockBadge.textContent = "locked";
    lockBadge.title = "Locked: the login is frozen and no command can be added, edited or removed";
  }

  // The probed address, as a link only when a browser could actually follow it.
  // Guard dials it from the server, so it is often localhost or a private
  // address — and a link that fails every time teaches people to distrust the
  // page rather than the network.
  const link = qs("[data-node-url]", item);
  const internal = qs("[data-node-internal]", item);
  if (reachableFromBrowser(node.url)) {
    link.textContent = node.url;
    link.href = node.url;
    link.hidden = false;
    if (internal) internal.textContent = "";
  } else {
    link.hidden = true;
    if (internal) internal.textContent = node.url ? `${node.url} · from the server` : "";
  }

  const icon = qs("[data-node-icon]", item);
  if (icon && node.has_icon) {
    icon.src = `/api/cluster/icon/${node.id}`;
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
    : "not checked yet";

  const interval = qs("[data-node-interval]", item);
  if (interval) interval.value = node.interval_seconds || 3;

  fillTags(item, node.tags);
  strip(qs("[data-node-strip]", item), node.history || []);

  const pause = qs("[data-node-pause-icon]", item);
  if (pause) pause.textContent = node.enabled ? "⏸" : "▶";
  qs("[data-node-pause]", item)?.setAttribute("title", node.enabled ? "Pause watching" : "Resume watching");

  detail(item, node);
  return item;
}

// The panel under a row: where the machine is, how to get in, and what to run.
// Filled from the server's answer every time, which is why an unsaved edit
// holds off the redraw rather than being merged into one.
function detail(item, node) {
  const panel = qs("[data-node-detail]", item);
  if (!panel) return;
  const open = expanded.has(node.id);
  panel.hidden = !open;
  turn(item, open);

  const field = (name) => qs(`[data-node-field="${name}"]`, panel);
  // A machine added before the address and the health path were one field kept
  // its old internal address; showing that is better than showing an empty box
  // next to a machine that is plainly being checked.
  field("domain").value = node.domain || node.internal_url || "";
  field("group").value = node.group || "";
  field("health_path").value = node.health_path || "";
  field("ssh_address").value = node.ssh_address || "";
  field("stats_interval").value = node.stats_interval_seconds ?? 0;

  qs("[data-node-probe]", panel).textContent = node.url ? `checking ${node.url}` : "";

  // Dots for a stored password, and the box is read-only until somebody asks to
  // change it: the value here is not the password, and letting it be edited in
  // place would mean saving the dots.
  const password = qs("[data-node-password]", panel);
  password.value = node.has_password ? MASK : "";
  password.readOnly = node.has_password;
  password.placeholder = node.has_password ? "" : "not set";
  delete password.dataset.changed;

  const fingerprint = qs("[data-node-fingerprint]", panel);
  if (fingerprint) {
    fingerprint.textContent = node.ssh_fingerprint
      ? `host key ${node.ssh_fingerprint}`
      : node.ssh_address ? "no host key pinned yet — the first connection pins one" : "";
  }

  // Locked is drawn here as well as enforced in the store — a control that is
  // going to be refused should not look like a control that will work.
  const locked = !!node.locked;
  field("ssh_address").readOnly = locked;
  password.readOnly = locked || node.has_password;
  const change = qs("[data-node-password-edit]", panel);
  if (change) change.hidden = locked;
  const lock = qs("[data-node-lock]", panel);
  if (lock) lock.hidden = locked;
  const note = qs("[data-node-lock-note]", panel);
  if (note) note.hidden = !locked;
  // On a locked machine the list is finished, so the controls that would change
  // it are gone rather than disabled: there is nothing to try.
  const add = qs("[data-action-add]", panel);
  if (add) add.hidden = locked;
  const saveActions = qs("[data-actions-save]", panel);
  if (saveActions) saveActions.hidden = locked;

  fillTagEditor(panel, node);
  fillActions(panel, node);
  fillEnv(panel, node);
  // Only for an open row: the picker asks the provider what the account runs,
  // and a closed row is not a question anybody asked.
  if (open) fillCloudDetail(panel, node).catch(() => {});

  const output = outputs.get(node.id);
  const pane = qs("[data-node-output]", panel);
  if (pane) {
    pane.hidden = !output;
    if (output) {
      qs("[data-node-output-head]", pane).textContent = output.head;
      qs("[data-node-output-body]", pane).textContent = output.body;
    }
  }
}

// The chevron points down when the machine is closed and up when it is open,
// which is the only thing on the row that says a row can be opened at all.
function turn(item, open) {
  const chevron = qs("[data-node-chevron]", item);
  if (chevron) chevron.style.transform = open ? "rotate(180deg)" : "";
  qs("[data-node-toggle]", item)?.setAttribute("aria-expanded", open ? "true" : "false");
}

// The editor: the chips as they will be saved, and the ten colours to pick
// the next one from.
//
// The chips are the state. Adding one appends an element and removing one
// deletes it, exactly as the action rows work, so what gets written is what
// is on screen — and the panel is marked dirty, which holds off the
// three-second redraw that would otherwise throw the edit away mid-thought.
// Tags stay editable on a locked machine: a label cannot run anything.
function fillTagEditor(panel, node) {
  const chips = qs("[data-tag-chips]", panel);
  if (chips) chips.replaceChildren(...(node.tags || []).map((tag) => tagChip(tag, { removable: true })));

  fillTagEditorSwatches(panel);
}

// Separate from the chips on purpose: picking a colour must not redraw the
// list, because the list may hold tags that have been added and not yet
// saved — and rebuilding it from the server's answer would throw them away.
function fillTagEditorSwatches(panel) {
  const swatches = qs("[data-tag-swatches]", panel);
  if (!swatches) return;
  if (!panel.dataset.tagColour) panel.dataset.tagColour = "slate";
  swatches.replaceChildren(...Object.entries(TAG_COLOURS).map(([name, colour]) => {
    const swatch = el("button", "size-5 cursor-pointer rounded-full border border-border");
    swatch.type = "button";
    swatch.dataset.tagSwatch = name;
    swatch.title = name;
    swatch.setAttribute("aria-label", `Use ${name}`);
    swatch.setAttribute("aria-pressed", String(panel.dataset.tagColour === name));
    swatch.style.background = colour;
    // The chosen one wears a ring drawn in the page's own foreground, so the
    // selection stays visible whichever of the ten it is.
    swatch.style.outline = panel.dataset.tagColour === name ? "2px solid var(--foreground)" : "";
    swatch.style.outlineOffset = "1px";
    return swatch;
  }));
}

// What the panel would save: the chips as they stand.
function readTags(panel) {
  return qsa("[data-tag-chips] [data-card-tag]", panel).map((chip) => ({
    label: chip.dataset.tagLabel,
    colour: chip.dataset.tagColour,
  }));
}

function addTag(panel) {
  const input = qs("[data-tag-input]", panel);
  const label = input.value.trim();
  if (!label) return;
  const chips = qs("[data-tag-chips]", panel);
  // Same label twice is the same tag: recolouring it here would be a second
  // way to say something the palette already says once.
  if (readTags(panel).some((tag) => tag.label.toLowerCase() === label.toLowerCase())) {
    input.value = "";
    return;
  }
  if (readTags(panel).length >= 8) {
    say(panel, "Eight tags is the limit — past that the chips are the card.");
    return;
  }
  chips.appendChild(tagChip({ label, colour: panel.dataset.tagColour || "slate" }, { removable: true }));
  input.value = "";
  input.focus();
  dirty.add(Number(panel.closest("[data-node-id]").dataset.nodeId));
}

function fillActions(panel, node) {
  const host = qs("[data-action-rows]", panel);
  const template = qs("[data-action-row-template]");
  if (!host || !template) return;
  const actions = node.actions || [];
  host.replaceChildren(...actions.map((action) => actionRow(template, action, !!node.locked)));
  const empty = qs("[data-action-empty]", panel);
  if (empty) empty.hidden = actions.length > 0;
}

function actionRow(template, action, locked) {
  const row = template.content.firstElementChild.cloneNode(true);
  row.dataset.actionId = action.id || 0;
  const name = qs('[data-action-field="name"]', row);
  const command = qs('[data-action-field="command"]', row);
  const schedule = qs('[data-action-field="schedule"]', row);
  const stale = qs('[data-action-field="stale"]', row);
  name.value = action.name || "";
  command.value = action.command || "";
  schedule.value = action.schedule || "";
  // Stored in seconds and typed in minutes: seven hours is 420, and a field
  // measured in seconds is one people put 7 in.
  stale.value = action.stale_after_seconds ? Math.round(action.stale_after_seconds / 60) : "";
  // On a locked machine the list is closed: read-only, and the remove button
  // is taken away rather than left there to be refused.
  if (locked) {
    name.readOnly = true;
    command.readOnly = true;
    schedule.readOnly = true;
    stale.readOnly = true;
    name.title = command.title = "Locked. This command cannot be changed or removed — only run.";
    qs("[data-action-remove]", row)?.remove();
  }
  // When it is next due, and — the part worth reading — when it last worked,
  // which is a different question from when it last ran.
  const next = qs("[data-action-next]", row);
  if (next) next.textContent = scheduleLine(action);
  // Go's zero time serialises as year one rather than as nothing, and an
  // action that has never run would otherwise claim to have run some seventeen
  // million hours ago.
  const ranAt = action.last_run_at && !action.last_run_at.startsWith("0001") ? action.last_run_at : "";
  const last = qs("[data-action-last]", row);
  if (last && ranAt) {
    last.textContent = action.last_error
      ? `${relativeTime(ranAt)} · ${action.last_error}`
      : `${relativeTime(ranAt)} · ok`;
    last.className = action.last_error
      ? "mr-1 text-[.65rem] text-destructive empty:hidden"
      : "mr-1 text-[.65rem] text-muted-foreground empty:hidden";
  }
  // An action that has never been saved cannot be run: the server takes an id,
  // on purpose, so that everything it runs was written down first.
  const run = qs("[data-action-run]", row);
  if (run && !action.id) {
    run.disabled = true;
    run.title = "Save this action before running it";
  }
  return row;
}

// Go's zero time serialises as year one rather than as nothing, so every
// timestamp on this page passes through here first: an action that has never
// run must not claim to have run seventeen million hours ago.
function realStamp(value) {
  return value && !value.startsWith("0001") ? value : "";
}

// relativeTime only looks backwards, and the entire point of a next run is
// that it has not happened yet.
function untilTime(value) {
  const seconds = Math.max(0, Math.round((new Date(value).getTime() - Date.now()) / 1000));
  if (seconds < 60) return `in ${seconds}s`;
  if (seconds < 3600) return `in ${Math.floor(seconds / 60)}m`;
  return `in ${Math.floor(seconds / 3600)}h`;
}

// What a command says about itself when nobody is pressing it: the cadence, the
// next fire, and when it last *worked* — which is a different question from
// when it last ran, and the one a backup is judged on.
function scheduleLine(action) {
  const parts = [];
  if (action.schedule) {
    parts.push(action.schedule);
    const next = realStamp(action.next_run_at);
    if (next) parts.push(`next ${untilTime(next)}`);
  }
  if (action.stale_after_seconds) {
    const ok = realStamp(action.last_ok_at);
    parts.push(ok ? `last ok ${relativeTime(ok)}` : "never succeeded");
  }
  return parts.join(" · ");
}

// Whether the staleness budget somebody set on this command has been blown.
// Computed here rather than sent, because the answer changes every minute and
// the page is redrawn every three seconds anyway.
function isStale(action) {
  if (!action.stale_after_seconds) return false;
  const since = realStamp(action.last_ok_at) || realStamp(action.created_at);
  if (!since) return false;
  return (Date.now() - new Date(since).getTime()) / 1000 > action.stale_after_seconds;
}

// One tag, drawn. The three tones come from one hex with an alpha suffix
// rather than three stored values: a tag is a colour, and a border that
// disagrees with its own fill is a thing to get wrong later.
function tagChip(tag, { removable = false } = {}) {
  // Built here rather than cloned from a <template>: the chips appear on
  // three surfaces, only one of which had the template, and a chip that
  // silently loses its marker attribute on the other two is a chip nothing
  // can read back. The class string is a literal, which is all Tailwind asks.
  const chip = el("span", "cn-badge inline-flex w-fit shrink-0 items-center whitespace-nowrap border");
  chip.dataset.cardTag = "";
  const colour = tagColour(tag.colour);
  chip.style.borderColor = `${colour}66`;
  chip.style.background = `${colour}26`;
  chip.style.color = colour;
  chip.dataset.tagLabel = tag.label;
  chip.dataset.tagColour = tag.colour || "slate";
  chip.replaceChildren(text(tag.label));
  if (removable) {
    const remove = el("button", "ml-1 -mr-0.5 cursor-pointer opacity-70 hover:opacity-100", "✕");
    remove.type = "button";
    remove.dataset.tagRemove = tag.label;
    remove.setAttribute("aria-label", `Remove the ${tag.label} tag`);
    chip.appendChild(remove);
  }
  return chip;
}

// The chips on a row or a card. Read-only here: this is the surface people
// scan, and a delete control on it is a delete waiting to be misclicked.
function fillTags(item, tags) {
  const host = qs("[data-node-tags]", item);
  if (!host) return;
  host.replaceChildren(...(tags || []).map((tag) => tagChip(tag)));
}

// What is running on this machine, as chips: the service, and how much it has
// sent. Derived from the telemetry — a span naming this host was served by it —
// so a machine nothing has reported from shows nothing rather than an empty
// promise.
function fillCardInstances(item, node) {
  const wrap = qs("[data-card-instances-wrap]", item);
  const host = qs("[data-card-instances]", item);
  if (!wrap || !host) return;
  const instances = instancesByNode.get(node.id) || [];
  wrap.hidden = instances.length === 0;
  if (!instances.length) return;
  qs("[data-card-instances-count]", item).textContent =
    `${instances.length} service${instances.length === 1 ? "" : "s"}`;
  host.replaceChildren(...instances.map((instance) => {
    const chip = el("span",
      "cn-badge cn-badge-variant-secondary inline-flex w-fit items-center gap-1 whitespace-nowrap",
      instance.service);
    const counts = [
      instance.spans ? `${number.format(instance.spans)} spans` : "",
      instance.logs ? `${number.format(instance.logs)} logs` : "",
      instance.metrics ? `${number.format(instance.metrics)} metrics` : "",
    ].filter(Boolean).join(" · ");
    chip.title = [instance.instance || "default", counts].filter(Boolean).join(" — ");
    return chip;
  }));
}

// The /cluster page: one card per machine, the stored commands laid out to
// run. The status fields reuse the row's data attributes, so the writers
// below serve both surfaces; what a card does not have is any input, which
// is why the typing()/dirty guards above never apply to it.
function renderCards() {
  const host = qs("[data-cluster-cards]");
  const template = qs("[data-cluster-card-template]");
  if (!host || !template) return;

  // Laid out by group, because that is how somebody holds a fleet in their
  // head: "the VPC-1 boxes", not "the twenty machines". A wall of cards in id
  // order is a wall you have to read to navigate; the same wall under three
  // headings is one you can skip past.
  const sections = [];
  for (const [group, members] of groupNodes(nodes)) {
    sections.push(groupHeading(group, members));
    // One list per group, full width: rows divided by a line, so twenty
    // machines are twenty lines rather than ten cards and a scroll.
    const list = el("div", "divide-y divide-border overflow-hidden rounded-xl border border-border");
    list.append(...members.map((node) => cardFor(template, node)));
    sections.push(list);
  }
  host.replaceChildren(...sections);
  const empty = qs("[data-cluster-cards-empty]");
  if (empty) empty.hidden = nodes.length > 0;
  fillGroupSuggestions();
}

// Grouped, named groups first and alphabetically, "Ungrouped" last — a machine
// nobody has filed yet belongs at the bottom rather than jumping the queue on
// an empty string.
function groupNodes(list) {
  const groups = new Map();
  for (const node of list) {
    const key = (node.group || "").trim();
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(node);
  }
  return [...groups.entries()].sort(([a], [b]) => {
    if (!a) return 1;
    if (!b) return -1;
    return a.localeCompare(b);
  });
}

// One line per group: what it is, how many machines, and how they are doing.
// The count of what is *wrong* leads, because a heading that says "6 machines"
// and a heading that says "6 machines · 1 down" are read differently from
// across a room.
function groupHeading(group, members) {
  const row = el("div", "flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-border pb-2");
  const down = members.filter((node) => node.enabled && node.status === "down").length;
  const paused = members.filter((node) => !node.enabled).length;
  row.append(el("h2", "text-sm font-semibold tracking-tight", group || "Ungrouped"));
  row.append(el("span", "text-xs text-muted-foreground",
    `${members.length} machine${members.length === 1 ? "" : "s"}`));
  if (down) row.append(el("span", "text-xs font-medium text-destructive", `${down} down`));
  if (paused) row.append(el("span", "text-xs text-warning", `${paused} paused`));
  if (!down && !paused) row.append(el("span", "text-xs text-muted-foreground", "all up"));
  return row;
}

// The groups that already exist, offered to the group boxes on the settings
// page. Typed rather than chosen — guard cannot know whether somebody's
// boundary is a VPC, a region or a floor — but suggesting the ones in use is
// what stops "VPC-1" and "vpc-1" from becoming two groups.
function fillGroupSuggestions() {
  const list = qs("#cluster-groups");
  if (!list) return;
  const names = [...new Set(nodes.map((node) => (node.group || "").trim()).filter(Boolean))].sort();
  list.replaceChildren(...names.map((name) => {
    const option = document.createElement("option");
    option.value = name;
    return option;
  }));
}

function cardFor(template, node) {
  const item = template.content.firstElementChild.cloneNode(true);
  item.dataset.nodeId = node.id;

  const status = node.enabled ? node.status : "paused";
  qs("[data-node-dot]", item).style.background = node.enabled ? colours[node.status] : "var(--warning)";
  qs("[data-node-dot]", item).title = status;
  qs("[data-node-name]", item).textContent = node.name;

  const badge = qs("[data-node-badge]", item);
  badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${tones[status]}`;
  badge.textContent = node.checked_at && node.enabled ? `${status} · ${Math.round(node.latency_ms)}ms` : status;

  const lockBadge = qs("[data-node-lock-badge]", item);
  lockBadge.hidden = !node.locked;
  lockBadge.textContent = "locked";
  lockBadge.title = "Locked: the commands can only be run, never changed";

  const icon = qs("[data-node-icon]", item);
  if (node.has_icon) {
    icon.src = `/api/cluster/icon/${node.id}`;
    icon.hidden = false;
    icon.onerror = () => { icon.hidden = true; };
  }

  const link = qs("[data-node-url]", item);
  const internal = qs("[data-node-internal]", item);
  if (reachableFromBrowser(node.url)) {
    link.textContent = node.url;
    link.href = node.url;
    link.hidden = false;
    internal.textContent = "";
  } else {
    link.hidden = true;
    internal.textContent = node.url ? `${node.url} · from the server` : "";
  }

  qs("[data-node-error]", item).textContent = node.error || "";
  qs("[data-node-latency]", item).textContent = node.checked_at ? `${number.format(Math.round(node.latency_ms))} ms` : "—";
  qs("[data-node-checked]", item).textContent = node.checked_at ? relativeTime(node.checked_at) : "not checked yet";
  qs("[data-node-uptime]", item).textContent = node.checks
    ? `${node.uptime.toFixed(Number.isInteger(node.uptime) ? 0 : 1)}%`
    : "—";
  qs("[data-node-checks]", item).textContent = node.checks
    ? `over ${number.format(node.checks)} check${node.checks === 1 ? "" : "s"}`
    : "not checked yet";
  fillTags(item, node.tags);
  strip(qs("[data-node-strip]", item), node.history || []);

  const actions = node.actions || [];
  showBody(item, opened.has(node.id));
  const actionsHost = qs("[data-card-actions]", item);
  const actionTemplate = qs("[data-card-action-template]");
  actionsHost.replaceChildren(...actions.map((action) => {
    const chip = actionTemplate.content.firstElementChild.cloneNode(true);
    chip.dataset.actionId = action.id;
    qs("[data-card-action-name]", chip).textContent = action.name || "unnamed";
    qs("[data-card-action-command]", chip).textContent = action.command || "";
    const ranAt = realStamp(action.last_run_at);
    const last = qs("[data-card-action-last]", chip);
    if (ranAt) {
      last.textContent = action.last_error ? `${relativeTime(ranAt)} · failed` : `${relativeTime(ranAt)} · ok`;
      last.className = action.last_error
        ? "shrink-0 text-[.65rem] text-destructive empty:hidden"
        : "shrink-0 text-[.65rem] text-muted-foreground empty:hidden";
    }
    // The unattended half, on the card that gets scanned: the cadence, the
    // next fire, and — in the destructive tone, because it is the thing this
    // whole feature exists to surface — a budget that has been blown.
    const schedule = qs("[data-card-action-schedule]", chip);
    if (schedule) {
      schedule.textContent = scheduleLine(action);
      schedule.className = isStale(action)
        ? "truncate text-[.65rem] text-destructive empty:hidden"
        : "truncate text-[.65rem] text-muted-foreground empty:hidden";
    }
    // A machine without a stored login has nothing to run as. The commands
    // still show — they are part of what this machine *is* — but the button
    // says why it will not work instead of failing on the click.
    const runControl = qs("[data-card-run]", chip);
    if (!node.has_password || !node.ssh_address) {
      runControl.disabled = true;
      runControl.title = "This machine has no stored SSH login";
    }
    return chip;
  }));
  qs("[data-card-actions-empty]", item).hidden = actions.length > 0;

  fillCardEnv(item, node);
  fillCardInstances(item, node);
  fillHost(item, node);
  fillCloudCard(item, node);

  // The last output survives the three-second redraw: it lives in the
  // outputs map, and the card is rebuilt around it.
  const output = outputs.get(node.id);
  const pane = qs("[data-node-output]", item);
  pane.hidden = !output;
  if (output) {
    qs("[data-node-output-head]", pane).textContent = output.head;
    qs("[data-node-output-body]", pane).textContent = output.body;
  }
  return item;
}

// What the machine says about itself. Hidden entirely when sampling is off,
// because three empty bars on every card would be a page mostly about a
// feature nobody turned on.
//
// The numbers come with the node — the store attaches the latest sample the
// same way it attaches the latest check — so this draws and never fetches.
function fillHost(item, node) {
  const host = qs("[data-host]", item);
  if (!host) return;
  // The three meters live in the card's one metric grid and hide themselves;
  // this block is what is left — the load line, the hour of CPU, the
  // containers — and it goes when nothing is being sampled.
  // A machine with no login cannot answer CPU, memory or disk at all, so its
  // tiles go rather than standing empty — the same rule the monitors follow,
  // where unmeasurable is silence and not zero. A machine that has a login but
  // has not been sampled yet keeps them and shows a dash: guard is about to
  // fill them in.
  const sampled = !!node.stats_interval_seconds && !!node.ssh_address && !!node.has_password;
  for (const tile of qsa("[data-host-tile]", item)) tile.hidden = !sampled;
  for (const cell of qsa("[data-head-stat]", item)) cell.classList.toggle("lg:flex", sampled);
  if (!sampled) { host.hidden = true; return; }
  host.hidden = false;

  const stats = node.stats;
  const error = qs("[data-host-error]", item);
  const note = qs("[data-host-note]", item);
  error.textContent = stats?.error || "";
  qs("[data-host-uptime]", item).textContent = stats?.uptime_seconds
    ? `up ${uptimeText(stats.uptime_seconds)}`
    : "";
  qs("[data-host-load]", item).textContent = stats && !stats.error
    ? `load ${stats.load_1.toFixed(2)} ${stats.load_5.toFixed(2)} ${stats.load_15.toFixed(2)}` +
      (stats.cpu_count ? ` · ${stats.cpu_count} vCPU` : "")
    : "";

  // A first sample has no CPU percentage: the kernel counts since boot, so a
  // rate needs two readings. A dash says that; 0% would say "idle", which is
  // a different and wrong thing.
  meter(item, "cpu", stats?.has_cpu ? stats.cpu_percent : null, stats?.has_cpu ? `${Math.round(stats.cpu_percent)}%` : "—");
  meter(item, "mem", percent(stats?.mem_used_kb, stats?.mem_total_kb),
    stats?.mem_total_kb ? `${kb(stats.mem_used_kb)} / ${kb(stats.mem_total_kb)}` : "—");
  meter(item, "disk", percent(stats?.disk_used_kb, stats?.disk_total_kb),
    stats?.disk_total_kb ? `${kb(stats.disk_used_kb)} / ${kb(stats.disk_total_kb)}` : "—");

  // The row head says the same three numbers as percentages, because a list is
  // scanned down a column: "94" three rows apart is a comparison, and "7.7 GB /
  // 29 GB" is a sentence you have to read. The full figure is one click away,
  // and is the title here.
  headStat(item, "cpu", stats?.has_cpu ? stats.cpu_percent : null,
    stats?.has_cpu ? `${Math.round(stats.cpu_percent)}% of ${stats.cpu_count || "?"} vCPU` : "not measured yet");
  headStat(item, "mem", percent(stats?.mem_used_kb, stats?.mem_total_kb),
    stats?.mem_total_kb ? `${kb(stats.mem_used_kb)} / ${kb(stats.mem_total_kb)}` : "no reading");
  headStat(item, "disk", percent(stats?.disk_used_kb, stats?.disk_total_kb),
    stats?.disk_total_kb ? `${kb(stats.disk_used_kb)} / ${kb(stats.disk_total_kb)}` : "no reading");

  cpuStrip(qs("[data-host-strip]", item), node.cpu_history || []);

  const containers = qs("[data-host-containers]", item);
  containers.replaceChildren(...(stats?.containers || []).map((container) => {
    const chip = el("span",
      `cn-badge inline-flex w-fit items-center gap-1 whitespace-nowrap ${container.up ? "border-primary/40 bg-primary/15 text-primary" : "cn-badge-variant-destructive"}`,
      container.name);
    chip.title = `${container.image || ""} · ${container.status}`.trim();
    return chip;
  }));
  // "No docker here" and "this login cannot reach the socket" are different
  // sentences, and the second one is a thing to go and fix.
  note.textContent = stats && !stats.error && !stats.containers?.length ? stats.docker_error || "" : "";
}

// One of the three machine figures in the row head. A machine that cannot
// answer loses the column rather than showing a dash: the whole point of the
// alignment is that a gap means something.
function headStat(item, key, value, detail) {
  const cell = qs(`[data-head-stat="${key}"]`, item);
  if (!cell) return;
  const node = qs(`[data-head-value="${key}"]`, cell);
  if (value === null || value === undefined) {
    node.textContent = "—";
  } else {
    node.textContent = `${Math.round(value)}%`;
    // The one colour on the row: at nine tenths of anything, the number is the
    // reason somebody opened this page.
    node.className = value >= 90
      ? "font-mono text-sm tabular-nums text-destructive"
      : "font-mono text-sm tabular-nums";
  }
  cell.title = detail || "";
}

function percent(used, total) {
  if (!total) return null;
  return (used / total) * 100;
}

function meter(item, key, share, label) {
  const value = qs(`[data-host-value="${key}"]`, item);
  const bar = qs(`[data-host-bar="${key}"]`, item);
  if (!value || !bar) return;
  value.textContent = label;
  bar.style.width = share === null ? "0%" : `${Math.min(100, Math.max(0, share))}%`;
  // Past four fifths the bar stops being decoration: what happens next is the
  // machine running out of the thing it is measuring.
  bar.className = share !== null && share >= 80
    ? "h-full rounded-full bg-destructive"
    : share !== null && share >= 60
      ? "h-full rounded-full bg-warning"
      : "h-full rounded-full bg-primary";
}

function kb(value) {
  if (!value) return "0";
  const units = ["KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit++; }
  return `${size >= 10 || unit === 0 ? Math.round(size) : size.toFixed(1)} ${units[unit]}`;
}

function uptimeText(seconds) {
  const days = Math.floor(seconds / 86400);
  if (days >= 1) return `${days}d`;
  const hours = Math.floor(seconds / 3600);
  if (hours >= 1) return `${hours}h`;
  return `${Math.max(1, Math.floor(seconds / 60))}m`;
}

// The last hour of CPU. A gap — a sample with no percentage, or one guard
// could not take — draws as an empty slot rather than a zero-height bar,
// because "nothing was measured" and "the machine was idle" are the two
// answers this strip most needs to keep apart.
function cpuStrip(host, history) {
  if (!host) return;
  host.replaceChildren(...history.slice(-60).map((percent) => {
    const bar = el("span", "w-full rounded-[1px]");
    if (percent < 0) {
      bar.style.height = "100%";
      bar.style.background = "var(--muted)";
      return bar;
    }
    bar.style.height = `${Math.max(6, Math.min(100, percent))}%`;
    bar.style.background = percent >= 80 ? "var(--destructive)" : "var(--primary)";
    return bar;
  }));
}

async function sampleNow(card, nodeID) {
  const note = qs("[data-host-note]", card);
  if (note) note.textContent = "Asking the machine…";
  try {
    await request("/api/cluster/stats", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ node_id: nodeID }),
    });
    // The answer is already stored against the node, so the ordinary refresh
    // is what draws it — one path to the numbers rather than two.
    await refreshCluster();
  } catch (failure) {
    const error = qs("[data-host-error]", card);
    if (error) error.textContent = failure.message;
    if (note) note.textContent = "";
  }
}

// Open or shut one card's command list. Only that: the status, the address,
// the uptime and the tags are why the card is on screen, and folding them
// away would leave a row of names.
// Open or shut one row. The chevron turns, and the state is on the button for
// anything reading the page rather than looking at it.
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
  const node = {
    name: qs('[data-node="name"]', form).value.trim(),
    domain: qs('[data-node="domain"]', form).value.trim(),
    health_path: qs('[data-node="health"]', form).value.trim(),
    ssh_address: qs('[data-node="ssh"]', form).value.trim(),
    interval_seconds: Number(qs('[data-node="interval"]', form).value) || 3,
  };
  // Only when there is one. An empty string means "forget the password", which
  // is a different request from "there was never one".
  const password = qs('[data-node="password"]', form).value;
  if (password) node.password = password;
  return node;
}

async function addNode(form) {
  const status = qs("[data-cluster-status]", form);
  const node = readForm(form);
  // A login is proved before the machine is stored, which means this can take a
  // few seconds — and a form that looks idle while it does is a form somebody
  // submits twice.
  status.textContent = node.ssh_address ? "Connecting over SSH…" : "Saving…";
  const submit = qs("button[type=submit]", form);
  if (submit) submit.disabled = true;
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
  } finally {
    if (submit) submit.disabled = false;
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
      domain: node.domain || "",
      internal_url: node.internal_url || "",
      health_path: node.health_path || "",
      ssh_address: node.ssh_address || "",
      url: node.url,
      enabled: node.enabled,
      locked: node.locked,
      interval_seconds: node.interval_seconds,
      // Always sent, because the store writes both columns on every save:
      // left out, pausing a machine would quietly strip its labels and drop it
      // out of its group.
      tags: node.tags || [],
      group: node.group || "",
      ...changes,
    }),
  });
  // The fleet just changed, so what is stored is wrong: dropped rather than
  // drawn for a frame before the refetch corrects it.
  forget("cluster.fleet");
  forget("cluster.nodes");
  await refreshCluster();
}

// Save the panel: the addresses, the login, and the password only if somebody
// actually typed one. A save that always sent the password field would send the
// dots, which is how a stored password becomes the literal string "••••".
async function saveDetail(panel) {
  const item = panel.closest("[data-node-id]");
  const id = item.dataset.nodeId;
  const field = (name) => qs(`[data-node-field="${name}"]`, panel).value.trim();
  const changes = {
    domain: field("domain"),
    // Cleared on the way past: the address and the health path are one field
    // now, and a leftover internal address would go on being probed instead.
    internal_url: "",
    health_path: field("health_path"),
    group: field("group"),
    ssh_address: field("ssh_address"),
    // A number, and zero means off — so it is read as one rather than sent as
    // the string an <input> hands over, which the API would reject.
    stats_interval_seconds: Number(field("stats_interval")) || 0,
    tags: readTags(panel),
  };
  const password = qs("[data-node-password]", panel);
  if (password.dataset.changed) changes.password = password.value;
  // Cleared before the write, not after: the refresh that follows redraws the
  // panel, and a panel still marked dirty would skip its own redraw and go on
  // showing what was typed rather than what was stored.
  dirty.delete(Number(id));
  if (changes.ssh_address && changes.password !== undefined) say(panel, "Connecting over SSH…");
  await updateNode(id, changes);
  say(panel, "Saved.");
}

async function saveActions(panel) {
  const item = panel.closest("[data-node-id]");
  const id = Number(item.dataset.nodeId);
  const actions = qsa("[data-action]", panel).map((row) => ({
    id: Number(row.dataset.actionId) || 0,
    name: qs('[data-action-field="name"]', row).value.trim(),
    command: qs('[data-action-field="command"]', row).value.trim(),
    schedule: qs('[data-action-field="schedule"]', row).value.trim(),
    // Typed in minutes, stored in seconds. Empty is not zero-with-a-meaning:
    // it is nobody watching this command, which is the default.
    stale_after_seconds: Math.round(Number(qs('[data-action-field="stale"]', row).value || 0) * 60),
  })).filter((action) => action.name || action.command);
  dirty.delete(id);
  const saved = await request("/api/cluster/actions", {
    method: "PUT",
    headers: adminHeaders(),
    body: JSON.stringify({ node_id: id, actions }),
  });
  // Refilled from the answer, here and now, rather than left to the next
  // three-second redraw. That redraw is skipped while the cursor is inside the
  // panel — which it always is, because saving is the click after typing — so
  // a new action would otherwise sit there with its Run button disabled until
  // somebody reloaded the page. Now it has its id, so it can be run.
  const node = nodes.find((candidate) => candidate.id === id);
  if (node) {
    node.actions = saved;
    fillActions(panel, node);
  }
  say(panel, "Saved.");
  await refreshCluster();
}

// Run one stored action, or test the connection. Both come back as the same
// shape — output, exit code, how long it took — because to the person reading
// it they are the same question asked twice.
async function run(panel, body, path, label) {
  const item = panel.closest("[data-node-id]");
  const id = Number(item.dataset.nodeId);
  show(panel, id, `${label} — running…`, "");
  try {
    const result = await request(path, { method: "POST", headers: adminHeaders(), body: JSON.stringify(body) });
    const took = `${number.format(Math.round(result.duration_ms))} ms`;
    const ending = result.error
      ? `failed · ${result.error}`
      : result.exit_code === 0 ? `exit 0 · ${took}` : `exit ${result.exit_code} · ${took}`;
    show(panel, id, `${label} — ${ending}`,
      (result.output || "(no output)") + (result.truncated ? "\n… output truncated" : ""));
    await refreshCluster();
  } catch (failure) {
    show(panel, id, `${label} — ${failure.message}`, "");
  }
}

// What has run on this machine lately — scheduled or pressed — on the same
// pane a run's output lands on. The tracker is a list, not a dashboard: a
// scheduled command that has been failing since Tuesday says so in the fifth
// column of five lines, which is the whole thing anybody needs from it.
async function runHistory(card, id) {
  show(card, id, "History — loading…", "");
  const runs = await request(`/api/cluster/runs?node=${id}&limit=25`, { headers: adminHeaders() });
  if (!runs.length) {
    show(card, id, "History — nothing has run on this machine yet", "");
    return;
  }
  const lines = runs.map((entry) => {
    const when = new Date(entry.ran_at).toLocaleString();
    const outcome = entry.outcome || (entry.error || entry.exit_code ? "failed" : "ok");
    // A skip took no time, and a run that never connected took none worth
    // printing: "0 ms" reads as a command that finished instantly.
    const took = entry.duration_ms ? `${number.format(Math.round(entry.duration_ms))} ms` : "—";
    const why = entry.error ? ` · ${entry.error}` : "";
    return [
      when,
      entry.action_name || "(removed command)",
      entry.trigger || "manual",
      outcome,
      took,
    ].join("  ") + why;
  });
  show(card, id, `History — the last ${runs.length} run${runs.length === 1 ? "" : "s"}`, lines.join("\n"));
}

function show(panel, id, head, body) {
  outputs.set(id, { head, body });
  const pane = qs("[data-node-output]", panel);
  if (!pane) return;
  pane.hidden = false;
  qs("[data-node-output-head]", pane).textContent = head;
  qs("[data-node-output-body]", pane).textContent = body;
}

// Whatever a panel has to say goes on its own output pane rather than the
// status line at the top of the page: with five machines listed, a message
// about one of them printed above all five is a message about none of them.
function say(panel, message) {
  const item = panel.closest("[data-node-id]");
  show(panel, Number(item.dataset.nodeId), message, outputs.get(Number(item.dataset.nodeId))?.body || "");
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
  if (!node) return;
  const locked = node.locked;
  const yes = await ask({
    title: `Stop watching ${node.name}?`,
    body: locked
      ? "This machine is locked, so deleting it is the only way to change its login or its commands — and it takes the uptime history, the pinned host key and every saved command with it."
      : "Its uptime history and its saved commands go with it.",
    confirm: "Delete",
    phrase: locked ? node.name : "",
  });
  if (!yes) return;
  await request(`/api/cluster/${id}`, { method: "DELETE", headers: adminHeaders() });
  expanded.delete(Number(id));
  dirty.delete(Number(id));
  outputs.delete(Number(id));
  await refreshCluster();
}

document.addEventListener("submit", (event) => {
  if (!event.target.matches("[data-cluster-form]")) return;
  event.preventDefault();
  addNode(event.target);
});

// Enter in the tag box adds the tag. Typing a word and pressing return is
// what everybody does to a field like this, and the alternative is a trip to
// a button two controls away for every label.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || !event.target.matches?.("[data-tag-input]")) return;
  event.preventDefault();
  const panel = event.target.closest("[data-node-detail]");
  if (panel) addTag(panel);
});

// Anything typed inside a panel holds off the three-second redraw until it is
// saved or the panel is closed. Without it, a password half-typed at the wrong
// moment is a password thrown away.
document.addEventListener("input", (event) => {
  const panel = event.target.closest?.("[data-node-detail]");
  if (!panel) return;
  dirty.add(Number(panel.closest("[data-node-id]").dataset.nodeId));
  if (event.target.matches("[data-node-password]")) event.target.dataset.changed = "1";
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

  // Ask one machine now rather than waiting out its cadence. No confirmation:
  // it is a fixed read-only command that changes nothing, which is the same
  // reason it is allowed on a locked machine.
  const sample = event.target.closest("[data-host-sample]");
  if (sample && !sample.disabled) {
    const card = sample.closest("[data-node-id]");
    sampleNow(card, Number(card.dataset.nodeId));
    return;
  }

  // A run from a card: the same confirmation and the same endpoint as the
  // settings panel, with the output landing on the card that asked.
  const cardRun = event.target.closest("[data-card-run]");
  if (cardRun && !cardRun.disabled) {
    const card = cardRun.closest("[data-node-id]");
    const chip = cardRun.closest("[data-card-action]");
    const actionID = Number(chip.dataset.actionId);
    const node = nodes.find((candidate) => String(candidate.id) === card.dataset.nodeId) || {};
    const name = qs("[data-card-action-name]", chip).textContent;
    const command = qs("[data-card-action-command]", chip).textContent;
    ask({
      title: `Run ${name} on ${node.name}?`,
      body: "This runs on the machine now, as the user in its SSH address.",
      detail: command,
      confirm: "Run it",
    }).then((yes) => {
      if (yes) run(card, { action_id: actionID }, "/api/cluster/run", name);
    });
    return;
  }

  // What has run on this machine lately.
  const historyButton = event.target.closest("[data-card-history]");
  if (historyButton) {
    const card = historyButton.closest("[data-node-id]");
    runHistory(card, Number(card.dataset.nodeId)).catch(reportOn(historyButton));
    return;
  }

  // Opening one row. The whole head line is the target, chevron included — a
  // 28-pixel arrow is a small thing to ask somebody to hit twenty times — but
  // not the controls inside it, or opening a machine and pressing a button on
  // it would be the same gesture.
  const cardHead = event.target.closest("[data-card-head]");
  if (cardHead && !event.target.closest("a, input, select, label, button:not([data-card-toggle])")) {
    const card = cardHead.closest("[data-node-id]");
    const id = Number(card.dataset.nodeId);
    const open = opened.delete(id) ? false : (opened.add(id), true);
    rememberOpened();
    showBody(card, open);
    return;
  }

  // The chevron, or anywhere on the row that is not itself a control: a
  // 28-pixel arrow is a small target, and on a narrow card it is the first
  // thing to be pushed out of reach.
  const head = event.target.closest("[data-node-head]");
  const toggle = head && !event.target.closest("button:not([data-node-toggle]), input, a, label, select")
    ? head
    : null;
  if (toggle) {
    const item = toggle.closest("[data-node-id]");
    const id = Number(item.dataset.nodeId);
    const panel = qs("[data-node-detail]", item);
    if (expanded.delete(id)) {
      // Closing drops whatever was typed and not saved — which is why it is a
      // deliberate click rather than something a refresh can do.
      dirty.delete(id);
      panel.hidden = true;
      turn(item, false);
      const node = nodes.find((candidate) => candidate.id === id);
      if (node) detail(item, node);
    } else {
      expanded.add(id);
      panel.hidden = false;
      turn(item, true);
      // Filled on the way open, not only on the next redraw. Most of the panel
      // is written on every render whether it is showing or not, but the parts
      // that cost a request — the cloud account's instances — are asked for
      // only when somebody is looking, and this is the moment they start.
      const node = nodes.find((candidate) => candidate.id === id);
      if (node) detail(item, node);
    }
    return;
  }

  const panel = event.target.closest("[data-node-detail]");
  if (panel) {
    handlePanel(event, panel);
    return;
  }

  const pause = event.target.closest("[data-node-pause]");
  if (pause) {
    const id = pause.closest("[data-node-id]").dataset.nodeId;
    const node = nodes.find((item) => String(item.id) === String(id));
    updateNode(id, { enabled: !node?.enabled }).catch(reportOn(pause));
    return;
  }
  const copy = event.target.closest("[data-node-duplicate]");
  if (copy) {
    duplicateNode(copy.closest("[data-node-id]").dataset.nodeId).catch(reportOn(copy));
    return;
  }
  const remove = event.target.closest("[data-node-delete]");
  if (remove) {
    deleteNode(remove.closest("[data-node-id]").dataset.nodeId).catch(reportOn(remove));
  }
});

// Copy a machine's shape onto a new one — the address, the cadence, the
// commands. Not the login: a password proved against one box proves nothing
// about another, and every stored login here is one that worked at least once.
async function duplicateNode(id) {
  const node = nodes.find((item) => String(item.id) === String(id));
  const copied = await request("/api/cluster/duplicate", {
    method: "POST",
    headers: adminHeaders(),
    body: JSON.stringify({ node_id: Number(id) }),
  });
  // Opened straight away, because a copy is not finished: it is paused, it has
  // no login, and it is pointing at the machine it was copied from.
  expanded.add(copied.id);
  await refreshCluster();
  const status = qs("[data-cluster-status]");
  if (status) {
    status.textContent = `Copied ${node?.name || "that machine"} to ${copied.name}. It is paused, and it has no login yet.`;
  }
}

function handlePanel(event, panel) {
  const item = panel.closest("[data-node-id]");
  const id = Number(item.dataset.nodeId);
  const node = nodes.find((candidate) => candidate.id === id) || {};

  if (event.target.closest("[data-node-save]")) {
    saveDetail(panel).catch((failure) => say(panel, failure.message));
    return;
  }

  // Changing a password means typing the new one in full. There is nothing to
  // edit in place: the box is showing dots, not a value.
  const change = event.target.closest("[data-node-password-edit]");
  if (change) {
    const password = qs("[data-node-password]", panel);
    password.readOnly = false;
    password.value = "";
    password.placeholder = "the new password";
    password.dataset.changed = "1";
    password.focus();
    dirty.add(id);
    return;
  }

  const swatch = event.target.closest("[data-tag-swatch]");
  if (swatch) {
    panel.dataset.tagColour = swatch.dataset.tagSwatch;
    fillTagEditorSwatches(panel);
    return;
  }

  if (event.target.closest("[data-tag-add]")) {
    addTag(panel);
    return;
  }

  const removeTag = event.target.closest("[data-tag-remove]");
  if (removeTag) {
    removeTag.closest("[data-card-tag]").remove();
    dirty.add(id);
    say(panel, "Removed — press Save tags to keep it that way.");
    return;
  }

  if (event.target.closest("[data-node-ssh-test]")) {
    run(panel, { node_id: id }, "/api/cluster/ssh", "Connection test");
    return;
  }

  const lock = event.target.closest("[data-node-lock]");
  if (lock) {
    // Typed, because this one cannot be undone from anywhere in the product —
    // not this page, not another tab, not the API — and the only way past it is
    // deleting the machine.
    ask({
      title: `Lock ${node.name}?`,
      body: "The SSH address, the password and the list of commands are frozen for good: nothing can be added, edited or removed, here or through the API. The commands that exist can still be run. The only way back is deleting this machine.",
      confirm: "Lock forever",
      phrase: node.name,
    }).then((yes) => {
      if (yes) updateNode(id, { locked: true }).catch((failure) => say(panel, failure.message));
    });
    return;
  }

  if (event.target.closest("[data-action-add]")) {
    const template = qs("[data-action-row-template]");
    qs("[data-action-rows]", panel).append(actionRow(template, {}, false));
    const empty = qs("[data-action-empty]", panel);
    if (empty) empty.hidden = true;
    dirty.add(id);
    return;
  }

  if (event.target.closest("[data-actions-save]")) {
    saveActions(panel).catch((failure) => say(panel, failure.message));
    return;
  }

  const runner = event.target.closest("[data-action-run]");
  if (runner) {
    const row = runner.closest("[data-action]");
    const actionID = Number(row.dataset.actionId);
    const name = qs('[data-action-field="name"]', row).value.trim() || "this action";
    const command = qs('[data-action-field="command"]', row).value.trim();
    if (!actionID) {
      say(panel, "Save this action before running it.");
      return;
    }
    // The command in full, because "Reboot" is a label somebody chose and the
    // line underneath it is what actually happens.
    ask({
      title: `Run ${name} on ${node.name}?`,
      body: "This runs on the machine now, as the user in its SSH address.",
      detail: command,
      confirm: "Run it",
    }).then((yes) => {
      if (yes) run(panel, { action_id: actionID }, "/api/cluster/run", name);
    });
  }
}

// ask opens the one dialog on this page and resolves true if it was confirmed.
//
// A native <dialog>, so Escape, the focus trap and the inert background come
// from the browser. `phrase` makes it a typed confirmation: the button stays
// disabled until the machine's name is typed in full, which is what turns an
// irreversible act from something you click into something you notice.
export function ask({ title, body, detail = "", confirm: label = "Confirm", phrase = "" }) {
  const dialog = qs("[data-confirm]");
  // No dialog on this page — the overview draws the same rows without it — so
  // fall back rather than silently doing nothing.
  if (!dialog) return Promise.resolve(window.confirm(`${title}\n\n${body}`));

  qs("[data-confirm-title]", dialog).textContent = title;
  qs("[data-confirm-body]", dialog).textContent = body;
  qs("[data-confirm-detail]", dialog).textContent = detail;
  const ok = qs("[data-confirm-ok]", dialog);
  ok.textContent = label;

  const typed = qs("[data-confirm-typed]", dialog);
  const input = qs("[data-confirm-input]", dialog);
  typed.hidden = !phrase;
  input.value = "";
  qs("[data-confirm-phrase]", dialog).textContent = phrase;
  ok.disabled = !!phrase;
  input.oninput = () => { ok.disabled = input.value.trim() !== phrase; };

  dialog.showModal();
  if (phrase) input.focus();
  return new Promise((resolve) => {
    dialog.addEventListener("close", () => {
      // returnValue is the value of the button that submitted the form, and is
      // "" when the dialog was dismissed with Escape.
      resolve(dialog.returnValue === "ok" && (!phrase || input.value.trim() === phrase));
    }, { once: true });
  });
}

// A failed write has to say so somewhere the reader is already looking — most
// often it is a missing admin token, which is fixable but invisible.
function reportOn(node) {
  return (failure) => {
    const status = qs("[data-cluster-status]") || qs("[data-cluster-summary]");
    if (status) status.textContent = failure.message;
    else node.title = failure.message;
  };
}

// A machine's environment: a box of KEY=value lines, saved here and injected onto
// the machine.
//
// Two presses, and the difference between them is the whole feature. **Save**
// stores the variables in guard and touches nothing — it is somebody's intent,
// typed once, and a locked machine can still be edited. **Inject** writes them to
// the box: /etc/environment and a systemd drop-in, so everything on that machine
// that takes an environment takes the same one. That press is the one the lock
// refuses, and the one that asks first.
//
// The text is parsed on the server with the same dialect the vault's .env import
// uses, so nothing here has to know what a quoted multi-line value looks like.
function fillEnv(panel, node) {
  const box = qs("[data-env-text]", panel);
  if (!box) return;
  const state = node.env || {};
  const line = qs("[data-env-state]", panel);
  if (line) {
    const saved = realStamp(state.saved_at);
    const injected = realStamp(state.injected_at);
    line.textContent = !state.count
      ? "nothing saved yet"
      : [
        `${state.count} ${state.count === 1 ? "variable" : "variables"}`,
        saved ? `saved ${relativeTime(saved)}` : "",
        // The one thing this line exists to say: what is stored is not what the
        // machine has.
        injected ? `on the machine ${relativeTime(injected)}` : "never injected",
      ].filter(Boolean).join(" · ");
    line.className = envPending(state)
      ? "text-[.65rem] text-warning empty:hidden"
      : "text-[.65rem] text-muted-foreground empty:hidden";
  }
  // Loaded once per open row and then left alone: the values are not in the
  // machine list, and refetching under somebody typing would replace the box
  // mid-word.
  if (box.dataset.loaded === String(node.id)) return;
  box.dataset.loaded = String(node.id);
  box.value = "";
  request(`/api/cluster/env?node_id=${node.id}`, { headers: adminHeaders() })
    .then((answer) => {
      if (box.dataset.dirty === "true") return;
      // The server renders it: a value with a comment marker or a quote in it
      // has to come back the way a save will read it, and that is the same
      // function that writes the file.
      box.value = answer.text || "";
    })
    .catch((failure) => { qs("[data-env-skipped]", panel).textContent = failure.message; });
  const inject = qs("[data-env-inject]", panel);
  if (inject) inject.hidden = !!node.locked;
}

function envPending(state) {
  if (!state.count) return false;
  const injected = realStamp(state.injected_at);
  const saved = realStamp(state.saved_at);
  return !injected || (saved && injected < saved);
}

async function saveEnv(panel) {
  const item = panel.closest("[data-node-id]");
  const id = Number(item.dataset.nodeId);
  const box = qs("[data-env-text]", panel);
  const skipped = qs("[data-env-skipped]", panel);
  dirty.delete(id);
  box.dataset.dirty = "false";
  const answer = await request("/api/cluster/env", {
    method: "PUT",
    headers: adminHeaders(),
    body: JSON.stringify({ node_id: id, text: box.value }),
  });
  // Redrawn from what was stored, not from what was typed: a line guard could
  // not read is named here rather than silently dropped, and the box then shows
  // exactly what the machine will be given.
  box.value = answer.text || "";
  skipped.textContent = (answer.skipped || []).length
    ? `Not saved: ${answer.skipped.map((line) => `line ${line.line} (${line.reason})`).join(", ")}`
    : "";
  const node = nodes.find((candidate) => candidate.id === id);
  if (node) node.env = answer.state;
  say(panel, `Saved ${answer.vars.length} ${answer.vars.length === 1 ? "variable" : "variables"} — press Inject to put them on the machine.`);
  forget("cluster");
}

// Put them on the machine. The request carries a node id and nothing else: the
// variables come from the database and the paths are fixed in the server, so there
// is no shape of this call that writes chosen content to a chosen place.
async function injectEnv(panel, node) {
  const count = node.env?.count || 0;
  const yes = await ask({
    title: `Inject the environment on ${node.name}?`,
    body: "Writes /etc/environment and a systemd drop-in, keeping the previous copies as .guard-bak. Services already running keep their old environment until they are restarted.",
    detail: `${count} ${count === 1 ? "variable" : "variables"}`,
    confirm: "Inject it",
  });
  if (!yes) return;
  say(panel, "Injecting…");
  const answer = await request("/api/cluster/env/inject", {
    method: "POST",
    headers: adminHeaders(),
    body: JSON.stringify({ node_id: node.id }),
  });
  node.env = answer.state;
  show(panel, node.id, `Injected ${answer.count} ${answer.count === 1 ? "variable" : "variables"} — restart a service to pick them up`, answer.output || "");
  forget("cluster");
}

// One line on the operational card: what the machine's environment is, and the
// same press. Editing it stays in settings, where things are declared.
function fillCardEnv(item, node) {
  const wrap = qs("[data-card-env-wrap]", item);
  if (!wrap) return;
  const state = node.env || {};
  wrap.hidden = !state.count;
  if (!state.count) return;
  const note = qs("[data-card-env-note]", item);
  const injected = realStamp(state.injected_at);
  note.textContent = [
    `${state.count} ${state.count === 1 ? "variable" : "variables"}`,
    injected ? `on the machine ${relativeTime(injected)}` : "never injected",
    envPending(state) ? "· saved since" : "",
  ].filter(Boolean).join(" · ");
  note.className = envPending(state) ? "truncate text-xs text-warning" : "truncate text-xs text-muted-foreground";
  const inject = qs("[data-env-inject]", item);
  if (inject) {
    // A machine with no login has nothing to write with, and a locked one takes
    // nothing: the button says why rather than failing on the press.
    const why = node.locked ? "This machine is locked" : !node.has_password || !node.ssh_address ? "This machine has no stored SSH login" : "";
    inject.disabled = !!why;
    inject.title = why;
  }
}

document.addEventListener("input", (event) => {
  const box = event.target.closest("[data-env-text]");
  if (!box) return;
  box.dataset.dirty = "true";
  dirty.add(Number(box.closest("[data-node-id]").dataset.nodeId));
});

document.addEventListener("click", (event) => {
  const save = event.target.closest("[data-env-save]");
  if (save) {
    const panel = save.closest("[data-node-id]");
    saveEnv(panel).catch((failure) => say(panel, failure.message));
    return;
  }
  const inject = event.target.closest("[data-env-inject]");
  if (inject && !inject.disabled) {
    const item = inject.closest("[data-node-id]");
    const node = nodes.find((candidate) => String(candidate.id) === item.dataset.nodeId);
    if (node) injectEnv(item, node).catch((failure) => say(item, failure.message));
  }
});
