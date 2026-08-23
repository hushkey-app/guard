// The analytics page's client: which of the three bands is the page, and the
// window everything under it is read over.
//
// The page ships all three bands hidden, because the decision cannot be made
// where the markup is — `index.client.templ` also runs in wasm, where there is
// no request and no environment to read, and "analytics is off" is exactly the
// sentence that must never be shown to somebody whose tracker is working. So
// `GET /api/analytics/health` decides here: the door is shut, the door is open
// and nothing has arrived, or there is something to draw.
//
// Everything goes through the store, like every other page. Walking to /traces
// and back is a navigation, not a fetch, and the window is module state for the
// same reason: the store lives outside the outlet, and so does this.
import { adminHeaders, el, muted, number, qs, qsa, relativeTime, request, text } from "./core.js";
import { draw } from "./charts.js";
import { ask } from "./cluster.js";
import { ensure, forget, freshness, get, set } from "./store.js";

// The window the strip and the grid are read over. Held here rather than read
// off the select, because the markup's default is a default — once somebody
// has chosen 30d, coming back to this page should come back to 30d.
let range = "7d";

// Is there anything to draw? Enabled says the door is open; last_event says a
// browser has actually been through it. Configured-but-never-visited is its own
// sentence, because "no rows" and "no tracker" are the same empty grid.
const isLive = (health) => Boolean(health?.enabled && health?.last_event);

function band(name, shown) {
  const node = qs(`[data-analytics-${name}]`);
  if (node) node.hidden = !shown;
}

function renderBands(health) {
  const live = isLive(health);
  band("off", !health.enabled);
  band("install", health.enabled && !live);
  band("live", live);
  if (health.enabled && !live) renderSnippet();
}

// The origin is the tab's, never guard's own idea of it. Behind a proxy the
// server has no way to know which URL somebody reached it on, and a snippet
// naming the wrong host is one that gets pasted and then silently posts
// nowhere.
function renderSnippet() {
  const tag = `<script defer src="${location.origin}/v1/rum/track.js"><\/script>`;
  // Two places now: the card, where it is copied from, and the How to use
  // dialog, where it is read. Both, because a dialog showing the wrong host is
  // worse than one showing none — and it is one string either way.
  for (const slot of qsa("[data-analytics-script], [data-analytics-script-copy]")) {
    slot.textContent = tag;
  }
}

async function copySnippet(button) {
  const snippet = qs("[data-analytics-script]")?.textContent;
  if (!snippet) return;
  const said = button.textContent;
  try {
    await navigator.clipboard.writeText(snippet);
    button.textContent = "Copied";
  } catch {
    // The clipboard is refused on an insecure origin, which is where a guard
    // on somebody's laptop usually is. The snippet is on screen and
    // selectable, so say that rather than failing silently.
    button.textContent = "Select it instead";
  }
  setTimeout(() => { button.textContent = said; }, 2000);
}

function say(message) {
  const grid = qs("[data-analytics-grid]");
  if (grid) grid.replaceChildren(el("p", `text-sm ${muted}`, message));
}

// The strip, in the order it is read: what happened, how much of it, and the
// two ratios that say whether it was all the same visit.
//
// `read` answers null for a window that cannot measure the figure rather than
// zero, and the tile draws a dash from it — no sessions is no ratio, which is
// the same answer the store already refuses to invent (analyticsWindow).
const FIGURES = [
  {
    label: "Sessions",
    read: (span) => span.sessions ?? 0,
    format: (value) => number.format(value),
  },
  {
    label: "Page views",
    read: (span) => span.views ?? 0,
    format: (value) => number.format(value),
  },
  {
    label: "Views per session",
    read: (span) => (span.sessions ? span.views_per_session : null),
    format: (value) => value.toFixed(2),
  },
  {
    label: "Actions per session",
    read: (span) => (span.sessions ? span.actions_per_session : null),
    format: (value) => value.toFixed(2),
  },
];

// The change against the window of equal length before it.
//
// Three answers, and only one of them is a percentage. Unmeasurable on either
// side is silence, because a percentage against a dash is a number invented to
// fill the space; a rise from nothing is "new", because +100% from zero is the
// same invention with a decimal point. Up is the primary colour for all four
// figures — every one of them is a number somebody is trying to increase, so
// there is no metric here where a fall is the good news.
function fillDelta(tile, value, previous, format) {
  const delta = tile.querySelector("[data-stat-delta]");
  const note = tile.querySelector("[data-stat-note]");
  delta.textContent = "";
  note.textContent = "";
  delta.className = "font-medium tabular-nums";
  if (value === null || previous === null) return;
  if (previous === 0) {
    delta.textContent = value ? "new" : "no change";
    delta.classList.add(muted);
    return;
  }
  const change = ((value - previous) / Math.abs(previous)) * 100;
  delta.textContent = `${change >= 0 ? "▲" : "▼"} ${Math.abs(change).toFixed(1)}%`;
  delta.classList.add(change >= 0 ? "text-primary" : muted);
  note.textContent = `from ${format(previous)}`;
}

// Cloned from the <template> in client/ui/components rather than built here,
// so the card is the same shadcn markup every other panel is and every class
// stays where Tailwind looks for it.
function renderStrip(summary) {
  const strip = qs("[data-analytics-strip]");
  const template = qs("[data-analytics-stat-template]");
  if (!strip || !template) return;
  // No summary at all is the read having failed, which is not a window of
  // zeroes: nothing measured means nothing drawn.
  if (!summary) {
    strip.replaceChildren();
    return;
  }
  // Never `window` as a name here: the tiles are drawn beside a global of that
  // name, and shadowing it is how a later edit reaches for location.origin and
  // gets a rollup instead.
  const current = summary.window || {};
  const previous = summary.previous || {};
  strip.replaceChildren(...FIGURES.map((figure) => {
    const tile = template.content.firstElementChild.cloneNode(true);
    const value = figure.read(current);
    tile.querySelector("[data-stat-label]").textContent = figure.label;
    tile.querySelector("[data-stat-value]").textContent = value === null ? "—" : figure.format(value);
    fillDelta(tile, value, figure.read(previous), figure.format);
    return tile;
  }));
}

