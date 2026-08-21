// The deploys page: groups over the cluster, versioned compose templates, and
// the runs themselves.
//
// Three things about this file are deliberate:
//
//   - **The run rows are polled on the live tick, everything else is not.**
//     Groups and templates move when somebody saves a form; a run moves by
//     itself, machine by machine, and watching it is the entire point of
//     deploying from here rather than from a terminal. So `refreshDeploys`
//     reads the lists on a mount and a press, and reads the *active* runs every
//     tick — one small query against guard's own SQLite.
//   - **The mode is never chosen for you.** The dialog opens on the gated mode
//     every time, and the other one is labelled with what it actually costs.
//     A remembered "all at once" is how a staging habit reaches production.
//   - **Rollback is the deploy dialog with a tag filled in.** There is no
//     rollback path in this file that does anything the ordinary press does not
//     — which is why it can be trusted on the day it is used.
import { adminHeaders, el, muted, qs, qsa, relativeTime, request } from "./core.js";
import { ensure, set as publish } from "./store.js";
import { ask, clusterNodes } from "./cluster.js";

let groups = [];
let templates = [];
let runs = [];
let webhooks = [];
// The registries overview, read only when a dialog needs to offer images or
// tags. Behind it is somebody's provider API, not guard's database.
let registries = [];

// Which group rows are folded open. Here rather than in the DOM, because the
// tick redraws the runs and a fold that closed itself every three seconds would
// be a fold nobody can use.
const opened = new Set();

// What the deploy dialog is currently about.
let pending = { group: null, template: null, version: 0, tag: "", rollback: false, nodeIDs: [] };
// What the template dialog is editing: null for a new one.
let editing = null;

export async function refreshDeploys(force = false) {
  if (!qs("[data-deploy-rows]")) return;
  const work = [refreshRuns(), pollInstalls()];
  if (force) work.push(refreshLists());
  await Promise.allSettled(work);
}

async function refreshLists() {
  try {
    await ensure("deploy.groups", () => request("/api/deploy/groups", { headers: adminHeaders() }), (answer) => {
      groups = answer || [];
      renderGroups();
    });
    await ensure("deploy.templates", () => request("/api/deploy/templates", { headers: adminHeaders() }), (answer) => {
      templates = answer || [];
      renderTemplates();
      // A group's row names the tag each machine is running, and the service
      // that tag belongs to comes off a template — so the groups are redrawn
      // once the templates are known.
      renderGroups();
    });
    webhooks = await request("/api/webhooks", { headers: adminHeaders() }).catch(() => []);
    status("");
  } catch (failure) {
    status(failure.message);
  }
}

// Three at a time. A deploy row is tall — a machine list, a progress bar, an
// error and a log pane — so a page of them is a page, not a list.
const runsPerPage = 3;
let runPage = 0;
let runTotal = 0;

// The runs are read every tick while something is happening, and only the
// active ones — a page that re-read the history three times a second to watch
// one deploy would be a page that gets throttled off.
async function refreshRuns() {
  try {
    const live = await request("/api/deploy/runs?active=true", { headers: adminHeaders() });
    const active = (live && live.runs) || [];
    for (const run of active) watch("run", run.id, (frame) => {
      // A frame saying the run is over is a prompt to go and read the finished
      // row, not to draw the one in hand: the buttons on it — Stop, Retry —
      // are decided by the run's status, and the copy held here still says it
      // is running. Without this the Stop button sat there until the next tick,
      // or forever with the live toggle off.
      if (frame.done) { refreshRuns().catch(() => {}); return; }
      renderRuns();
    });
    if (active.length) {
      // Something is happening, so the newest page is the interesting one and
      // the live rows are merged into it. Only on page zero: somebody reading
      // last week's deploys should not have the list move under them.
      const known = new Map(runs.map((run) => [run.id, run]));
      for (const run of active) known.set(run.id, run);
      if (runPage === 0) runs = [...known.values()].sort((a, b) => b.id - a.id).slice(0, runsPerPage);
      renderRuns();
      // A machine that just came back healthy is running a different tag, and
      // the group row above says what each machine is running.
      await refreshStateInto();
      return;
    }
    const page = await request(
      `/api/deploy/runs?limit=${runsPerPage}&offset=${runPage * runsPerPage}`, { headers: adminHeaders() });
    runs = (page && page.runs) || [];
    runTotal = (page && page.total) || 0;
    // A page that has emptied out — the last run on it was pruned, or somebody
    // was on page four of four — steps back rather than showing nothing.
    if (!runs.length && runPage > 0) {
      runPage = Math.max(0, Math.ceil(runTotal / runsPerPage) - 1);
      return refreshRuns();
    }
    renderRuns();
  } catch (failure) {
    status(failure.message);
  }
}

// What each machine is running, per group, so the row can say it. Read from the
// same place rollback reads it: guard only writes it when a health check
// passed, so it is never the tag that was on its way.
const running = new Map(); // `${nodeID}/${service}` -> {current_tag, last_known_good_tag}

