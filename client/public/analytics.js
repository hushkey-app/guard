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
import { adminHeaders, el, muted, number, qs, request, text } from "./core.js";
import { draw } from "./charts.js";
import { ensure, freshness, get, set } from "./store.js";

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
  const slot = qs("[data-analytics-script]");
  if (slot) slot.textContent = `<script defer src="${location.origin}/v1/rum/track.js"></script>`;
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
  // Started here rather than awaited, so the panel opens now and the chart
  // lands into a box that already has its place: a fold that waited on a read
  // before appearing would be a press with nothing behind it.
  fillSeries(panel.querySelector("[data-fold-chart]"), panel.querySelector("[data-fold-legend]"), row.path);
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

// Pinning is how a column is created, and it is the whole ordered list rather
// than a verb, because the order is part of the decision — the endpoint answers
// with what it stored, so the page settles on that rather than on the shuffle
// it drew. A new pin goes last: the grid is read left to right, and a column
// that inserted itself in the middle would move the ones being read.
async function togglePin(name) {
  const unpinning = pinned.includes(name);
  const next = unpinning ? pinned.filter((each) => each !== name) : [...pinned, name];
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
    columnsProblem = `${name} could not be ${unpinning ? "unpinned" : "pinned"}: ${failure.message}`;
  }
  renderGrid();
}

// A day-grain series cannot move within the minute — the rollup is keyed by
// whole UTC days. Without this the grid rebuilding, which it does whenever a
// count moves, would be one request per open fold every time.
const SERIES_MAX_AGE_MS = 60_000;

// Through the store like everything else, but drawn by hand: `ensure`'s render
// callback remembers what it put on screen, and this panel is built on open and
// thrown away on close, so that bookkeeping would be about a node that is gone.
// Called with no renderer it is still what is wanted here — one request shared
// between concurrent callers, and the answer published.
async function fillSeries(host, legend, path) {
  const key = `analytics.path.${range}.${path}`;
  const asked = range;
  const known = get(key);
  if (known) drawSeries(host, legend, known);
  if (known && Date.now() - freshness(key) < SERIES_MAX_AGE_MS) return;
  try {
    const points = await ensure(key, () => request(`/api/analytics/path?path=${encodeURIComponent(path)}&range=${asked}`));
    // The fold may have been shut and the window may have moved on while this
    // was in flight, and either makes this answer somebody else's.
    if (host.isConnected && asked === range) drawSeries(host, legend, points);
  } catch (failure) {
    // Only where there is nothing on screen. A chart drawn from the last read
    // is worth more than the reason a refresh of it failed.
    if (host.isConnected && asked === range && !known) {
      host.replaceChildren(el("p", `text-xs ${muted}`, failure.message));
    }
  }
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
    });
    // ensure draws only when the answer changed, so a recovery is recorded
    // here: the read that succeeded after one that did not is still a read
    // where nothing moved, and the warning would otherwise stay on screen.
    if (columnsProblem) {
      columnsProblem = "";
      pinned = pinnedNames(actions);
      renderGrid();
    }
  } catch (failure) {
    // The rows are kept. The columns are what is in doubt, and the last known
    // set of them is a better grid than none — as long as it says so.
    columnsProblem = `The pinned actions could not be read, so a column may be missing: ${failure.message}`;
    renderGrid();
  }
}

async function refreshWindow() {
  if (!qs("[data-analytics-grid]")) return;
  // The window this request is for, pinned before it leaves. The store
  // publishes to whoever asked, and a slow read of 90d must not repaint a page
  // that has since been switched to 24h.
  const asked = range;
  try {
    await ensure(`analytics.${asked}`, () => request(`/api/analytics?range=${asked}`), (answer, stale) => {
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

document.addEventListener("click", (event) => {
  const copy = event.target.closest("[data-analytics-copy]");
  if (copy) copySnippet(copy);

  const pin = event.target.closest("[data-action-pin]");
  if (pin) {
    togglePin(pin.dataset.actionPin).catch(() => {});
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

document.addEventListener("change", (event) => {
  const select = event.target.closest("[data-analytics-range]");
  if (!select) return;
  range = select.value;
  refreshAnalytics().catch(() => {});
});