// The grid.
//
// Its rows are the window's paths and its columns are the pinned actions, and
// those are two reads of two endpoints — the actions list has no window on
// purpose, because "what actions exist" is not a measurement and a list that
// emptied itself when somebody narrowed to a day is a list nobody could pin
// from. So both are held here and whichever lands second draws.
let paths = null;
let pathsStale = false;
let pinned = [];
// Why the columns might be missing. Drawn rather than swallowed, because a grid
// with no action columns reads as "nobody has pinned one" — a sentence about
// somebody's decision, not about a fetch that failed.
let columnsProblem = "";

// Sorted by views, descending: the order the endpoint already answers in, so
// the first draw is the server's answer rather than a re-sort of it.
let sort = { key: "views", descending: true };

// Every window key this page has read. Deleting an action purges what was
// counted under it from all of them at once, and `ensure` would otherwise hand
// a remembered grid straight back with a column of numbers that no longer
// exist. The per-path series are not in here on purpose: they count page views
// and sessions, which an action's deletion does not touch.
const windowsRead = new Set();

// The track list, in rem, so every row lines up under every heading. Fixed
// columns are the point of a grid over a wall of cards — the figure that is
// unlike the others is found by scanning a column, which only works if the
// column is a column.
const PATH_MIN_REM = 16;
const COUNT_REM = 6;
const ACTION_REM = 7;

function tracks(actions) {
  const fixed = `minmax(0,1fr) ${COUNT_REM}rem ${COUNT_REM}rem`;
  // `repeat(0, …)` is not a valid track list, and an invalid one drops the
  // whole declaration — which would silently collapse every row into one
  // column on the day somebody unpinned the last action.
  return actions ? `${fixed} repeat(${actions}, ${ACTION_REM}rem)` : fixed;
}

// What a row is sorted on, and null for anything this window cannot measure.
//
// Views and sessions included: a path with actions and no page views is a real
// thing — a tracker firing on a route the page view never reached — and its
// zero is an absence rather than a measurement.
function sortValue(row, key) {
  if (key === "path") return row.path;
  if (key === "views") return row.views || null;
  if (key === "sessions") return row.sessions || null;
  const cell = row.actions?.[key];
  // Sorted on the rate, which is the only reason the column exists. Sorting an
  // action by its raw count mostly reproduces the views order — busy pages have
  // more of everything — and buries the small page that converts, which is the
  // row somebody opened this grid to find.
  if (!cell || !row.sessions) return null;
  return cell.rate;
}

function compare(left, right, key, descending) {
  const a = sortValue(left, key);
  const b = sortValue(right, key);
  // A dash is not a small number. Unmeasurable sorts last in both directions:
  // ascending by an action, the top of the page would otherwise be every path
  // the action has never been on.
  if (a === null || b === null) return a === b ? 0 : a === null ? 1 : -1;
  if (typeof a === "string") return descending ? b.localeCompare(a) : a.localeCompare(b);
  return descending ? b - a : a - b;
}

function head(column) {
  const cell = qs("[data-analytics-head-template]").content.firstElementChild.cloneNode(true);
  cell.dataset.analyticsSort = column.key;
  cell.querySelector("[data-head-label]").textContent = column.label;
  if (column.key !== "path") cell.classList.add("justify-end");
  if (column.title) cell.title = column.title;
  const active = sort.key === column.key;
  cell.setAttribute("aria-sort", active ? (sort.descending ? "descending" : "ascending") : "none");
  if (active) {
    cell.classList.add("text-foreground");
    cell.querySelector("[data-head-arrow]").textContent = sort.descending ? "▼" : "▲";
  }
  return cell;
}

function actionCell(row, name) {
  const cell = qs("[data-analytics-cell-template]").content.firstElementChild.cloneNode(true);
  const count = cell.querySelector("[data-cell-count]");
  const seen = row.actions?.[name];
  if (!seen) {
    // A dash, never a zero. `0` under a column for a button that page does not
    // have reads as a page failing to convert rather than one the action was
    // never on, and it is a lie told in a fixed-width font.
    count.textContent = "—";
    count.classList.add(muted);
    cell.title = `${name} was never seen on ${row.path} in this window`;
    return cell;
  }
  count.textContent = number.format(seen.sessions);
  const events = `${number.format(seen.events)} event${seen.events === 1 ? "" : "s"}`;
  if (!row.sessions) {
    // No denominator is no rate. The store refuses to invent one and so does
    // this: the counts are real, the share of nothing is not.
    cell.querySelector("[data-cell-rate]").textContent = "—";
    cell.title = `${name}: ${events}, and no page view on this path to measure them against`;
    return cell;
  }
  cell.querySelector("[data-cell-rate]").textContent = `${(seen.rate * 100).toFixed(1)}%`;
  cell.title = `${name}: ${events}, from ${number.format(seen.sessions)} of ${number.format(row.sessions)} sessions that saw ${row.path}`;
  return cell;
}