async function refreshStateInto() {
  const wanted = new Set();
  for (const group of groups) for (const node of group.nodes || []) wanted.add(node.node_id);
  await Promise.allSettled([...wanted].map(async (nodeID) => {
    const states = await request(`/api/deploy/state?node_id=${nodeID}`, { headers: adminHeaders() });
    for (const state of states || []) running.set(`${nodeID}/${state.service_name}`, state);
  }));
  renderGroups();
}

function status(message) {
  const note = qs("[data-deploy-status]");
  if (!note) return;
  note.textContent = message || "";
  note.hidden = !message;
}

function clone(name) {
  const template = qs(`[data-tpl="${name}"]`);
  return template ? template.content.firstElementChild.cloneNode(true) : null;
}

// ---------------------------------------------------------------- the groups

function renderGroups() {
  const host = qs("[data-deploy-rows]");
  if (!host) return;
  host.replaceChildren();
  const empty = qs("[data-deploy-empty]");
  if (empty) empty.hidden = groups.length > 0;

  for (const group of groups) {
    const row = clone("deploy-group");
    if (!row) return;
    row.dataset.group = group.id;
    qs("[data-fold]", row).textContent = group.name;
    const members = group.nodes || [];
    qs("[data-count]", row).textContent = members.length === 1 ? "1 machine" : `${members.length} machines`;

    // What this group is running, in one line: the distinct tags across its
    // machines. Two tags in one group is the thing worth seeing at a glance —
    // it means a rolling deploy stopped halfway.
    const tags = new Set();
    for (const member of members) {
      for (const [key, state] of running) {
        if (key.startsWith(`${member.node_id}/`) && state.current_tag) tags.add(state.current_tag);
      }
    }
    qs("[data-running]", row).textContent = tags.size ? [...tags].join(" · ") : "nothing deployed from guard yet";

    const busy = qs("[data-busy]", row);
    busy.hidden = !runs.some((run) => run.group_id === group.id && (run.status === "running" || run.status === "awaiting"));

    qs("[data-deploy]", row).onclick = () => openDeploy(group);
    qs("[data-edit]", row).onclick = () => openGroup(group);
    qs("[data-remove]", row).onclick = () => removeGroup(group);
    qs("[data-fold]", row).onclick = () => {
      if (opened.has(group.id)) opened.delete(group.id); else opened.add(group.id);
      renderGroups();
    };

    const body = qs("[data-body]", row);
    body.hidden = !opened.has(group.id);
    if (!body.hidden) {
      if (!members.length) body.appendChild(el("p", `text-sm ${muted}`, "No machines in this group."));
      for (const member of members) body.appendChild(memberRow(group, member));
      const where = webhooks.find((hook) => hook.id === group.webhook_id);
      body.appendChild(el("p", `pt-2 text-xs ${muted}`, where
        ? `A stopped deploy tells ${where.name}.`
        : "No destination: a deploy that stops will wait in silence for thirty minutes."));
    }
    host.appendChild(row);
  }
}

function memberRow(group, member) {
  const row = clone("deploy-member");
  qs("[data-name]", row).textContent = member.name;
  const warn = qs("[data-warn]", row);
  if (member.locked) { warn.hidden = false; warn.textContent = "locked"; }
  else if (!member.has_password) { warn.hidden = false; warn.textContent = "no login"; }

  // Every service this machine runs, not just this group's — the machine is
  // where a tag actually lives, and a group is only one way of naming it.
  const states = [...running.entries()]
    .filter(([key]) => key.startsWith(`${member.node_id}/`))
    .map(([, state]) => state);
  qs("[data-tag]", row).textContent = states.length
    ? states.map((state) => `${state.service_name}:${state.current_tag}` +
        (state.current_version ? ` v${state.current_version}` : "")).join(" ")
    : "—";

  // Docker, over the login guard already has. Offered on every machine rather
  // than only after a deploy has failed for want of it: the press is idempotent
  // and a box that is already fine answers in one round trip saying so.
  const install = qs("[data-prepare]", row);
  const report = installs.get(member.node_id);
  if (member.locked || !member.has_password) {
    install.hidden = true;
  } else if (installing.has(member.node_id)) {
    install.textContent = "installing…";
    install.disabled = true;
  } else {
    install.textContent = report && !report.error ? "Docker ready" : "Install docker";
    install.onclick = () => prepareMachine(member, install);
  }
  // What the machine is saying, live. The same pane a deploy's log uses: the
  // interesting minute of an install is the middle of it, not the end.
  if (report && (report.output || report.error)) {
    const pane = el("pre", "mt-1 max-h-48 w-full overflow-auto rounded-lg border border-border bg-background p-3 font-mono text-xs whitespace-pre-wrap select-text",
      report.error ? `${report.output || ""}\n${report.error}` : report.output);
    row.appendChild(pane);
  }

  // Rollback is offered only where there is a previous good deploy that is not
  // the one already running. Either half can differ: a new image tag, or the
  // same tag with an edited compose file.
  const back = states.find((state) => state.last_known_good_tag && state.last_known_good_version &&
    (state.last_known_good_tag !== state.current_tag || state.last_known_good_version !== state.current_version));
  const button = qs("[data-rollback]", row);
  if (back) {
    button.hidden = false;
    // Both halves in the label, because "roll back to alpine" is not an answer
    // when every deploy this week was alpine.
    button.textContent = `Roll back to ${back.last_known_good_tag} v${back.last_known_good_version}`;
    button.onclick = () => {
      const template = templates.find((entry) => entry.id === back.template_id)
        || templates.find((entry) => entry.service_name === back.service_name);
      openDeploy(group, {
        tag: back.last_known_good_tag,
        version: back.last_known_good_version,
        template, nodeIDs: [member.node_id], rollback: true,
      });
    };
  }
  return row;
}

