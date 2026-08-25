// The cluster: machines guard watches from the outside, and — for the ones that
// gave guard a way in — the commands somebody keeps for them.
//
// Two surfaces, one renderer. The settings page lists them with controls; the
// overview shows the same rows with the controls hidden by CSS, because a
// second near-identical template drifts the first time a column is added.

import { adminHeaders, el, number, qs, qsa, relativeTime, request, setOutput, text } from "./core.js";
import { ensure as swr, forget, set as remember } from "./store.js";
// The cloud half of a machine — the provider strip, the link, the snapshots.
// It imports `ask` back from here, which is a cycle ES modules are fine with
// so long as nothing is called while the modules are still evaluating: both
// sides only ever reach across inside a function.
import { fillCloudCard, fillCloudDetail, powerStatus } from "./cloud.js";

const tones = {
  up: "border-primary/40 bg-primary/15 text-primary",
  down: "cn-badge-variant-destructive",
  unknown: "cn-badge-variant-secondary",
  paused: "border-warning/40 bg-warning/15 text-warning",
  running: "border-primary/40 bg-primary/15 text-primary",
  stopped: "cn-badge-variant-destructive",
  pending: "cn-badge-variant-secondary",
  unlinked: "cn-badge-variant-secondary",
};

const colours = {
  up: "var(--primary)",
  down: "var(--destructive)",
  unknown: "var(--muted-foreground)",
  running: "var(--primary)",
  stopped: "var(--destructive)",
  pending: "var(--muted-foreground)",
  unlinked: "var(--muted-foreground)",
};

function fillPowerStatus(item, node) {
  const status = powerStatus(node);
  const dot = qs("[data-node-dot]", item);
  if (dot) {
    dot.style.background = colours[status.state] || colours.unknown;
    dot.title = status.label;
  }
  const badge = qs("[data-node-badge]", item);
  if (badge) {
    badge.className = `cn-badge inline-flex w-fit shrink-0 items-center justify-center whitespace-nowrap ${tones[status.state] || tones.unknown}`;
    badge.textContent = status.label;
  }
  return status;
}

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

function hostOf(raw) {
  try {
    return new URL(raw).host;
  } catch {
    return raw;
  }
}

function renderStat() {
  for (const stat of qsa('[data-stat="cluster"]')) {
    const linked = nodes.filter((node) => node.provider_instance_id);
    stat.textContent = linked.length
      ? `${linked.filter((node) => powerStatus(node).state === "running").length}/${linked.length}`
      : "—";
  }
}

// The machines dialog is a checkbox, so "is it open" is one property read.
// A page with no dialog — anything that shows the rows directly — has no
// checkbox, and then the rows ARE the page and always render.
const MACHINES_PANEL = "machine-dialog";

function machinesPanelOpen() {
  const toggle = document.getElementById(MACHINES_PANEL);
  return !toggle || toggle.checked;
}