function pathRow(row, columns, track) {
  const node = qs("[data-analytics-row-template]").content.firstElementChild.cloneNode(true);
  node.style.gridTemplateColumns = track;
  const path = node.querySelector("[data-row-path]");
  path.textContent = row.path;
  path.title = row.path;
  node.querySelector("[data-row-views]").textContent = row.views ? number.format(row.views) : "—";
  node.querySelector("[data-row-sessions]").textContent = row.sessions ? number.format(row.sessions) : "—";
  // Appended to the row itself rather than into a wrapper: the cells have to be
  // children of the grid, or they are one column between them.
  for (const column of columns) node.append(actionCell(row, column.key));
  return node;
}

// One path: the line in the grid, and the panel it opens onto. The block is
// what carries the path, because the row is a grid and the panel must not be a
// cell of it.
function pathBlock(row, columns, track) {
  const block = el("div");
  block.dataset.analyticsPath = row.path;
  block.append(pathRow(row, columns, track));
  showFold(block, opened.has(row.path));
  return block;
}

// ---------------------------------------------------------------------------
// The fold
// ---------------------------------------------------------------------------

// Which paths are opened out, remembered across navigations — the same rule
// /cluster keeps. Shut is the default because the grid is what somebody came to
// scan; open is remembered because a row somebody opened is a row they are
// working on, and walking to /traces and back should not close it.
//
// Keyed by path rather than by position: the grid re-sorts on a press and on
// every window, and yesterday's third row is not today's.
const opened = new Set(JSON.parse(localStorage.getItem("guard.analytics.open") || "[]"));

function rememberOpened() {
  localStorage.setItem("guard.analytics.open", JSON.stringify([...opened]));
}

const pathOf = (path) => (paths || []).find((row) => row.path === path);

// Open or shut one path. The panel is built on the way open and removed on the
// way shut rather than hidden, because `display:none` markup is still markup to
// every querySelector on the page: a panel that stayed would be a chart and an
// action list rebuilt for every path anybody had ever opened, on every pass,
// behind a fold nobody is looking at.
function showFold(block, open) {
  const row = block.querySelector("[data-analytics-row]");
  row.setAttribute("aria-expanded", open ? "true" : "false");
  const chevron = row.querySelector("[data-row-chevron]");
  if (chevron) chevron.style.transform = open ? "rotate(180deg)" : "";
  block.querySelector("[data-analytics-fold]")?.remove();
  if (!open) return;
  const data = pathOf(block.dataset.analyticsPath);
  if (data) block.append(foldPanel(data));
}

function foldPanel(row) {
  const panel = qs("[data-analytics-fold-template]").content.firstElementChild.cloneNode(true);
  foldActions(panel.querySelector("[data-fold-actions]"), row);
  foldTraces(panel.querySelector("[data-fold-traces]"), row.path);
  // Started here rather than awaited, so the panel opens now and the chart and
  // the sources land into boxes that already have their place: a fold that
  // waited on a read before appearing would be a press with nothing behind it.
  fillFold(panel, row.path);
  return panel;
}

// Every action on this path, pinned or not — the discovery half, and the only
// place an unpinned name can be seen at all. Ordered by the sessions that did
// it, because the list is read from the top and the one worth making a column
// of is the one people actually do.
function foldActions(host, row) {
  const names = Object.keys(row.actions || {});
  if (!names.length) {
    host.replaceChildren(el("p", `px-2 text-xs ${muted}`, "Nothing but page views on this path in this window."));
    return;
  }
  names.sort((left, right) => (row.actions[right].sessions - row.actions[left].sessions) || left.localeCompare(right));
  host.replaceChildren(...names.map((name) => actionLine(row, name)));
}

function actionLine(row, name) {
  const line = qs("[data-analytics-action-template]").content.firstElementChild.cloneNode(true);
  const cell = row.actions[name];
  const label = line.querySelector("[data-action-name]");
  label.textContent = name;
  label.title = `${number.format(cell.events)} event${cell.events === 1 ? "" : "s"}, from ${number.format(cell.sessions)} session${cell.sessions === 1 ? "" : "s"}`;
  line.querySelector("[data-action-count]").textContent = number.format(cell.sessions);
  // The same denominator the column uses, and the same silence where there is
  // none: a share of no page views is not a small share.
  line.querySelector("[data-action-rate]").textContent = row.sessions ? `${(cell.rate * 100).toFixed(1)}%` : "—";

  const pin = line.querySelector("[data-action-pin]");
  const isPinned = pinned.includes(name);
  pin.dataset.actionPin = name;
  pin.textContent = isPinned ? "Unpin" : "Pin";
  pin.setAttribute("aria-pressed", isPinned ? "true" : "false");
  pin.title = isPinned
    ? `Stop drawing ${name} as a column. It is still counted.`
    : `Draw ${name} as a column, on every path`;
  return line;
}

// /analytics answers what happened on a page; /traces answers what the browsers
// were doing while it did. The session id is what joins them, and this link is
// the half of that join a person can press: a rate worth investigating is one
// press from the spans of the visits behind it.
//
// The link names the path, never the sessions. Which sessions saw a page is a
// question the database answers at the far end (`rum_path` in model.Filter), so
// this stays a URL somebody can read in a status bar, paste into a message and
// still open next week.
//
// The window travels too. Spans are kept in hours where the rollup is kept in
// days, so /traces has no 30-day option and anything longer than a week arrives
// as "all retained" — the widest true answer rather than a silently narrower
// one that would read as "nothing happened".
const traceWindows = { "24h": "24h", "7d": "7d" };

function foldTraces(link, path) {
  if (!link) return;
  link.href = `/traces?rum_path=${encodeURIComponent(path)}&range=${traceWindows[range] || "all"}`;
  link.title = `Every span from the browser sessions that saw ${path}`;
}