// The machines with an install in flight. The row polls while one is here, so
// apt talks into the pane rather than into a held-open request.
const installing = new Set(); // nodeID

async function prepareMachine(member, button) {
  const yes = await ask({
    title: `Install docker on ${member.name}?`,
    body: "Guard runs its own fixed command over this machine's stored SSH login: the compose plugin from the distribution if docker is already there, or Docker's own get.docker.com installer if it is not. A machine that already has both is left alone.",
    confirm: "Install",
  });
  if (!yes) return;
  try {
    await request("/api/deploy/prepare", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ node_id: member.node_id }),
    });
    installing.add(member.node_id);
    watch("prepare", member.node_id, (frame) => {
      installs.set(member.node_id, { ...(installs.get(member.node_id) || {}), output: frame.output, running: !frame.done });
      if (frame.done) installing.delete(member.node_id);
      renderGroups();
    });
    renderGroups();
  } catch (failure) {
    status(`${member.name}: ${failure.message}`);
  }
}

// One poll per machine still installing, on the page's own tick. The output
// grows in the pane underneath; the answer lives on the server, so a reload
// mid-install picks up where the pane was rather than starting again.
async function pollInstalls() {
  if (!installing.size) return;
  await Promise.allSettled([...installing].map(async (nodeID) => {
    try {
      const report = await request(`/api/deploy/prepare?node_id=${nodeID}`, { headers: adminHeaders() });
      installs.set(nodeID, report);
      if (!report.running) installing.delete(nodeID);
    } catch {
      installing.delete(nodeID);
    }
  }));
  renderGroups();
}

// The last thing each machine's install said, kept so a finished one still
// shows its output instead of blinking away the moment it succeeds.
const installs = new Map(); // nodeID -> report

async function removeGroup(group) {
  const yes = await ask({
    title: `Remove ${group.name}?`,
    body: "The machines stay in the cluster and keep running what they are running. The deploys already recorded keep this group's name.",
    confirm: "Remove group",
  });
  if (!yes) return;
  try {
    await request(`/api/deploy/groups/${group.id}`, { method: "DELETE", headers: adminHeaders() });
    publish("deploy.groups", null);
    await refreshLists();
  } catch (failure) {
    status(failure.message);
  }
}

// ------------------------------------------------------------- the templates

function renderTemplates() {
  const host = qs("[data-template-rows]");
  if (!host) return;
  host.replaceChildren();
  const empty = qs("[data-template-empty]");
  if (empty) empty.hidden = templates.length > 0;

  for (const template of templates) {
    const row = clone("deploy-template-row");
    if (!row) return;
    qs("[data-name]", row).textContent = template.name;
    qs("[data-version]", row).textContent = `v${template.version}`;
    const vault = (template.vars || []).filter((entry) => entry.source === "vault").length;
    qs("[data-detail]", row).textContent =
      `${template.service_name} · ${template.image} · ${template.path}` + (vault ? ` · ${vault} from the vault` : "");
    qs("[data-edit]", row).onclick = () => openTemplate(template);
    qs("[data-remove]", row).onclick = () => removeTemplate(template);
    host.appendChild(row);
  }
}

async function removeTemplate(template) {
  const yes = await ask({
    title: `Delete ${template.name}?`,
    body: "Every version goes. The deploys that used it keep the name and version number they recorded, so the history stays readable.",
    confirm: "Delete template",
    phrase: template.name,
  });
  if (!yes) return;
  try {
    await request(`/api/deploy/templates/${template.id}`, { method: "DELETE", headers: adminHeaders() });
    publish("deploy.templates", null);
    await refreshLists();
  } catch (failure) {
    status(failure.message);
  }
}

// ------------------------------------------------------------------ the runs

// What a status is worth on a progress bar. A failed machine's bar is full and
// red rather than stuck at 70%: the work is over, and a bar still creeping
// along under the word "failed" reads as something still happening.
const progress = {
  pending: 0, deploying: 45, health_check: 75,
  healthy: 100, failed: 100, skipped: 100, interrupted: 100,
};
const shade = {
  healthy: "var(--primary)", failed: "var(--destructive)", interrupted: "var(--destructive)",
  skipped: "var(--muted-foreground)", deploying: "var(--primary)", health_check: "var(--primary)",
};
const runBadge = {
  healthy: "cn-badge-variant-default", running: "cn-badge-variant-secondary",
  cancelled: "cn-badge-variant-secondary",
  awaiting: "cn-badge-variant-destructive", failed: "cn-badge-variant-destructive",
  abandoned: "cn-badge-variant-destructive", interrupted: "cn-badge-variant-destructive",
};
const runWord = {
  healthy: "healthy", running: "running", awaiting: "stopped — waiting on you",
  cancelled: "stopped by you",
  failed: "failed", abandoned: "gave up waiting", interrupted: "interrupted by a restart",
};

