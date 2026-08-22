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
import { el, muted, number, qs, request } from "./core.js";
import { ensure } from "./store.js";

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

// The rows are the next task. What this draws is the one thing the wiring can
// already prove: the window was read, and this is what came back for it.
function renderPaths(answer, stale) {
  const paths = answer.paths || [];
  const line = paths.length
    ? `${number.format(paths.length)} path${paths.length === 1 ? "" : "s"} in this window.`
    : "No page views in this window.";
  say(stale ? `${line} Refreshing…` : line);
}

async function refreshWindow() {
  if (!qs("[data-analytics-grid]")) return;
  // The window this request is for, pinned before it leaves. The store
  // publishes to whoever asked, and a slow read of 90d must not repaint a page
  // that has since been switched to 24h.
  const asked = range;
  try {
    await ensure(`analytics.${asked}`, () => request(`/api/analytics?range=${asked}`), (answer, stale) => {
      if (asked === range) renderPaths(answer, stale);
    });
  } catch (failure) {
    if (asked === range) say(failure.message);
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
  if (isLive(health)) await refreshWindow();
}

document.addEventListener("click", (event) => {
  const copy = event.target.closest("[data-analytics-copy]");
  if (copy) copySnippet(copy);
});

document.addEventListener("change", (event) => {
  const select = event.target.closest("[data-analytics-range]");
  if (!select) return;
  range = select.value;
  refreshAnalytics().catch(() => {});
});