// The columns, sent as the whole ordered list rather than as a verb, because
// the order is part of the decision — the endpoint answers with what it stored,
// so the page settles on that rather than on the shuffle it drew.
async function savePins(next, whenItFails) {
  try {
    const actions = await request("/api/analytics/actions", {
      method: "POST",
      headers: adminHeaders(),
      body: JSON.stringify({ pinned: next }),
    });
    // Published rather than kept here, so a page holding the same list — and
    // this page on the way back to it — is corrected without a round trip.
    set("analytics.actions", actions);
    pinned = pinnedNames(actions);
    columnsProblem = "";
  } catch (failure) {
    // Said out loud and drawn nowhere else: an optimistic column that the
    // server refused would be a grid disagreeing with what is stored, which is
    // the one thing this endpoint answering with the whole list prevents.
    columnsProblem = `${whenItFails}: ${failure.message}`;
  }
  renderGrid();
  renderActions();
}

// A new pin goes last: the grid is read left to right, and a column that
// inserted itself in the middle would move the ones being read.
function togglePin(name) {
  const unpinning = pinned.includes(name);
  const next = unpinning ? pinned.filter((each) => each !== name) : [...pinned, name];
  return savePins(next, `${name} could not be ${unpinning ? "unpinned" : "pinned"}`);
}

// One place in the order, per press. The list in the dialog is vertical and the
// columns are horizontal, so the buttons say which of the two they mean — up in
// the list is left on the grid.
function movePin(name, step) {
  const at = pinned.indexOf(name);
  const to = at + step;
  if (at < 0 || to < 0 || to >= pinned.length) return Promise.resolve();
  const next = pinned.slice();
  next.splice(to, 0, ...next.splice(at, 1));
  return savePins(next, `${name} could not be moved`);
}

// A day-grain answer cannot move within the minute — the rollup is keyed by
// whole UTC days. Without this the grid rebuilding, which it does whenever a
// count moves, would be one request per open fold every time.
const SERIES_MAX_AGE_MS = 60_000;

// The chart and the sources, from one read: they are two halves of one panel
// over one window, and asking twice would be two windows in one box.
//
// Through the store like everything else, but drawn by hand: `ensure`'s render
// callback remembers what it put on screen, and this panel is built on open and
// thrown away on close, so that bookkeeping would be about a node that is gone.
// Called with no renderer it is still what is wanted here — one request shared
// between concurrent callers, and the answer published.
async function fillFold(panel, path) {
  const chart = panel.querySelector("[data-fold-chart]");
  const legend = panel.querySelector("[data-fold-legend]");
  const sources = panel.querySelector("[data-fold-sources]");
  const key = `analytics.path.${range}.${path}`;
  const asked = range;
  const known = get(key);
  if (known) drawFold(chart, legend, sources, path, known);
  if (known && Date.now() - freshness(key) < SERIES_MAX_AGE_MS) return;
  try {
    const answer = await ensure(key, () => request(`/api/analytics/path?path=${encodeURIComponent(path)}&range=${asked}`));
    // The fold may have been shut and the window may have moved on while this
    // was in flight, and either makes this answer somebody else's.
    if (chart.isConnected && asked === range) drawFold(chart, legend, sources, path, answer);
  } catch (failure) {
    // Only where there is nothing on screen. A panel drawn from the last read
    // is worth more than the reason a refresh of it failed.
    if (chart.isConnected && asked === range && !known) {
      chart.replaceChildren(el("p", `text-xs ${muted}`, failure.message));
      sources.replaceChildren();
    }
  }
}

function drawFold(chart, legend, sources, path, answer) {
  drawSeries(chart, legend, answer?.series);
  drawSources(sources, path, answer?.sources);
}

// Drawn through the panel renderer every other chart in guard goes through,
// from a frame built here — the endpoint answers days rather than a frame,
// because what a path did is not a saved view.
//
// Two series in one chart rather than two charts: a day where the lines part is
// a day one visit read the page five times, and that is only visible when they
// are drawn against the same axis.
function drawSeries(host, legend, points) {
  if (!points?.length) {
    host.replaceChildren(el("p", `text-xs ${muted}`, "No page view on this path in this window."));
    legend.replaceChildren();
    return;
  }
  const rows = [];
  for (const point of points) rows.push([point.day, "views", point.views], [point.day, "sessions", point.sessions]);
  // `measure` because the series are the measure here; without it the tooltip
  // reads "events" under the line counting sessions.
  const entries = draw(host, { panel: "timeseries", series: ["views", "sessions"], rows }, { measure: "count" });
  legend.replaceChildren(...entries.map((entry) => {
    // The class strings are literal so Tailwind can find them; the colour is
    // inline for the reason it cannot be.
    const node = el("span", "inline-flex items-center gap-2");
    const dot = el("span", "size-2 shrink-0 rounded-full");
    dot.style.background = entry.colour;
    node.append(dot, text(entry.label));
    return node;
  }));
}

// Where the sessions on this path came from, biggest first — the other
// direction from the grid, which says what happened once they were here.
//
// The share is against the path's own sessions, which is the number the row
// above is drawn with and the number these lines add up to: every session that
// saw the page is in exactly one of them, because attribution is filed against
// the page view that brought it.
function drawSources(host, path, sources) {
  if (!sources?.length) {
    host.replaceChildren(el("p", `px-2 text-xs ${muted}`, "Nothing arrived on this path in this window."));
    return;
  }
  const row = pathOf(path);
  host.replaceChildren(...sources.map((source) => sourceLine(source, row)));
}

// Direct is a line rather than the remainder somebody works out: a session with
// no campaign and no referrer is one that typed the address, opened a bookmark
// or came out of an app that sends neither, and that is an answer worth reading
// beside the campaigns somebody paid for.
const sourceName = (source) => source.source || source.referrer || "Direct";