function renderRuns() {
  const host = qs("[data-run-rows]");
  if (!host) return;
  host.replaceChildren();
  const empty = qs("[data-run-empty]");
  if (empty) empty.hidden = runs.length > 0;

  for (const run of runs) {
    const row = clone("deploy-run");
    if (!row) return;
    qs("[data-group]", row).textContent = run.group_name || "a deleted group";
    const badge = qs("[data-status]", row);
    badge.textContent = runWord[run.status] || run.status;
    badge.classList.add(runBadge[run.status] || "cn-badge-variant-secondary");
    const mode = qs("[data-mode]", row);
    mode.textContent = run.mode === "parallel" ? "all at once" : "one at a time";
    qs("[data-detail]", row).textContent =
      `${run.image}:${run.tag} · ${run.template_name} v${run.template_version}` + (run.rollback ? " · rollback" : "");
    const [healthy, failed, waiting] = tally(run);
    qs("[data-tally]", row).textContent =
      `${healthy}/${run.instances.length} healthy${failed ? `, ${failed} failed` : ""}${waiting ? `, ${waiting} pending` : ""} · ${relativeTime(run.started_at)}`;

    const list = qs("[data-instances]", row);
    for (const instance of run.instances || []) list.appendChild(instanceRow(instance));

    // A run that is going, or one sitting at a failure, can be stopped. A
    // finished one cannot, and a button that would say "stop" over a deploy
    // that ended four hours ago is a button that gets pressed by accident.
    const stop = qs("[data-cancel]", row);
    if (run.status === "running" || run.status === "awaiting") {
      stop.hidden = false;
      stop.onclick = () => cancelRun(run);
    } else if (run.status !== "healthy") {
      // Over, and it did not come back healthy. One press to go again with the
      // same template version, tag and mode — which is what somebody standing
      // in front of a failed deploy is going to do next.
      const again = qs("[data-retry-run]", row);
      again.hidden = false;
      again.onclick = () => retryRun(run);
    }

    const answers = qs("[data-answers]", row);
    answers.hidden = run.status !== "awaiting";
    if (!answers.hidden) {
      const stuck = (run.instances || []).find((instance) => instance.status === "failed");
      qs("[data-answer-note]", answers).textContent = stuck
        ? `${stuck.node_name}: ${stuck.error || "failed"} — nothing after it has been touched. It gives up in thirty minutes.`
        : "Stopped.";
      qs("[data-retry]", answers).onclick = () => answer(run, "retry");
      qs("[data-skip]", answers).onclick = () => answer(run, "skip");
      qs("[data-stop]", answers).onclick = () => answer(run, "stop");
      const back = qs("[data-run-rollback]", answers);
      if (stuck && stuck.previous_tag) {
        back.hidden = false;
        back.textContent = `Roll back ${stuck.node_name} to ${stuck.previous_tag}`;
        back.onclick = async () => {
          // Stop this run first: leaving it waiting while another deploy takes
          // the same machine is how two deploys end up on one box.
          await answer(run, "stop");
          const group = groups.find((entry) => entry.id === run.group_id);
          const template = templates.find((entry) => entry.id === run.template_id);
          if (group) openDeploy(group, { tag: stuck.previous_tag, template, nodeIDs: [stuck.node_id], rollback: true });
        };
      }
    }
    host.appendChild(row);
  }
  renderRunPager();
}

function renderRunPager() {
  const pager = qs("[data-run-pager]");
  if (!pager) return;
  const pages = Math.max(1, Math.ceil(runTotal / runsPerPage));
  // One page of history is not a pager, it is two disabled buttons.
  pager.hidden = runTotal <= runsPerPage;
  if (pager.hidden) return;
  const first = runPage * runsPerPage + 1;
  qs("[data-run-page-summary]", pager).textContent =
    `Showing ${first}–${Math.min(first + runs.length - 1, runTotal)} of ${runTotal}`;
  qs("[data-run-page-number]", pager).textContent = `Page ${runPage + 1} of ${pages}`;
  const previous = qs('[data-run-page="previous"]', pager);
  const next = qs('[data-run-page="next"]', pager);
  previous.disabled = runPage === 0;
  next.disabled = runPage + 1 >= pages;
  previous.onclick = () => { runPage = Math.max(0, runPage - 1); refreshRuns(); };
  next.onclick = () => { runPage = Math.min(pages - 1, runPage + 1); refreshRuns(); };
}

function tally(run) {
  let healthy = 0, failed = 0, waiting = 0;
  for (const instance of run.instances || []) {
    if (instance.status === "healthy") healthy++;
    else if (instance.status === "failed" || instance.status === "interrupted") failed++;
    else if (instance.status !== "skipped") waiting++;
  }
  return [healthy, failed, waiting];
}

