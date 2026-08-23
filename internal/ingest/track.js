// Guard's tracker: page views, the actions somebody pressed, and the session
// that ties the two together. Served from /v1/rum/track.js, posted back to
// /v1/rum/events.
//
// The budget is two kilobytes minified with no build step, so this is written
// the way it is served: ES5, no dependencies, one closure. It reads the page
// and writes nothing but a session id to sessionStorage — no cookie, no
// address, no fingerprint — which is what lets it run without asking anybody
// for permission first.
(function (w, d) {
  var n = w.navigator;
  if (w.guard) return;
  if (n.doNotTrack === "1") {
    // Nothing at all, but still an answer: a page calling guard.track() and
    // getting a TypeError is the tracker breaking the site it measures.
    // session() answers with nothing rather than an id, so a span tagged from
    // it carries no session instead of one that will never join to anything.
    var quiet = function () {};
    w.guard = { track: quiet, session: quiet };
    return;
  }

  var tag = d.currentScript || d.querySelector("script[src*='track.js']");
  var endpoint =
    (tag && (tag.getAttribute("data-endpoint") || tag.src.replace(/track\.js.*$/, "events"))) ||
    "/v1/rum/events";

  var KEY = "guard.session",
    NAME = /^[a-z0-9_.-]{1,64}$/;

  // 16 random bytes, kept for as long as the tab is open and the visitor is
  // moving. One person in two tabs is two sessions, and guard says "sessions"
  // rather than "people" precisely because of that.
  function mint() {
    var random = w.crypto && w.crypto.getRandomValues(new Uint8Array(16)), out = "", i;
    // `| 256` is what makes a byte two hex digits: it truncates, and it puts a
    // leading 1 in front that the slice takes back off.
    for (i = 0; i < 16; i++) out += ((random ? random[i] : Math.random() * 256) | 256).toString(16).slice(1);
    return out;
  }

  function session() {
    var now = +new Date(), held;
    try {
      held = JSON.parse(w.sessionStorage.getItem(KEY));
      // Thirty minutes of quiet ends a session, which is the one part of GA4's
      // session model worth copying.
      if (!held || now - held.t > 18e5) held = { i: mint() };
      held.t = now;
      w.sessionStorage.setItem(KEY, JSON.stringify(held));
    } catch (e) {
      // Storage refused — a private window, or cookies off. A fresh id per
      // beacon overcounts sessions; a tracker that throws counts none.
      held = held || { i: mint() };
    }
    return held.i;
  }

  // The same normalisation guard does on arrival — query and hash are the
  // visit rather than the page, and /pricing/ is /pricing.
  function path() {
    return w.location.pathname.toLowerCase().replace(/\/+$/, "") || "/";
  }

  // Four strings, read before the query string is dropped: the campaign that
  // brought the session, and the referrer's host. Never the full referrer,
  // which is somebody else's private path.
  var source = {};
  w.location.search.replace(/[?&]utm_(source|medium|campaign)=([^&]*)/g, function (all, name, value) {
    source[name.charAt(0)] = decodeURIComponent(value).slice(0, 200);
  });
  var link = d.createElement("a");
  link.href = d.referrer;
  var referrer = link.host === w.location.host ? "" : link.host;

  var queue = [], on = "", timer = null, refused = 0, dead = false;

  function post(body, closing) {
    // sendBeacon is the only flush that survives a tab closing, and it cannot
    // report a status — so the door's answer is only ever read on the timer.
    if (closing && n.sendBeacon) return n.sendBeacon(endpoint, body);
    var call = new XMLHttpRequest();
    call.open("POST", endpoint, true);
    // No Content-Type is set, because sending a string already means
    // text/plain — the same thing sendBeacon sends, so both flushes look the
    // same at the door and neither pays for a preflight.
    call.onload = function () {
      // Twice refused is a tracker posting somewhere that will not have it.
      // Retrying that from every visitor's browser is how a misconfiguration
      // becomes a load test.
      if ((call.status / 100 | 0) === 4) dead = ++refused > 1;
      else refused = 0;
    };
    call.send(body);
  }

  function flush(closing) {
    clearTimeout(timer);
    timer = null;
    var batch = queue;
    queue = [];
    if (!batch.length || dead) return;
    post(JSON.stringify({ s: session(), p: on, u: source, r: referrer, e: batch }), closing);
  }

  function record(name, props) {
    if (dead) return;
    // A beacon carries one path, so a navigation closes the batch it was in:
    // events counted against the page after the one they happened on would be
    // a grid nobody can act on.
    var here = path();
    if (here !== on) { flush(); on = here; }
    queue.push({ n: name, t: +new Date(), d: props });
    // Fifty is the door's ceiling, and a fuller beacon is refused whole.
    if (queue.length > 49) flush();
    else if (!timer) timer = setTimeout(flush, 5e3);
  }

  function track(name, props) {
    // The door refuses a whole beacon over one bad name, so a typo here would
    // take the page views batched beside it. Dropping it is the smaller loss.
    if (!NAME.test(name)) return;
    var kept, prop, count = 0;
    for (prop in props) {
      if (count > 7 || prop.length > 64) continue;
      count++;
      (kept = kept || {})[prop] = ("" + props[prop]).slice(0, 200);
    }
    record(name, kept);
  }

  // A page view is "the path changed", which makes the first one and a
  // single-page app's tenth the same event with no extra call.
  function view() { if (path() !== on) record("page_view"); }

  function patch(method) {
    var was = w.history[method];
    if (was) {
      w.history[method] = function () {
        var answer = was.apply(this, arguments);
        view();
        return answer;
      };
    }
  }
  patch("pushState");
  patch("replaceState");
  w.addEventListener("popstate", view);

  // One delegated listener, in the capture phase so an app that stops
  // propagation still counts. The selector lives in the markup, where whoever
  // changes the markup can see it.
  d.addEventListener("click", function (event) {
    for (var node = event.target; node; node = node.parentNode) {
      var name = node.getAttribute && node.getAttribute("data-guard-track");
      if (name) return track(name);
    }
  }, true);

  d.addEventListener("visibilitychange", function () {
    if (d.visibilityState === "hidden") flush(true);
  });

  // track() is what the page calls; session() is what the OpenTelemetry web
  // SDK beside it tags its spans with. Guard indexes `rum.session_id`, so the
  // two halves of one visit — the pages somebody saw and the spans their
  // browser produced — are a single filter apart on /traces.
  w.guard = { track: track, session: session };
  view();
})(window, document);