// What is left once the name has taken the line: the medium and the campaign
// that were put in the link, and the host the browser actually came from where
// the source has already said something else.
function sourceDetail(source) {
  const rest = [source.medium, source.campaign];
  if (source.source && source.referrer) rest.push(source.referrer);
  return rest.filter(Boolean).join(" · ");
}

function sourceLine(source, row) {
  const line = qs("[data-analytics-source-template]").content.firstElementChild.cloneNode(true);
  const name = sourceName(source);
  const detail = sourceDetail(source);
  line.querySelector("[data-source-name]").textContent = name;
  line.querySelector("[data-source-detail]").textContent = detail;
  // Both are truncated to keep the figures in their columns, so the whole of
  // either is on the row itself.
  line.title = [name, detail].filter(Boolean).join(" · ");
  line.querySelector("[data-source-count]").textContent = number.format(source.sessions);
  // The same silence the action cells keep: a share of no sessions is not a
  // small share. It can only happen against a grid read a moment earlier than
  // this list, and inventing a percentage for that is worse than a dash.
  line.querySelector("[data-source-share]").textContent =
    row?.sessions ? `${((source.sessions / row.sessions) * 100).toFixed(1)}%` : "—";
  return line;
}

function renderGrid() {
  const host = qs("[data-analytics-grid]");
  // Nothing read yet — the markup's own "Loading paths…" is still the truth.
  if (!host || !paths) return;
  if (!paths.length) {
    say("No page views in this window.");
    return;
  }

  const columns = pinned.map((name) => ({ key: name, label: name, title: `${name} — the share of each path's sessions that did it` }));
  const track = tracks(columns.length);
  const table = el("div");
  // The grid is as wide as its columns need, and the scroller is what gives
  // way. Set here rather than as a class for the reason the track list is:
  // the number of columns is data.
  table.style.minWidth = `${PATH_MIN_REM + COUNT_REM * 2 + columns.length * ACTION_REM}rem`;

  const heads = el("div", "grid items-end gap-3");
  heads.style.gridTemplateColumns = track;
  heads.append(head({ key: "path", label: "Path" }), head({ key: "views", label: "Views" }), head({ key: "sessions", label: "Sessions" }));
  for (const column of columns) heads.append(head(column));
  table.append(heads);

  const ordered = paths.slice().sort((left, right) =>
    // Ties break on the path, always, so the grid does not reshuffle under
    // somebody reading it when the next window lands with the same figures.
    compare(left, right, sort.key, sort.descending) || left.path.localeCompare(right.path));
  for (const row of ordered) table.append(pathBlock(row, columns, track));

  const scroller = el("div", "overflow-x-auto");
  scroller.append(table);

  const count = `${number.format(paths.length)} path${paths.length === 1 ? "" : "s"} in this window.`;
  const above = [el("p", `pb-1 text-xs ${muted}`, pathsStale ? `${count} Refreshing…` : count)];
  if (columnsProblem) above.push(el("p", "pb-1 text-xs text-destructive", columnsProblem));
  const below = [];
  // Only when the columns are known to be absent rather than unread — the two
  // look identical on screen and mean opposite things.
  if (!columns.length && !columnsProblem) {
    below.push(el("p", `pt-3 text-xs ${muted}`,
      "No action is pinned, so this is page views alone. Every action is still counted — open a path to see the ones it has, and pin one to make it a column."));
  }
  host.replaceChildren(...above, scroller, ...below);
}

// The pinned names, in the order the grid draws them. The endpoint already
// answers pinned-first in stored position order, so filtering keeps it.
const pinnedNames = (actions) => (actions || []).filter((action) => action.pinned).map((action) => action.name);

async function refreshActions() {
  try {
    const actions = await ensure("analytics.actions", () => request("/api/analytics/actions"), (list) => {
      pinned = pinnedNames(list);
      columnsProblem = "";
      renderGrid();
      renderActions();
    });
    // ensure draws only when the answer changed, so a recovery is recorded
    // here: the read that succeeded after one that did not is still a read
    // where nothing moved, and the warning would otherwise stay on screen.
    if (columnsProblem) {
      columnsProblem = "";
      pinned = pinnedNames(actions);
      renderGrid();
      renderActions();
    }
  } catch (failure) {
    // The rows are kept. The columns are what is in doubt, and the last known
    // set of them is a better grid than none — as long as it says so.
    columnsProblem = `The pinned actions could not be read, so a column may be missing: ${failure.message}`;
    renderGrid();
    renderActions();
  }
}

async function refreshWindow() {
  if (!qs("[data-analytics-grid]")) return;
  // The window this request is for, pinned before it leaves. The store
  // publishes to whoever asked, and a slow read of 90d must not repaint a page
  // that has since been switched to 24h.
  const asked = range;
  const key = `analytics.${asked}`;
  windowsRead.add(key);
  try {
    await ensure(key, () => request(`/api/analytics?range=${asked}`), (answer, stale) => {
      if (asked !== range) return;
      renderStrip(answer.summary);
      paths = answer.paths || [];
      pathsStale = stale;
      renderGrid();
    });
    // The remembered rows turned out to be the live ones, so ensure drew
    // nothing the second time — and "Refreshing…" would sit there having
    // already finished. Cleared here, which is the only place that knows.
    if (asked === range && pathsStale) {
      pathsStale = false;
      renderGrid();
    }
  } catch (failure) {
    if (asked !== range) return;
    // The strip and the rows go with the message. Four confident numbers above
    // "the window could not be read" are four numbers from some other window,
    // and the reader has no way to tell which.
    renderStrip(null);
    paths = null;
    say(failure.message);
  }
}