// Opening it cannot wait for the next tick: the panel would come up holding
// whatever the last render left, or nothing at all on a first open.
document.addEventListener("change", (event) => {
  if (event.target && event.target.id === MACHINES_PANEL && event.target.checked) render();
});

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
  // The editable rows live in a dialog that is shut almost all the time. Each
  // one is a large subtree, this runs every three seconds, and a row built into
  // a display:none panel is work that can only ever cost — the grouped cards
  // are what this page is for. Opening the dialog renders immediately, so
  // nothing on screen is ever stale.
  if (machinesPanelOpen()) {
    for (const host of qsa("[data-cluster-rows]")) {
      host.replaceChildren(...nodes.map(row).filter(Boolean));
    }
  }
  for (const node of qsa("[data-cluster-empty]")) node.hidden = nodes.length > 0;
  renderCards();

  const summary = qs("[data-cluster-summary]");
  if (summary) {
    const states = nodes.map((node) => powerStatus(node).state);
    const running = states.filter((state) => state === "running").length;
    const stopped = states.filter((state) => state === "stopped").length;
    const unlinked = states.filter((state) => state === "unlinked").length;
    const unreadable = states.length - running - stopped - unlinked;
    summary.textContent = nodes.length ? [
      `${running} running`,
      stopped ? `${stopped} stopped` : "",
      unreadable ? `${unreadable} pending` : "",
      unlinked ? `${unlinked} not linked` : "",
    ].filter(Boolean).join(" · ") : "No machines yet";
  }
  // The overview's stat tile is powered-on over provider-linked machines.
  // Unlinked machines have no power claim to include in either side.
  for (const stat of qsa('[data-stat="cluster"]')) {
    const linked = nodes.filter((node) => node.provider_instance_id);
    stat.textContent = linked.length
      ? `${linked.filter((node) => powerStatus(node).state === "running").length}/${linked.length}`
      : "—";
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

  fillPowerStatus(item, node);
  qs("[data-node-name]", item).textContent = node.name;

  const lockBadge = qs("[data-node-lock-badge]", item);
  if (lockBadge) {
    lockBadge.hidden = !node.locked;
    lockBadge.textContent = "locked";
    lockBadge.title = "Locked: the login is frozen and no command can be added, edited or removed";
  }

  const icon = qs("[data-node-icon]", item);
  if (icon && node.has_icon) {
    icon.src = `/api/cluster/icon/${node.id}`;
    icon.hidden = false;
    // The bytes were an image when guard stored them; a broken one now means
    // the node changed under us, and an alt box is worse than the dot alone.
    icon.onerror = () => { icon.hidden = true; };
  }

  fillTags(item, node.tags);

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
  field("group").value = node.group || "";
  field("ssh_address").value = node.ssh_address || "";
  field("stats_interval").value = node.stats_interval_seconds ?? 0;

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
      setOutput(qs("[data-node-output-body]", pane), output.body);
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
  const states = members.map((node) => powerStatus(node).state);
  const running = states.filter((state) => state === "running").length;
  const stopped = states.filter((state) => state === "stopped").length;
  const unlinked = states.filter((state) => state === "unlinked").length;
  row.append(el("h2", "text-sm font-semibold tracking-tight", group || "Ungrouped"));
  row.append(el("span", "text-xs text-muted-foreground",
    `${members.length} machine${members.length === 1 ? "" : "s"}`));
  if (stopped) row.append(el("span", "text-xs font-medium text-destructive", `${stopped} stopped`));
  if (unlinked) row.append(el("span", "text-xs text-muted-foreground", `${unlinked} not linked`));
  if (running === members.length) row.append(el("span", "text-xs text-muted-foreground", "all running"));
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

  fillPowerStatus(item, node);
  // The name is the way in to the machine's own page. A link rather than the
  // whole row, because the row is a disclosure — clicking it opens the fold, and
  // a row that navigated instead would take away the quick look.
  const name = qs("[data-node-name]", item);
  name.replaceChildren();
  const nameLink = el("a", "truncate font-medium hover:underline", node.name);
  nameLink.href = `/cluster/${node.id}`;
  // data-nav-link is what closes the mobile drawer on navigation, and what marks
  // this as one of guard's own links rather than an outbound one.
  nameLink.dataset.navLink = "true";
  name.append(nameLink);

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

  fillTags(item, node.tags);

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
    setOutput(qs("[data-node-output-body]", pane), output.body);
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
    group: qs('[data-node="group"]', form).value.trim(),
    ssh_address: qs('[data-node="ssh"]', form).value.trim(),
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
    status.textContent = `Added ${node.name}. Add any service URLs from Checks.`;
    await refreshCluster();
  } catch (failure) {
    status.textContent = failure.message;
  } finally {
    if (submit) submit.disabled = false;
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
      // Sent for the same reason as tags and group: the store writes the
      // column on every save, so a pause that left it out would quietly
      // unpublish the machine.
      public_name: node.public_name || "",
      public: !!node.public,
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
  setOutput(qs("[data-node-output-body]", pane), body);
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

document.addEventListener("change", (event) => {
  const select = event.target.closest?.("[data-instance-node]");
  if (select) {
    const row = select.closest("[data-service]");
    assign(row.dataset.service, row.dataset.instance, Number(select.value)).catch(reportOn(select));
  }
});

document.addEventListener("click", (event) => {
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

// The machine page: /cluster/{id}.
//
// It draws the same card the list draws — from the same template, through the same
// fillers — so a section added to a machine appears here without being written
// twice. What this page adds is what a list has no room for: a command line for
// this machine and a terminal under it.
//
// One machine is one request. The list endpoint could be filtered in the browser,
// but a page somebody keeps open on one box should not be re-reading forty
// machines, their host stats and their tags every three seconds.
export async function refreshMachine() {
  const host = qs("[data-machine-card]");
  const page = qs("[data-machine]");
  if (!host || !page) return;
  const id = Number(page.dataset.machine);
  if (!id) return;
  try {
    await swr(`cluster.machine.${id}`, () => request(`/api/cluster/${id}`), (node, cached) => {
      // The one machine this page is about lives in the same array every renderer
      // here reads, so Run, Inject and the cloud strip find it without being told.
      nodes = nodes.filter((candidate) => candidate.id !== node.id).concat(node);
      drawMachine(host, node, cached);
    });
  } catch (failure) {
    host.replaceChildren(el("p", "p-4 text-sm text-destructive", failure.message));
  }
}

function drawMachine(host, node, cached) {
  const template = qs("[data-cluster-card-template]");
  if (!template) return;
  // Typing guard, the same one the list has: this card carries an environment box
  // and a cadence field, and rebuilding it under somebody mid-word is the one
  // thing a three-second refresh must not do.
  if (host.dataset.drawn === String(node.id) && (typing() || dirty.size)) return;

  const card = cardFor(template, node);
  // Always open. A page about one machine that starts folded is a page that makes
  // you click to see what you came for.
  qs("[data-card-body]", card).hidden = false;
  const toggle = qs("[data-card-toggle]", card);
  if (toggle) toggle.hidden = true;
  const head = qs("[data-card-head]", card);
  if (head) head.classList.remove("cursor-pointer", "hover:bg-muted/30");
  host.replaceChildren(card);
  host.dataset.drawn = String(node.id);

  const title = qs("[data-machine-name]");
  if (title) title.textContent = node.name;
  const note = qs("[data-machine-note]");
  if (note) {
    const power = powerStatus(node).label;
    note.textContent = [
      node.group || "Ungrouped",
      power,
      node.locked ? "locked" : "",
      cached ? "from your last visit" : "",
    ].filter(Boolean).join(" · ");
  }
  const hint = qs("[data-machine-hint]");
  if (hint) {
    hint.textContent = node.locked
      ? "Locked — only this machine's stored commands can be run."
      : !node.has_password || !node.ssh_address
        ? "This machine has no stored SSH login."
        : "Runs as the user in this machine's SSH address. Every run is logged.";
  }
  const refused = !!node.locked || !node.has_password || !node.ssh_address;
  for (const control of qsa("[data-machine-run]")) {
    // Recorded on the element, because setRunning re-enables the button when a
    // block finishes and must not hand a locked machine a working Run button.
    control.dataset.refused = refused ? "1" : "0";
    control.disabled = refused;
  }
  // The arrow keys are only worth pressing if there is something behind them,
  // and the useful history belongs to the machine rather than to this tab.
  // Once per drawn machine: it is a request, and the list only grows by what
  // this page itself runs, which it appends as it goes.
  if (historyFor !== node.id) {
    historyFor = node.id;
    cwd = "";
    loadCommandHistory(node);
  }
  // The card's own output pane is redundant here — this page has a terminal — and
  // two panes showing different things is worse than one.
  const pane = qs("[data-node-output]", card);
  if (pane) pane.remove();
}

// ---------------------------------------------------------------------------
// The command line on /cluster/{id}.
//
// It was a form: one input, one output pane, replaced on every run. That is
// fine for `docker ps` and useless for the thing people actually do, which is
// run four commands and compare what the second said with what the fourth did.
// So the pane is a transcript, the box takes several lines, and the arrow keys
// walk what this machine has run before.
// ---------------------------------------------------------------------------

// What the machine has been asked to do, oldest first. Seeded from the server
// rather than from this tab, because the useful history is "what has been run
// on this box" — last week, from somebody else's browser — and a per-tab list
// is empty exactly when you reload to try again.
let commandHistory = [];
let historyFor = 0;      // Which machine the list belongs to, so a second page does not inherit it.

// Where you are. Each exec is its own SSH session, so a `cd` on one line is
// gone by the next — which reads as the terminal ignoring you: `cd ..`, `ls`,
// and the same listing again. So the directory is carried here and put back in
// front of the next command.
//
// It is tracked rather than assumed: every command is followed by a probe that
// prints $PWD, so `cd`, `pushd`, a script that changes directory and a failed
// `cd` all end up with the box agreeing with the machine. The probe is written
// as an OSC escape, which the pane's ANSI renderer already drops — so the thing
// that keeps this honest is invisible rather than a marker line somebody has to
// look past.
let cwd = "";
const CWD_MARK = "guard-cwd;";
let historyAt = -1;      // -1 is "not walking"; otherwise an index into the list.
let historyDraft = "";   // What was typed before ↑ was pressed, restored on the way back down.

// A transcript can be scrolled up to read; writing to it should not yank the
// reader back down. So it follows only when it was already at the bottom, which
// is what a terminal does.
function atBottom(pane) {
  return pane.scrollHeight - pane.scrollTop - pane.clientHeight < 24;
}

function terminalPane() {
  return qs("[data-machine-output]");
}

// head is the one-line status above the pane: what is running, or how the last
// thing ended.
function terminalHead(text) {
  const line = qs("[data-machine-output-head]");
  if (line) line.textContent = text;
}

// append puts one node at the end of the transcript and follows it if the
// reader was at the bottom.
function terminalAppend(node) {
  const pane = terminalPane();
  if (!pane) return;
  if (pane.dataset.empty !== "0") {
    pane.textContent = "";
    pane.dataset.empty = "0";
  }
  const follow = atBottom(pane);
  pane.append(node);
  // A transcript nobody trims is a tab that gets slower every command. Six
  // hundred blocks is far more than anybody scrolls back through and far less
  // than a page notices.
  while (pane.childElementCount > 600) pane.removeChild(pane.firstElementChild);
  if (follow) pane.scrollTop = pane.scrollHeight;
}

function terminalClear(message) {
  const pane = terminalPane();
  if (!pane) return;
  pane.textContent = message;
  pane.dataset.empty = "1";
  terminalHead("");
}

// echo writes the prompt line for a command, the way a terminal shows what you
// typed before it shows what happened.
function terminalEcho(command) {
  const line = el("div", "mt-2 first:mt-0");
  // The directory in the prompt, the way a shell puts it there: it is how you
  // see that a `cd` took, without running `pwd` to check.
  if (cwd) line.append(el("span", "select-none text-muted-foreground", `${cwd} `));
  line.append(el("span", "select-none text-primary", "$ "), el("span", "text-foreground", command));
  terminalAppend(line);
}

// result writes what came back: the output through the ANSI renderer, then a
// line saying how it ended.
//
// The output is `text-foreground` and the status is the dim one, which is the
// way round it should always have been — what the machine said is the thing you
// came for, and it was being drawn in the colour used for captions.
//
// A command that printed nothing says so. `ls` in an empty directory, `cd`, a
// successful `systemctl restart` — all of them return nothing at all, and a
// transcript that shows a prompt, then a status, and no sign that the gap is
// deliberate reads exactly like a terminal that is swallowing output.
//
// No duration. It was on every line and nobody was reading it: what a command
// took matters when something is slow, and then it is in the run history with
// everything else. A number repeated after every line is noise the eye has to
// step over to find the output.
function terminalResult(result) {
  if (result.output && result.output.trim() !== "") {
    const block = el("div", "text-foreground");
    setOutput(block, result.output);
    terminalAppend(block);
  } else if (!result.error) {
    terminalAppend(el("div", "text-[.65rem] italic text-muted-foreground", "(no output)"));
  }
  const failed = Boolean(result.error) || result.exit_code !== 0;
  const ending = result.error
    ? `failed — ${result.error}`
    : result.exit_code === 0
      ? "ok"
      : `exit ${result.exit_code}`;
  terminalAppend(el("div", `text-[.65rem] ${failed ? "text-destructive" : "text-muted-foreground"}`, ending));
  terminalHead(ending);
}

function terminalNote(text, tone = "text-muted-foreground") {
  terminalAppend(el("div", `text-[.65rem] ${tone}`, text));
}

// The lines to run, in order. Blank lines are skipped and a `#` comment is
// skipped too — pasting a block copied from a runbook should not fail on its
// own comments.
function commandLines(value) {
  return String(value || "")
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "" && !line.startsWith("#"));
}

// Run what is in the box, one line at a time.
//
// One request per line: POST /api/cluster/exec, the one endpoint in guard that
// takes a command rather than an action id. It is admin, refused on a locked
// machine, and every line is its own logged row — which is also why a `cd` on
// one line does not carry to the next, and why the hint under the box says so.
//
// It stops at the first line that fails, because a paste of four commands is
// usually four steps of one thing, and running step three against a failed step
// two is how a bad morning gets worse.
async function runCommand(node) {
  const input = qs("[data-machine-command]");
  const lines = commandLines(input.value);
  if (!lines.length) {
    terminalHead("Nothing to run");
    input.focus();
    return;
  }
  // No confirmation. A dialog in front of every command is the least
  // terminal-like thing a terminal can have, and it was in front of `ls` as
  // often as anything else — which is how a confirmation stops being read.
  // What actually guards this is unchanged and is not a dialog: the endpoint is
  // admin, a locked machine refuses it, and every line lands in the run log
  // with who and when.

  input.value = "";
  resizeCommandBox();
  historyAt = -1;
  historyDraft = "";
  setRunning(true);
  try {
    for (const [index, command] of lines.entries()) {
      rememberCommand(command);
      terminalEcho(command);
      terminalHead(`${command} — running…`);
      let result;
      try {
        result = await request("/api/cluster/exec", {
          method: "POST",
          headers: adminHeaders(),
          body: JSON.stringify({ node_id: node.id, command: withDirectory(command) }),
        });
      } catch (failure) {
        // A refusal — locked, no login, no permission — is the end of the
        // block, and it is the machine's answer rather than a line's output.
        terminalNote(failure.message, "text-destructive");
        terminalHead(failure.message);
        return;
      }
      readDirectory(result);
      terminalResult(result);
      const failed = Boolean(result.error) || result.exit_code !== 0;
      if (failed && index < lines.length - 1) {
        terminalNote(`stopped — ${lines.length - index - 1} command${lines.length - index - 1 === 1 ? "" : "s"} not run`, "text-destructive");
        return;
      }
    }
  } finally {
    setRunning(false);
    input.focus();
  }
}

// withDirectory puts the command back where you left off and asks where it
// ended up.
//
// Three parts, and the order is the whole of it: step into the remembered
// directory (quietly — a directory that has been deleted since must not stop
// the command), run the line, then keep its exit status while printing $PWD.
// Without that last `exit`, every command would report the status of the probe
// and nothing could ever fail.
//
// The first command of a session has no directory to restore, so it is sent as
// typed plus the probe. That keeps the ordinary one-line case readable in the
// run log, which is where somebody reads it back months later.
function withDirectory(command) {
  const probe = `__guard_status=$?; printf '\\033]${CWD_MARK}%s\\007' "$PWD"; exit $__guard_status`;
  const step = cwd ? `cd ${shellQuote(cwd)} 2>/dev/null\n` : "";
  return `${step}${command}\n${probe}`;
}

// One single-quoted shell word, the same rule internal/deploy uses: everything
// is literal inside single quotes, and the only thing that can end them is a
// single quote, which is closed, escaped and reopened.
function shellQuote(value) {
  return `'${String(value).replaceAll("'", `'\\''`)}'`;
}

// readDirectory takes the answer out of the output, and takes it *out* — the
// probe is removed from the string rather than left for the renderer to drop.
//
// Both would look the same on screen and only one of them can tell whether the
// command printed anything: a command with no output still comes back carrying
// the probe, so "did this print something" answered against the raw string is
// always yes, and `ls` in an empty directory loses its "(no output)" line and
// gains an empty block instead.
function readDirectory(result) {
  const pattern = new RegExp(`\\u001b\\]${CWD_MARK}([^\\u0007]*)\\u0007`);
  const output = String(result.output || "");
  const match = pattern.exec(output);
  if (match && match[1]) cwd = match[1];
  if (match) result.output = output.replace(pattern, "");
}

// The button says what it is doing, and a second Enter while a block is running
// must not start it again half way through.
function setRunning(running) {
  const control = qs("[data-machine-run]");
  const input = qs("[data-machine-command]");
  if (control) {
    control.disabled = running || control.dataset.refused === "1";
    control.textContent = running ? "Running…" : "Run";
  }
  if (input) input.readOnly = running;
}

// rememberCommand puts a command at the end of the history, without a duplicate
// of the one before it — a terminal does not fill its history with the command
// you pressed Up and Enter on four times.
//
// Not `remember`: that name is already the store's `set`, imported at the top
// of this file, and redeclaring it is a SyntaxError that takes the whole module
// with it — every page this file drives, not just this one.
function rememberCommand(command) {
  if (!command) return;
  if (commandHistory[commandHistory.length - 1] === command) return;
  commandHistory.push(command);
  if (commandHistory.length > 200) commandHistory.shift();
}

// Seeded from the machine's own run log: the exec rows carry the command they
// ran, so what somebody typed here last week is what ↑ offers today. Reversed
// because the API answers newest first and a history is walked backwards from
// the end.
async function loadCommandHistory(node) {
  try {
    const runs = await request(`/api/cluster/runs?node=${node.id}&limit=50`, { headers: adminHeaders() });
    const typed = runs
      .filter((entry) => !entry.action_id && entry.command)
      .map((entry) => entry.command)
      .reverse();
    commandHistory = [];
    for (const command of typed) rememberCommand(command);
  } catch {
    // No history is a usable command line; a failed page is not.
  }
}

// ↑ and ↓ walk it. The half-typed line is kept and handed back on the way past
// the end, which is the behaviour that makes the arrows safe to press.
function walkHistory(direction) {
  const input = qs("[data-machine-command]");
  if (!input || !commandHistory.length) return;
  if (historyAt === -1) {
    if (direction > 0) return;              // Down at the bottom does nothing.
    historyDraft = input.value;
    historyAt = commandHistory.length - 1;
  } else {
    historyAt += direction;
  }
  if (historyAt < 0) historyAt = 0;
  if (historyAt >= commandHistory.length) {
    historyAt = -1;
    input.value = historyDraft;
    resizeCommandBox();
    caretToEnd(input);
    return;
  }
  input.value = commandHistory[historyAt];
  resizeCommandBox();
  caretToEnd(input);
}

function caretToEnd(input) {
  const at = input.value.length;
  // After the value is painted, or the caret lands where the old text ended.
  requestAnimationFrame(() => input.setSelectionRange(at, at));
}

// The box grows to what is in it, up to the max-height the class sets, after
// which it scrolls. A four-line paste that shows one line is a paste you cannot
// check before running.
function resizeCommandBox() {
  const input = qs("[data-machine-command]");
  if (!input) return;
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight}px`;
}

// The pane selects and copies like a terminal, and this copies the whole of it —
// the reason to run `docker ps` from here is usually to paste a line of it
// somewhere else.
async function copyOutput() {
  const pane = terminalPane();
  if (!pane || !pane.textContent.trim()) return;
  try {
    await navigator.clipboard.writeText(pane.textContent);
    terminalHead("Copied.");
  } catch {
    // The clipboard is refused on an insecure origin, which is exactly where a
    // guard on a laptop lives. Select it instead of failing silently.
    const range = document.createRange();
    range.selectNodeContents(pane);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  }
}

// History is appended to the transcript rather than replacing it, for the same
// reason everything else is: looking up what ran yesterday should not cost you
// what you ran a minute ago.
async function machineHistory(node) {
  terminalHead("History — loading…");
  const runs = await request(`/api/cluster/runs?node=${node.id}&limit=25`, { headers: adminHeaders() });
  if (!runs.length) {
    terminalNote("Nothing has run on this machine yet.");
    terminalHead("History");
    return;
  }
  terminalNote(`— the last ${runs.length} run${runs.length === 1 ? "" : "s"} on this machine —`);
  for (const entry of runs) {
    const line = [
      new Date(entry.ran_at).toLocaleString(),
      entry.action_name || entry.command || "(removed command)",
      entry.trigger || "manual",
      entry.outcome || (entry.error || entry.exit_code ? "failed" : "ok"),
      entry.duration_ms ? `${number.format(Math.round(entry.duration_ms))} ms` : "—",
    ].join("  ") + (entry.error ? ` · ${entry.error}` : "");
    terminalNote(line);
  }
  terminalHead(`History — the last ${runs.length} run${runs.length === 1 ? "" : "s"}`);
}

function machineNode() {
  const page = qs("[data-machine]");
  if (!page) return null;
  return nodes.find((candidate) => candidate.id === Number(page.dataset.machine)) || null;
}

document.addEventListener("click", (event) => {
  if (!qs("[data-machine]")) return;
  const run = event.target.closest("[data-machine-run]");
  if (run && !run.disabled) {
    const node = machineNode();
    if (node) runCommand(node).catch((failure) => { setRunning(false); terminalNote(failure.message, "text-destructive"); terminalHead("Failed"); });
    return;
  }
  if (event.target.closest("[data-machine-copy]")) { copyOutput(); return; }
  if (event.target.closest("[data-machine-history]")) {
    const node = machineNode();
    if (node) machineHistory(node).catch((failure) => { terminalNote(failure.message, "text-destructive"); terminalHead("Failed"); });
    return;
  }
  if (event.target.closest("[data-machine-clear]")) terminalClear("Nothing has run from this page yet.");
});

document.addEventListener("keydown", (event) => {
  if (!event.target.matches?.("[data-machine-command]")) return;
  // Enter runs, because a command line where you have to reach for the mouse is
  // not one. Shift+Enter is the newline, which is how a block gets typed rather
  // than pasted.
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    const control = qs("[data-machine-run]");
    if (control && !control.disabled) control.click();
    return;
  }
  // The arrows walk the history — but only from a single-line box, because in a
  // block somebody is editing, Up means "the line above" and taking that away
  // would make multi-line editing impossible.
  if ((event.key === "ArrowUp" || event.key === "ArrowDown") && !event.target.value.includes("\n")) {
    event.preventDefault();
    walkHistory(event.key === "ArrowUp" ? -1 : 1);
  }
});

// Typing is what ends a walk through the history: the next ↑ starts again from
// the end rather than from wherever the last one left off.
document.addEventListener("input", (event) => {
  if (!event.target.matches?.("[data-machine-command]")) return;
  historyAt = -1;
  resizeCommandBox();
});
