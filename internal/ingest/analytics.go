package ingest

// The analytics door: a third path on the browser intake.
//
// Not OTLP, on purpose. The OTLP JS exporter plus a web tracer is tens of
// kilobytes for a payload that is a name and a path, and the tracker that
// posts here has a two-kilobyte budget — so this takes guard's own JSON
// (`model.Beacon`, one-letter keys) instead.
//
// Everything else about it is the door it is mounted on rather than anything
// of its own: the same origin allowlist, the same limiter, the same 256 KB
// budget, the same preflight, and the same rule that no Origin at all is a
// relay and is allowed. It adds no configuration — GUARD_RUM_ORIGINS is what
// turns analytics on, because a second switch would be a second answer to "is
// the browser door open".

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// The tracker, served to somebody else's visitors. It lives beside the door it
// posts to rather than under client/public/, which is the dashboard's own
// JavaScript and where every file needs a Tailwind @source line — this one has
// no classes, no build step and no dependencies, and is embedded exactly as it
// is written.
//
//go:embed track.js
var tracker []byte

// trackJS serves the tracker.
//
// Cached for a day, and the header is also the ceiling on how long a fix to
// the tracker takes to reach a browser that already has it — the URL carries
// no version, because a version in it would be a number every customer's page
// has to be edited to change.
func trackJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(tracker)
}

func (h Handler) beacon(config Browser, limiter *rateLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cors(w, r, config.Origins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if !limiter.allow(address(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBrowserBody)

		// No Content-Type check: navigator.sendBeacon posts a string as
		// text/plain, which is what keeps the flush on a closing tab free of a
		// preflight it would never live long enough to complete.
		var beacon model.Beacon
		if err := json.NewDecoder(r.Body).Decode(&beacon); err != nil {
			http.Error(w, "could not read the beacon", http.StatusBadRequest)
			return
		}
		// The edge limits are answered here, before anything is written, so a
		// tracker with a bug is told it has one. The store validates again —
		// it must, since a second caller must not be able to write a name that
		// cannot be a column — but a store error is guard's fault and reads as
		// a 500, and these are the sender's.
		if err := beacon.Validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.Store.AddAnalytics(beacon); err != nil {
			http.Error(w, "could not store the beacon", http.StatusInternalServerError)
			return
		}
		// Nothing to answer with. The tracker's successful flush is usually a
		// sendBeacon from a tab that is already gone, and a body it will never
		// read is bytes on somebody's visitor's connection.
		w.WriteHeader(http.StatusNoContent)
	}
}