export async function refreshAnalytics() {
  if (!qs("[data-analytics-page]")) return;
  const select = qs("[data-analytics-range]");
  if (select) select.value = range;
  let health;
  try {
    // Not gated on the live band being open: health is what decides whether it
    // is, so this one read happens on every pass whatever is on screen.
    health = await ensure("analytics.health", () => request("/api/analytics/health"), renderBands);
  } catch (failure) {
    // Health is the page's own answer about itself. Unreadable, the live band
    // is the least wrong of the three — it is the only one with somewhere to
    // put the reason.
    band("live", true);
    say(failure.message);
    return;
  }
  // Together, because the grid is not drawn until both have landed and neither
  // is on the other's critical path.
  if (isLive(health)) await Promise.all([refreshWindow(), refreshActions()]);
}

// ---------------------------------------------------------------------------
// The controls
// ---------------------------------------------------------------------------
//
// Two dialogs, and between them the two things the grid is made of: which
// actions are columns, and what counts as one path. Both are checkbox overlays,
// so they are present to every querySelector on this page whether or not
// anybody has opened one — which is why nothing below draws without asking.
// A closed panel that kept rebuilding its rows is a page that reads as slow for
// a reason nothing on screen explains.
const ACTIONS_PANEL = "analytics-actions";
const RULES_PANEL = "analytics-rules";

const panelOpen = (id) => Boolean(document.getElementById(id)?.checked);

function actionRow(action, index, total) {
  const row = qs("[data-analytics-action-row-template]").content.firstElementChild.cloneNode(true);
  row.dataset.analyticsAction = action.name;
  row.querySelector("[data-action-row-name]").textContent = action.name;
  row.querySelector("[data-action-row-events]").textContent =
    action.events ? `${number.format(action.events)} event${action.events === 1 ? "" : "s"}` : "—";
  // Last seen rather than first: the question asked of a name on a list is
  // whether the page still fires it, and a rename leaves the old one here
  // looking exactly like the new one until this line is read.
  row.querySelector("[data-action-row-seen]").textContent =
    action.last_seen ? `last seen ${relativeTime(action.last_seen)}` : "never seen";

  const toggle = row.querySelector("[data-action-toggle]");
  toggle.textContent = action.pinned ? "Unpin" : "Pin";
  toggle.setAttribute("aria-pressed", action.pinned ? "true" : "false");

  const move = row.querySelector("[data-action-move]");
  if (!action.pinned) {
    // An unpinned action has no position — it is not drawn — so the buttons go
    // rather than sit there disabled asking to be pressed.
    move.remove();
  } else {
    move.querySelector("[data-action-up]").disabled = index === 0;
    move.querySelector("[data-action-down]").disabled = index === total - 1;
  }
  return row;
}

function actionGroup(title, lede, actions, whenEmpty) {
  const group = el("section", "space-y-2");
  group.append(
    el("h3", "text-xs font-semibold uppercase tracking-[.16em] text-muted-foreground", title),
    el("p", `text-xs ${muted}`, lede),
  );
  if (!actions.length) {
    group.append(el("p", `text-sm ${muted}`, whenEmpty));
    return group;
  }
  group.append(...actions.map((action, index) => actionRow(action, index, actions.length)));
  return group;
}

// The list is read from the store rather than held here, so the answer a pin
// came back with, the one the grid drew its columns from and the one this
// dialog draws are one value. Two copies is two chances to list a column
// somebody has just unpinned.
function renderActions() {
  const host = qs("[data-analytics-actions]");
  if (!host || !panelOpen(ACTIONS_PANEL)) return;
  const known = get("analytics.actions");
  // The same sentence the grid carries, on the surface the press was made on:
  // a refusal drawn only behind the dialog is a refusal nobody reads.
  const parts = columnsProblem ? [el("p", "text-xs text-destructive", columnsProblem)] : [];
  // Nothing read yet — the markup's own "Loading actions…" is still the truth,
  // and "nothing has been sent" is a different sentence from "not asked yet".
  if (!known) {
    if (columnsProblem) host.replaceChildren(...parts);
    return;
  }
  if (!known.length) {
    parts.push(el("p", `text-sm ${muted}`,
      "Nothing but page views so far. page_view is the tracker's own and is the Views column, so it is never on this list — everything here arrives from guard.track."));
    host.replaceChildren(...parts);
    return;
  }
  // Pinned first and in stored order, which is what the endpoint already
  // answers in: the group headings are the two states, and the order inside the
  // first one is the order of the columns.
  parts.push(
    actionGroup("Columns", "Left to right, in the order the grid draws them.",
      known.filter((action) => action.pinned),
      "No action is pinned, so the grid is page views alone."),
    actionGroup("Discovered", "Counted just the same, and listed under every path they happened on.",
      known.filter((action) => !action.pinned),
      "Every name the tracker has sent is a column."),
  );
  host.replaceChildren(...parts);
}

// Deleting is the one press here that cannot be undone, so it is the one that
// asks — with the name typed in full, and with what goes with it said out loud.
// Unpinning hides a column; this drops the history behind it.
async function deleteAction(name) {
  const agreed = await ask({
    title: `Delete ${name}?`,
    body: "This removes the name and everything counted under it: the rollup, the sessions that did it and the raw events. "
      + "The page views on those paths stay, because they are a different action, and the next beacon carrying this name discovers it again — "
      + "so this is a purge of the history, not a mute.",
    confirm: "Delete it",
    phrase: name,
  });
  if (!agreed) return;
  try {
    await request(`/api/analytics/${encodeURIComponent(name)}`, { method: "DELETE", headers: adminHeaders() });
  } catch (failure) {
    columnsProblem = `${name} could not be deleted: ${failure.message}`;
    renderGrid();
    renderActions();
    return;
  }
  // Every window remembered a grid with this action in it, and one of them is
  // on screen. Dropped before the refresh, or `ensure` draws the old answer
  // first and the column comes back for a moment.
  for (const key of windowsRead) forget(key);
  windowsRead.clear();
  columnsProblem = "";
  await refreshAnalytics();
}