// ---------------------------------------------------------------- streaming
//
// The tick is what the page is built on and what it falls back to; this is the
// live wire on top. An EventSource per thing being watched, opened when
// something is actually happening and closed the moment it stops — a stream
// held open for a finished deploy is a socket doing nothing.
//
// Every frame is a superset of the last, so a dropped connection costs nothing:
// the browser reconnects on its own, and the tick has the row anyway.
const streams = new Map(); // `${kind}/${id}` -> EventSource
// What the wire has said, ahead of the row. Read by the renderers in preference
// to the polled copy, because it is the same output one to three seconds sooner.
const live = new Map(); // `${kind}/${id}/${nodeID}` -> {status, output}

function watch(kind, id, onFrame) {
  const key = `${kind}/${id}`;
  if (streams.has(key)) return;
  const source = new EventSource(`/api/deploy/stream?${kind === "run" ? "run" : "node"}=${id}`);
  streams.set(key, source);
  source.addEventListener("frame", (event) => {
    let frame;
    try { frame = JSON.parse(event.data); } catch { return; }
    live.set(`${kind}/${id}/${frame.node_id}`, frame);
    onFrame(frame);
    if (frame.done) unwatch(kind, id);
  });
  // An error is a dropped connection, and EventSource reconnects by itself. It
  // is only worth closing when the thing being watched is over, which the tick
  // finds out anyway.
  source.onerror = () => {
    if (source.readyState === EventSource.CLOSED) streams.delete(key);
  };
}

function unwatch(kind, id) {
  const key = `${kind}/${id}`;
  const source = streams.get(key);
  if (!source) return;
  source.close();
  streams.delete(key);
}

// The outlet is about to throw this page away. Every open socket goes with it —
// a stream nobody is looking at is a stream that should not be open.
export function closeDeployStreams() {
  for (const source of streams.values()) source.close();
  streams.clear();
  live.clear();
}

// Which instances have their log open. Kept here, not in the DOM: the runs are
// redrawn on the live tick, and a pane that closed itself every three seconds
// while somebody was reading a stack trace would be worse than no pane.
const logsOpen = new Set(); // `${runID}/${nodeID}`

function instanceRow(instance) {
  const row = clone("deploy-instance");
  qs("[data-name]", row).textContent = instance.node_name;
  qs("[data-state]", row).textContent = instance.status.replace("_", " ");
  const bar = qs("[data-bar]", row);
  // Inline styles rather than classes: a width assembled from a number is a
  // class Tailwind can never find, and this one is a percentage.
  bar.style.width = `${progress[instance.status] ?? 0}%`;
  bar.style.background = shade[instance.status] || "var(--muted-foreground)";
  qs("[data-note]", row).textContent = instance.error || instance.health || "";

  // The wire is ahead of the row by up to a tick, so it wins where it has
  // something to say. Same output either way — this one is just sooner.
  const wire = live.get(`run/${instance.run_id}/${instance.node_id}`);
  if (wire && wire.output && !instance.error) {
    instance = { ...instance, output: wire.output, status: wire.status || instance.status };
    qs("[data-state]", row).textContent = instance.status.replace("_", " ");
    bar.style.width = `${progress[instance.status] ?? 0}%`;
    bar.style.background = shade[instance.status] || "var(--muted-foreground)";
  }

  // What the machine printed. Recorded on every run since the first one — it
  // was the pane that was missing, not the log.
  const key = `${instance.run_id}/${instance.node_id}`;
  const pane = qs("[data-output]", row);
  const button = qs("[data-log]", row);
  if (instance.output) {
    button.hidden = false;
    const settle = () => {
      const open = logsOpen.has(key);
      pane.hidden = !open;
      pane.textContent = open ? instance.output : "";
      button.textContent = open ? "Hide log" : "Log";
    };
    button.onclick = () => {
      if (logsOpen.has(key)) logsOpen.delete(key); else logsOpen.add(key);
      settle();
    };
    settle();
  }
  return row;
}

// Retry is a new run, not a resurrection of the old one: the row that failed is
// a record of something that happened, and rewriting it would lose the only
// account of it. The same rule rollback follows — there is one deploy path, and
// everything else fills its dialog in.
function retryRun(run) {
  const group = groups.find((entry) => entry.id === run.group_id);
  if (!group) { status("That group has been deleted, so there is nothing to retry against."); return; }
  const template = templates.find((entry) => entry.id === run.template_id);
  if (!template) { status("That template has been deleted, so there is nothing to retry against."); return; }
  // Only the machines that did not come back healthy. Redeploying the ones that
  // already passed their gate would be replacing working containers to fix a
  // different machine.
  const unhealthy = (run.instances || [])
    .filter((instance) => instance.status !== "healthy")
    .map((instance) => instance.node_id);
  openDeploy(group, {
    template, tag: run.tag, version: run.template_version,
    mode: run.mode, rollback: run.rollback,
    // Every machine failed is the same as "the whole group", and saying so
    // keeps the summary honest about what it is about to touch.
    nodeIDs: unhealthy.length === (run.instances || []).length ? [] : unhealthy,
  });
}

