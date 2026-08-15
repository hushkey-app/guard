// The dashboard's store: state that lives outside the page.
//
// Every page here used to own its data — fetch on mount, render, throw away on
// unmount — so walking from /cluster to /logs and back re-fetched a machine
// list that had not changed and re-drew it from an empty frame. The data is not
// the page's; it belongs to the session.
//
// So this module is the store, and it is deliberately *outside the outlet*: the
// pages come and go through howl's client-side navigation, and this module is
// evaluated once and stays. A page reads from it, subscribes to it, and never
// owns it. Two consequences worth being explicit about:
//
//   - Navigating back is instant, because the value is already here. No fetch
//     is on the critical path of a navigation.
//   - There is one copy. Two pages reading the machine list read the same list,
//     so they cannot disagree, and a write from one is seen by the other.
//
// It is also mirrored into sessionStorage, which covers the one case module
// state cannot: a *cold* load — a reload, or a link opened in a new tab — where
// this module is evaluated fresh. The mirror dies with the tab, because a
// machine list that outlived its session would be a page confidently drawing
// something from yesterday.
//
// What it is not is a cache with opinions. Nothing here decides anything: every
// read is followed by a revalidation, and the renderer is told which of the two
// it is drawing so a page can say so.

const PREFIX = "guard.store.";

// The live state. Module scope is the point — this survives every navigation
// and is shared by every page.
const state = new Map();
const listeners = new Map();
const inflight = new Map();
// What is currently on screen, per key: the exact value a renderer was last
// handed. It is the difference between "this page has already drawn this" and
// "this page has never drawn anything", which the store cannot otherwise tell —
// and drawing again is not free, because it rebuilds the DOM under whoever is
// reading it.
const rendered = new Map();

// hydrate pulls the last session's values in on a cold start, once. Values
// arrive marked stale, so nothing can mistake them for a live read.
let hydrated = false;

function hydrate() {
  if (hydrated) return;
  hydrated = true;
  try {
    for (const key of Object.keys(sessionStorage)) {
      if (!key.startsWith(PREFIX)) continue;
      const raw = sessionStorage.getItem(key);
      if (raw) state.set(key.slice(PREFIX.length), { value: JSON.parse(raw), encoded: raw, at: 0 });
    }
  } catch {
    // A disabled or full sessionStorage costs a spinner, not a page.
  }
}

/** The current value, or undefined. Never fetches — asking is not loading. */
export function get(key) {
  hydrate();
  return state.get(key)?.value;
}

/** When that value was last confirmed live, as a timestamp (0 = never). */
export function freshness(key) {
  hydrate();
  return state.get(key)?.at || 0;
}

/**
 * Put a value in and tell everybody watching.
 *
 * Exported because a write is not always a fetch: saving a machine answers with
 * the new one, and publishing that is what makes every open page correct
 * without a round trip.
 */
export function set(key, value) {
  hydrate();
  const encoded = encode(value);
  const known = state.get(key);
  // Nothing changed: record that it was confirmed live and wake nobody. This
  // is the whole point of a background refresh — the answer is usually the
  // same answer, and re-rendering it costs a rebuilt DOM, a lost text
  // selection and a scroll position for no new information.
  if (known && known.encoded === encoded) {
    known.at = Date.now();
    return false;
  }
  state.set(key, { value, encoded, at: Date.now() });
  try {
    if (encoded !== undefined) sessionStorage.setItem(PREFIX + key, encoded);
  } catch { /* see hydrate */ }
  for (const listener of listeners.get(key) || []) {
    try {
      listener(value, false);
    } catch (failure) {
      // One bad subscriber must not stop the others being told.
      console.error(failure);
    }
  }
  return true;
}

// The comparison the store is honest about: a structural one. Two responses
// that serialise the same are the same answer, and anything JSON cannot hold
// does not belong in a store that is mirrored to sessionStorage anyway.
function encode(value) {
  try {
    return JSON.stringify(value);
  } catch {
    return undefined;
  }
}

/** Watch a key. Returns the unsubscribe, which a page's unmount should call. */
export function subscribe(key, listener) {
  hydrate();
  if (!listeners.has(key)) listeners.set(key, new Set());
  listeners.get(key).add(listener);
  return () => listeners.get(key)?.delete(listener);
}

/**
 * Draw what is known, then confirm it.
 *
 * render is called with (value, stale): once immediately if anything is known,
 * and once with the fetched value. The stale flag is not decoration — it is how
 * a page says "from your last visit" instead of quietly showing old numbers.
 *
 * Concurrent callers for one key share a single request: the live tick and a
 * mount landing together should be one fetch, not two.
 */
export async function ensure(key, load, render) {
  hydrate();
  const known = state.get(key);
  let drawn = false;
  // Already on screen: this exact value was handed to a renderer and nothing
  // has thrown the DOM away since. Drawing it again would rebuild the page
  // under whoever is reading it, lose a text selection and a scroll position,
  // and show them nothing they were not already looking at.
  const onScreen = known !== undefined && rendered.get(key) === known.encoded;
  if (known !== undefined && render && !onScreen) {
    try {
      render(known.value, true);
      rendered.set(key, known.encoded);
      drawn = true;
    } catch (failure) {
      // A remembered shape this build cannot draw — a field renamed since the
      // tab was opened — must not take the live path down with it.
      state.delete(key);
      console.error(failure);
    }
  }
  let request = inflight.get(key);
  if (!request) {
    request = load().finally(() => inflight.delete(key));
    inflight.set(key, request);
  }
  const fresh = await request;
  const changed = set(key, fresh);
  // Draw again only if there is something new to draw, or if nothing was drawn
  // from memory. A page that navigated back to an unchanged answer never
  // rebuilds its DOM — which is the difference between "instant" and "instant,
  // then a flicker a moment later".
  if (render && (changed || (!drawn && !onScreen))) {
    render(fresh, false);
    rendered.set(key, state.get(key)?.encoded);
  }
  return fresh;
}

/**
 * The page's DOM was thrown away, so nothing is on screen any more.
 *
 * Called from the outlet's unmount. Without it the store would believe a value
 * it handed to the previous page is still visible, and the next page — which
 * starts empty — would be given nothing to draw until the network answered.
 * That is exactly the "Loading…" this whole module exists to remove.
 */
export function screenCleared() {
  rendered.clear();
}

/** Drop a key — used when something makes the stored value plainly wrong. */
export function forget(key) {
  state.delete(key);
  rendered.delete(key);
  try {
    sessionStorage.removeItem(PREFIX + key);
  } catch { /* see hydrate */ }
}
