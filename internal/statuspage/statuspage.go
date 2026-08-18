// Package statuspage puts the public status on the context, for the one page
// that renders it.
//
// It exists because of a rule that is easy to read past: every page under
// client/pages is compiled into views.wasm as well as into the server, so a
// page may import the model, the router and the context — and nothing that
// reaches a database. The status page read the store directly and the ordinary
// build was perfectly happy; the wasm build was not, because modernc.org/sqlite
// has no js/wasm target, and the failure named eight libc packages rather than
// the import that caused it.
//
// So the shape is the one internal/auth already uses for the login view: a
// middleware computes the value on the server and publishes it, and the page is
// a template over a struct. The page gets to stay server-rendered with no
// JavaScript, which was the point of it.
package statuspage

import (
	"log/slog"
	"net/http"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/state"
)

// Path is the one route this runs for. Named here rather than spelled twice,
// because the other place it appears is internal/auth's list of paths that need
// no session, and those two agreeing is what makes the page reachable.
const Path = "/status"

// Middleware publishes model.PublicStatus for the status page and does nothing
// at all for every other request.
//
// The path check is not an optimisation. This is the only unauthenticated read
// in guard, and running it for every request would mean the dashboard's seven
// polls a second each did a scan of the uptime table for a page nobody asked
// for.
func Middleware(store *telemetry.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != Path {
				next.ServeHTTP(w, r)
				return
			}
			status, err := store.PublicStatus()
			if err != nil {
				// The page renders an apology rather than a 500, because a
				// status page that returns an error page is a status page that
				// has become the outage. The zero value says "unknown", which
				// is the honest answer when guard cannot read its own store.
				slog.Error("public status unavailable", slog.Any("err", err))
				status = model.PublicStatus{State: "unknown", Days: telemetry.StatusDays}
			}
			next.ServeHTTP(w, r.WithContext(state.With(r.Context(), status)))
		})
	}
}