async function cancelRun(run) {
  const [, , pending] = tally(run);
  const yes = await ask({
    title: `Stop deploying ${run.group_name}?`,
    body: "Guard stops watching and stops moving on: nothing after the machine it is on now gets touched. " +
      "It does not undo anything — machines already deployed to keep what they were given, and the one in " +
      "flight may have a container running that guard never proved. Going back is a deploy of the last good tag.",
    detail: `${run.image}:${run.tag}\n${pending} machine${pending === 1 ? "" : "s"} not yet touched`,
    confirm: "Stop the deploy",
  });
  if (!yes) return;
  try {
    await request("/api/deploy/cancel", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ run_id: run.id }),
    });
    await refreshRuns();
  } catch (failure) {
    status(failure.message);
  }
}

async function answer(run, decision) {
  try {
    await request("/api/deploy/resolve", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ run_id: run.id, decision }),
    });
    await refreshRuns();
  } catch (failure) {
    status(failure.message);
  }
}

// --------------------------------------------------------- the deploy dialog

function openDeploy(group, { tag = "", template = null, nodeIDs = [], rollback = false,
  version = 0, mode = "sequential" } = {}) {
  const dialog = qs("[data-deploy-dialog]");
  if (!dialog) return;
  if (!templates.length) { status("Make a compose template first — a deploy is a template and a tag."); return; }
  pending = { group, template: template || templates[0], version, tag, rollback, nodeIDs };

  qs("[data-deploy-dialog-group]", dialog).textContent =
    nodeIDs.length === 1 ? `${group.name} → one machine` : group.name;
  const picker = qs("[data-deploy-template]", dialog);
  picker.replaceChildren(...templates.map((entry) => new Option(`${entry.name} (${entry.service_name})`, entry.id)));
  picker.value = String(pending.template.id);
  picker.onchange = () => {
    pending.template = templates.find((entry) => String(entry.id) === picker.value) || templates[0];
    fillVersions();
    loadTags();
    summarise();
  };
  qs("[data-deploy-tag]", dialog).value = tag;
  qs("[data-deploy-tag]", dialog).oninput = summarise;
  for (const radio of qsa("[data-deploy-mode]", dialog)) {
    // Gated unless the caller is repeating a run that was not. A retry is the
    // one place a remembered mode is right — it is repeating something that
    // already happened, not choosing afresh — and everywhere else this opens
    // gated, because a remembered "all at once" is how a staging habit reaches
    // production.
    radio.checked = radio.value === mode;
    radio.onchange = summarise;
  }
  qs("[data-deploy-dialog-error]", dialog).textContent = "";
  qs("[data-deploy-go]", dialog).onclick = go;
  qs("[data-deploy-tags-refresh]", dialog).onclick = () => loadTags(true);
  qs("[data-deploy-tag-list]", dialog).onchange = (event) => {
    qs("[data-deploy-tag]", dialog).value = event.target.value;
    summarise();
  };
  fillVersions();
  summarise();
  dialog.showModal();
  loadTags();
}

function fillVersions() {
  const dialog = qs("[data-deploy-dialog]");
  const picker = qs("[data-deploy-version]", dialog);
  const versions = pending.template.versions || [{ version: pending.template.version }];
  picker.replaceChildren(...versions.map((entry, index) =>
    new Option(index === 0 ? `v${entry.version} (newest)` : `v${entry.version}`, entry.version)));
  // A retry pins the version that actually ran, not the newest — repeating a
  // run against a template somebody edited since is not a repeat.
  const wanted = pending.version && versions.some((entry) => entry.version === pending.version)
    ? pending.version : versions[0].version;
  picker.value = String(wanted);
  picker.onchange = summarise;
}

// The tags come from the registry the image belongs to, matched by the prefix
// the registry itself reports — so a template's image string is enough, and no
// account id has to be stored beside it.
async function loadTags(force = false) {
  const dialog = qs("[data-deploy-dialog]");
  const list = qs("[data-deploy-tag-list]", dialog);
  list.replaceChildren(new Option("looking for tags…", ""));
  try {
    if (force || !registries.length) registries = await request("/api/registries", { headers: adminHeaders() });
    const image = pending.template.image || "";
    let found = null;
    for (const account of registries || []) {
      for (const registry of account.registries || []) {
        if (registry.urn && image.startsWith(registry.urn + "/")) {
          found = { account: account.account.id, registry: registry.name, repo: image.slice(registry.urn.length + 1) };
        }
      }
    }
    if (!found) { list.replaceChildren(new Option("no registry here owns that image — type the tag", "")); return; }
    const tags = await request(`/api/registries/tags?account=${found.account}` +
      `&registry=${encodeURIComponent(found.registry)}&repo=${encodeURIComponent(found.repo)}`,
      { headers: adminHeaders() });
    if (!tags || !tags.length) { list.replaceChildren(new Option("no tags in that repository", "")); return; }
    list.replaceChildren(new Option("choose a tag…", ""), ...tags.map((tag) => new Option(tag.name, tag.name)));
  } catch (failure) {
    list.replaceChildren(new Option(failure.message, ""));
  }
}