// ---------------------------------------------------------------------------
// Path rules
// ---------------------------------------------------------------------------
//
// The rows are the form: read out of the DOM on a save and on every preview,
// rather than mirrored into an array here that a keystroke could get out of
// step with. The list is small and it is only ever edited in one place.
let previewTimer;
let previewToken = 0;
// Whether anything has been typed since the dialog opened. A read that lands
// after somebody has started writing a rule must not refill the form under
// them — that is a half-written pattern disappearing for no visible reason.
let rulesEdited = false;

function ruleRow(rule) {
  const row = qs("[data-analytics-rule-template]").content.firstElementChild.cloneNode(true);
  row.querySelector('[data-rule-field="pattern"]').value = rule?.pattern || "";
  row.querySelector('[data-rule-field="replacement"]').value = rule?.replacement || "";
  return row;
}

// A rule list can legitimately be empty — most instances never need one — so
// the empty state is a sentence rather than a blank box that looks broken.
function syncRulesEmpty(host) {
  const rows = qsa("[data-analytics-rule]", host).length;
  const said = qs("[data-rules-empty]", host);
  if (rows && said) said.remove();
  if (!rows && !said) {
    const note = el("p", `text-sm ${muted}`, "No rule. Every path is counted exactly as it arrived.");
    note.dataset.rulesEmpty = "";
    host.append(note);
  }
}

function drawRules(list) {
  const host = qs("[data-analytics-rules]");
  if (!host) return;
  host.replaceChildren(...(list || []).map(ruleRow));
  syncRulesEmpty(host);
}

// The rows as they stand, including a half-written one: the preview shows the
// same refusal the save would, and being told that a rule has no replacement
// yet is more use than a preview that quietly ignores the row.
function collectRules() {
  return qsa("[data-analytics-rule]").map((row) => ({
    pattern: row.querySelector('[data-rule-field="pattern"]').value.trim(),
    replacement: row.querySelector('[data-rule-field="replacement"]').value.trim(),
  })).filter((rule) => rule.pattern || rule.replacement);
}

// The preview runs against the paths the tracker actually sent, which the
// server picks — a rule proved against paths this page supplied would be a rule
// proved against a site somebody imagined.
async function previewRules() {
  const host = qs("[data-analytics-preview]");
  if (!host || !panelOpen(RULES_PANEL)) return;
  const summary = qs("[data-rules-summary]");
  const error = qs("[data-rules-error]");
  const token = ++previewToken;
  try {
    const answers = await request("/api/analytics/preview", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rules: collectRules() }),
    });
    // A slower earlier preview must not overwrite a faster later one.
    if (token !== previewToken) return;
    error.textContent = "";
    drawPreview(host, summary, answers);
  } catch (failure) {
    if (token !== previewToken) return;
    // Cleared rather than left. What is on screen was drawn from a rule that
    // has since been edited, and a list describing the previous one beside a
    // message about this one is worse than no list.
    host.replaceChildren();
    summary.textContent = "";
    error.textContent = failure.message;
  }
}

function drawPreview(host, summary, answers) {
  if (!answers?.length) {
    summary.textContent = "";
    host.replaceChildren(el("p", `px-1 text-xs ${muted}`,
      "No path has arrived yet, so there is nothing to prove a rule against."));
    return;
  }
  const collapsed = answers.filter((answer) => answer.result !== answer.path);
  const into = new Set(collapsed.map((answer) => answer.result));
  summary.textContent = collapsed.length
    ? `${number.format(collapsed.length)} of ${number.format(answers.length)} recent paths collapse into ${number.format(into.size)} row${into.size === 1 ? "" : "s"}.`
    : `Nothing changes for the ${number.format(answers.length)} most recent paths.`;
  // What the rule caught first: a preview is read to check that it did
  // something, and the paths it left alone are the ones already understood.
  host.replaceChildren(...[...collapsed, ...answers.filter((answer) => answer.result === answer.path)]
    .map((answer) => previewRow(answer)));
}

function previewRow(answer) {
  const row = qs("[data-analytics-preview-template]").content.firstElementChild.cloneNode(true);
  const path = row.querySelector("[data-rule-path]");
  path.textContent = answer.path;
  path.title = answer.path;
  if (answer.result === answer.path) {
    // Untouched: the arrow and the second copy of the same string are noise on
    // the ninety rows a rule does nothing to.
    row.querySelector("[data-rule-arrow]").remove();
    row.querySelector("[data-rule-result]").remove();
    path.classList.add(muted);
    return row;
  }
  const result = row.querySelector("[data-rule-result]");
  result.textContent = answer.result;
  result.title = answer.result;
  return row;
}

function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => { previewRules().catch(() => {}); }, 250);
}