// The summary is the last place the mode is readable before machines start
// being replaced. It says the count, because "all at once" over six machines is
// a different sentence to "all at once" over one.
function summarise() {
  const dialog = qs("[data-deploy-dialog]");
  const tag = qs("[data-deploy-tag]", dialog).value.trim();
  const mode = qsa("[data-deploy-mode]", dialog).find((radio) => radio.checked)?.value || "sequential";
  const version = qs("[data-deploy-version]", dialog).value;
  const machines = pending.nodeIDs.length
    ? pending.nodeIDs.length
    : (pending.group.nodes || []).length;
  const lines = [
    `group     ${pending.group.name}`,
    `template  ${pending.template.name} v${version}`,
    `image     ${pending.template.image}:${tag || "…"}`,
    `machines  ${machines}`,
    `how       ${mode === "parallel"
      ? `all ${machines} at once, no health gate`
      : "one at a time, each proved before the next"}`,
  ];
  qs("[data-deploy-summary]", dialog).textContent = lines.join("\n");
}

async function go() {
  const dialog = qs("[data-deploy-dialog]");
  const error = qs("[data-deploy-dialog-error]", dialog);
  const tag = qs("[data-deploy-tag]", dialog).value.trim();
  const mode = qsa("[data-deploy-mode]", dialog).find((radio) => radio.checked)?.value || "sequential";
  if (!tag) { error.textContent = "Choose a tag."; return; }

  const machines = pending.nodeIDs.length || (pending.group.nodes || []).length;
  const yes = await ask({
    title: `Deploy ${tag} to ${pending.group.name}?`,
    body: mode === "parallel"
      ? `All ${machines} machines at once, with nothing gating between them. Every one of them can go down together.`
      : `${machines} machine${machines === 1 ? "" : "s"}, one at a time. A failure stops the run and waits for you.`,
    detail: qs("[data-deploy-summary]", dialog).textContent,
    confirm: "Write deploy",
  });
  if (!yes) return;

  try {
    await request("/api/deploy", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({
        group_id: pending.group.id,
        template_id: pending.template.id,
        template_version: Number(qs("[data-deploy-version]", dialog).value) || 0,
        tag, mode,
        node_ids: pending.nodeIDs,
        rollback: pending.rollback,
      }),
    });
    dialog.close("cancel");
    await refreshRuns();
  } catch (failure) {
    error.textContent = failure.message;
  }
}

// ---------------------------------------------------------- the group dialog

async function openGroup(group) {
  const dialog = qs("[data-group-dialog]");
  if (!dialog) return;
  qs("[data-group-dialog-title]", dialog).textContent = group ? `Edit ${group.name}` : "Add group";
  qs('[data-group="name"]', dialog).value = group ? group.name : "";
  qs("[data-group-dialog-error]", dialog).textContent = "";

  const where = qs("[data-group-webhook]", dialog);
  where.replaceChildren(new Option("nobody", "0"), ...webhooks.map((hook) => new Option(hook.name, hook.id)));
  where.value = String(group?.webhook_id || 0);
  const note = qs("[data-group-webhook-note]", dialog);
  const explain = () => {
    note.textContent = where.value === "0"
      ? "A deploy that stops will wait in silence and give up after thirty minutes."
      : "A stopped deploy says so immediately, again after fifteen minutes, and once more when it gives up.";
  };
  where.onchange = explain;
  explain();

  const host = qs("[data-group-machines]", dialog);
  host.replaceChildren(el("p", `text-sm ${muted}`, "Reading the cluster…"));
  const nodes = await clusterNodes();
  const chosen = new Set(group ? group.node_ids || [] : []);
  host.replaceChildren();
  if (!nodes.length) host.appendChild(el("p", `text-sm ${muted}`, "No machines in the cluster yet."));
  for (const node of nodes) {
    const row = clone("deploy-machine-choice");
    const pick = qs("[data-pick]", row);
    pick.checked = chosen.has(node.id);
    pick.value = node.id;
    qs("[data-name]", row).textContent = node.name;
    // A machine with no login or a locked one can be in a group — that is a
    // normal state on the day a box is added — and the deploy is what refuses,
    // in words. Saying so here stops somebody lining one up.
    qs("[data-note]", row).textContent = node.locked
      ? "locked — a deploy will refuse it"
      : node.has_password ? "" : "no ssh login — a deploy will refuse it";
    host.appendChild(row);
  }

  qs("[data-group-save]", dialog).onclick = async () => {
    const body = {
      id: group ? group.id : 0,
      name: qs('[data-group="name"]', dialog).value.trim(),
      node_ids: qsa("[data-pick]", host).filter((box) => box.checked).map((box) => Number(box.value)),
      webhook_id: Number(where.value) || 0,
    };
    try {
      await request("/api/deploy/groups", { method: "POST", headers: adminHeaders(), body: JSON.stringify(body) });
      publish("deploy.groups", null);
      dialog.close("cancel");
      await refreshLists();
    } catch (failure) {
      qs("[data-group-dialog-error]", dialog).textContent = failure.message;
    }
  };
  dialog.showModal();
}

// ------------------------------------------------------- the template dialog

async function openTemplate(template) {
  const dialog = qs("[data-template-dialog]");
  if (!dialog) return;
  editing = template || null;
  qs("[data-template-dialog-title]", dialog).textContent =
    template ? `${template.name} — new version` : "New template";
  qs("[data-template-dialog-error]", dialog).textContent = "";
  const value = (key, fallback = "") => {
    const field = qs(`[data-template="${key}"]`, dialog);
    if (field) field.value = template ? (template[key] ?? fallback) : fallback;
  };
  value("name"); value("compose_yaml"); value("health_path");
  qs('[data-template="health_port"]', dialog).value = template?.health_port || "";
  await fillEnvs(dialog, template?.secret_env_id || 0);

  // The three fields that used to be typed are now shown instead: the name
  // decides where guard writes, and the compose file decides what gets pulled.
  // Shown rather than hidden, because "which directory did this land in" is a
  // question somebody standing in front of a box actually has.
  const derived = qs("[data-template-derived]", dialog);
  const describe = () => {
    const name = qs('[data-template="name"]', dialog).value.trim();
    const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
    const compose = qs('[data-template="compose_yaml"]', dialog).value;
    const image = imageIn(compose);
    derived.textContent = slug
      ? `Writes to /guard/${slug} · deploys ${image || "the image your compose tags with ${TAG}"}`
      : "";
  };
  qs('[data-template="name"]', dialog).oninput = describe;
  qs('[data-template="compose_yaml"]', dialog).oninput = describe;
  describe();

  const vars = qs("[data-template-vars]", dialog);
  vars.replaceChildren();
  for (const entry of template?.vars || []) vars.appendChild(varRow(entry));
  qs("[data-template-add-var]", dialog).onclick = () => vars.appendChild(varRow({ key: "", source: "static", value: "" }));

  qs("[data-template-save]", dialog).onclick = () => saveTemplate(dialog);
  dialog.showModal();
}

function varRow(entry) {
  const row = clone("deploy-var");
  qs("[data-key]", row).value = entry.key || "";
  const source = qs("[data-source]", row);
  source.value = entry.source || "static";
  const value = qs("[data-value]", row);
  const settle = () => {
    // A vault variable has no value to type: the whole point is that guard
    // reads it at deploy time and keeps no copy.
    value.disabled = source.value === "vault";
    value.placeholder = source.value === "vault" ? "read from the vault at deploy time" : "value";
    if (source.value === "vault") value.value = "";
  };
  source.onchange = settle;
  value.value = entry.value || "";
  settle();
  qs("[data-remove]", row).onclick = () => row.remove();
  return row;
}

// The image, read out of the compose file the same way the server does it: the
// one reference that carries ${TAG}. Here only so the dialog can say what it is
// before the save — the server derives it again and its answer is the one
// stored.
function imageIn(compose) {
  for (const line of (compose || "").split("\n")) {
    const match = /^[ \t]*image:[ \t]*["']?([^\s"']+)["']?[ \t]*$/.exec(line);
    if (!match) continue;
    const reference = match[1];
    if (!/\$\{TAG(:-[^}]*)?\}|\$TAG\b/.test(reference)) continue;
    const at = reference.lastIndexOf(":");
    return at > 0 ? reference.slice(0, at) : reference;
  }
  return "";
}

async function fillEnvs(dialog, current) {
  const picker = qs("[data-template-env]", dialog);
  picker.replaceChildren(new Option("no vault environment", "0"));
  try {
    const spaces = await request("/api/secrets", { headers: adminHeaders() });
    const options = [new Option("no vault environment", "0")];
    // A workspace carries a count of its environments, not the environments —
    // so each one is asked for. Only the workspaces that have any.
    for (const space of spaces || []) {
      if (!space.envs) continue;
      const envs = await request(`/api/secrets/envs?workspace=${space.id}`, { headers: adminHeaders() }).catch(() => []);
      for (const env of envs || []) options.push(new Option(`${space.name} / ${env.name}`, env.id));
    }
    picker.replaceChildren(...options);
    picker.value = String(current || 0);
  } catch {
    // A vault that cannot be listed is not a reason to refuse to edit a
    // template: the static variables are still worth saving.
  }
}

async function saveTemplate(dialog) {
  const read = (key) => qs(`[data-template="${key}"]`, dialog)?.value.trim() || "";
  const vars = qsa("[data-template-vars] > *", dialog).map((row) => ({
    key: qs("[data-key]", row).value.trim(),
    source: qs("[data-source]", row).value,
    value: qs("[data-value]", row).value,
  })).filter((entry) => entry.key);

  const body = {
    id: editing ? editing.id : 0,
    name: read("name"),
    // No service, image or directory: the server derives all three from the
    // name and the compose file, and stores what it derived.
    compose_yaml: qs('[data-template="compose_yaml"]', dialog).value,
    health_path: read("health_path"),
    health_port: Number(qs('[data-template="health_port"]', dialog).value) || 0,
    secret_env_id: Number(qs("[data-template-env]", dialog).value) || 0,
    vars,
  };
  try {
    await request("/api/deploy/templates", { method: "POST", headers: adminHeaders(), body: JSON.stringify(body) });
    publish("deploy.templates", null);
    dialog.close("cancel");
    await refreshLists();
  } catch (failure) {
    qs("[data-template-dialog-error]", dialog).textContent = failure.message;
  }
}

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-deploy-new-group]")) { openGroup(null); return; }
  if (event.target.closest("[data-deploy-new-template]")) { openTemplate(null); return; }
});