// Filled on open, and only then: the rules move when somebody saves them here,
// so there is nothing for a timer to notice and everything for one to trample.
async function fillRules() {
  const host = qs("[data-analytics-rules]");
  if (!host || !panelOpen(RULES_PANEL)) return;
  rulesEdited = false;
  const known = get("analytics.rules");
  if (known) drawRules(known);
  schedulePreview();
  const status = qs("[data-rules-status]");
  try {
    const list = await ensure("analytics.rules", () => request("/api/analytics/rules"));
    // The dialog may have been shut and the form may have been typed into while
    // this was in flight, and either makes this answer somebody else's.
    if (!panelOpen(RULES_PANEL) || rulesEdited) return;
    drawRules(list);
    schedulePreview();
  } catch (failure) {
    // Only where there is nothing on screen: a form drawn from the last read is
    // worth more than the reason a refresh of it failed.
    if (panelOpen(RULES_PANEL) && !known && status) status.textContent = failure.message;
  }
}

async function saveRules() {
  const status = qs("[data-rules-status]");
  const error = qs("[data-rules-error]");
  status.textContent = "Saving…";
  try {
    const saved = await request("/api/analytics/rules", {
      method: "POST",
      headers: adminHeaders(),
      body: JSON.stringify({ rules: collectRules() }),
    });
    // The answer is the stored list, so the form settles on what guard kept —
    // the trimming, the lowercasing and the order it decided.
    set("analytics.rules", saved);
    rulesEdited = false;
    drawRules(saved);
    error.textContent = "";
    // Said every time, because it is the one thing about this feature that
    // surprises people: the days already rolled up keep the paths they were
    // counted under.
    status.textContent = "Saved. It shapes what is counted from now on.";
    schedulePreview();
  } catch (failure) {
    status.textContent = "";
    error.textContent = failure.message;
  }
}

document.addEventListener("click", (event) => {
  const copy = event.target.closest("[data-analytics-copy]");
  if (copy) copySnippet(copy);

  const pin = event.target.closest("[data-action-pin]");
  if (pin) {
    togglePin(pin.dataset.actionPin).catch(() => {});
    return;
  }

  // The actions dialog. Every press here is about one row, and the row carries
  // the name — an action's name is its id, because it is what the tracker sends
  // and what the column is called.
  const listed = event.target.closest("[data-analytics-action]");
  if (listed) {
    const name = listed.dataset.analyticsAction;
    if (event.target.closest("[data-action-toggle]")) togglePin(name).catch(() => {});
    else if (event.target.closest("[data-action-up]")) movePin(name, -1).catch(() => {});
    else if (event.target.closest("[data-action-down]")) movePin(name, 1).catch(() => {});
    else if (event.target.closest("[data-action-delete]")) deleteAction(name).catch(() => {});
    return;
  }

  // The rules dialog. Adding, removing and reordering are DOM edits and nothing
  // else: what is stored is decided by one press, and until then the rows are a
  // form somebody is still writing.
  if (event.target.closest("[data-rule-add]")) {
    const rules = qs("[data-analytics-rules]");
    const row = ruleRow();
    rules.append(row);
    syncRulesEmpty(rules);
    rulesEdited = true;
    row.querySelector('[data-rule-field="pattern"]').focus();
    return;
  }
  const rule = event.target.closest("[data-analytics-rule]");
  if (rule) {
    const rules = rule.parentNode;
    const step = event.target.closest("[data-rule-up]") ? -1 : event.target.closest("[data-rule-down]") ? 1 : 0;
    // Order is the rule — the first match wins — so moving a row is an edit of
    // the same weight as typing in one, and it re-runs the preview.
    if (step) {
      const sibling = step > 0 ? rule.nextElementSibling : rule.previousElementSibling;
      if (sibling) rules.insertBefore(step > 0 ? sibling : rule, step > 0 ? rule : sibling);
    } else if (event.target.closest("[data-rule-remove]")) {
      rule.remove();
      syncRulesEmpty(rules);
    } else {
      return;
    }
    rulesEdited = true;
    schedulePreview();
    return;
  }
  if (event.target.closest("[data-rules-save]")) {
    saveRules().catch(() => {});
    return;
  }

  const row = event.target.closest("[data-analytics-row]");
  if (row) {
    const block = row.closest("[data-analytics-path]");
    const path = block.dataset.analyticsPath;
    const open = opened.delete(path) ? false : (opened.add(path), true);
    rememberOpened();
    showFold(block, open);
    return;
  }

  const column = event.target.closest("[data-analytics-sort]");
  if (!column) return;
  const key = column.dataset.analyticsSort;
  // A column somebody has just reached for starts in the direction they mean
  // by it: a path is read A to Z, and every number here is read biggest first.
  // The same column again is the flip.
  sort = sort.key === key ? { key, descending: !sort.descending } : { key, descending: key !== "path" };
  renderGrid();
});

document.addEventListener("input", (event) => {
  if (!event.target.matches("[data-rule-field]")) return;
  rulesEdited = true;
  schedulePreview();
});

document.addEventListener("change", (event) => {
  // A dialog that was drawn while it was shut would open onto whatever was
  // true the last time something else redrew it, so both are filled on the way
  // open — and neither is touched while it is closed.
  if (event.target.id === ACTIONS_PANEL && event.target.checked) {
    renderActions();
    refreshActions().catch(() => {});
    return;
  }
  if (event.target.id === RULES_PANEL && event.target.checked) {
    fillRules().catch(() => {});
    return;
  }

  const select = event.target.closest("[data-analytics-range]");
  if (!select) return;
  range = select.value;
  refreshAnalytics().catch(() => {});
});

// Escape shuts the overlay, which a checkbox does not do by itself. Not while a
// <dialog> is up: the typed confirmation in front of a delete is the browser's
// modal, it handles its own Escape, and closing the panel underneath it would
// leave somebody agreeing to something they can no longer see.
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape" || qs("dialog[open]")) return;
  for (const id of [ACTIONS_PANEL, RULES_PANEL]) {
    const toggle = document.getElementById(id);
    if (toggle?.checked) toggle.checked = false;
  }
});
