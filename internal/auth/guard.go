package auth

// The middleware, and the permission answer the API layer asks for.
//
// One pass over every request decides three things: who this is, whether they
// may proceed, and what the page should be told. Nothing downstream repeats the
// work — pages read the viewer from the context, endpoints read the member from
// it, and neither can forget to ask.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/api"
	"github.com/mirairoad/howl-go/core/state"
)

// open lists the paths that never need a session.
//
// /v1/ is the important one and the reason this list is not "everything except
// the pages": the OTLP receiver and the browser intake are how telemetry gets
// in, they are guarded by GUARD_TOKEN and by the origin allowlist respectively,
// and a collector cannot sign in with Google.
var open = []string{
	"/login",
	"/auth/",
	"/static/",
	"/v1/",
	"/healthz",
	"/_howl/", // the dev server's live-reload stream
	"/favicon.ico",
}

// assets are the open paths that also need no viewer at all.
func assets(path string) bool {
	return strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/_howl/") ||
		path == "/favicon.ico"
}

func isOpen(path string) bool {
	for _, prefix := range open {
		if path == prefix || strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// Guard is the middleware. It is a plain func(http.Handler) http.Handler, so it
// drops into app.Config.Use beside the framework's own.
//
// With sign-in off it still runs, and still publishes an empty viewer and the
// login page's view model — the login page has to be able to say "sign-in is
// not configured" rather than render an empty card.
func (s *Service) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The files, the live-reload stream and the favicon: the same bytes for
		// everybody, and nothing downstream reads a viewer. Skipped before the
		// session lookup rather than after, because a page pulls a dozen of
		// these and each one would otherwise be a SQLite read.
		if assets(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		viewer, member, signedIn := s.identify(w, r)
		if signedIn {
			ctx = state.With(ctx, viewer)
			ctx = state.With(ctx, member)
		}
		ctx = state.With(ctx, s.loginView(r, signedIn))
		r = r.WithContext(ctx)

		if !s.Enabled() || isOpen(r.URL.Path) {
			// Already signed in and looking at the login page: there is nothing
			// there for them.
			if signedIn && s.Enabled() && r.URL.Path == "/login" {
				http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if signedIn || s.machine(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.deny(w, r)
	})
}

// identify reads the request's session and the member behind it.
//
// The member is looked up per request rather than trusted from the session row,
// which is what makes removing somebody take effect immediately: their cookie
// still exists, the row it points at still exists, and the next request finds
// they are no longer on the list — so the session is deleted from under them.
func (s *Service) identify(w http.ResponseWriter, r *http.Request) (model.Viewer, model.Member, bool) {
	if !s.Enabled() {
		return model.Viewer{}, model.Member{}, false
	}
	session, ok := s.session(r)
	if !ok {
		return model.Viewer{}, model.Member{}, false
	}
	member, allowed := s.member(session.Email)
	if !allowed {
		slog.Info("session ended — no longer a member", slog.String("email", session.Email))
		s.clear(w, r)
		return model.Viewer{}, model.Member{}, false
	}
	return session.Viewer(), member, true
}

// machine reports a caller holding GUARD_TOKEN: an exporter, the seed tool,
// somebody's script. They never see a login page.
func (s *Service) machine(r *http.Request) bool {
	if s.cfg.APIToken == "" {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+s.cfg.APIToken
}

// loginView is what the login page renders.
func (s *Service) loginView(r *http.Request, signedIn bool) model.LoginView {
	view := model.LoginView{Providers: s.Buttons()}
	if r.URL.Path != "/login" || signedIn {
		return view
	}
	query := r.URL.Query()
	// The message comes from the table, never from the query — the parameter
	// only selects one.
	view.Error = loginMessages[query.Get("error")]
	if next := safeNext(query.Get("next")); next != "/" {
		view.Next = next
	}
	return view
}

// deny is the answer to a request from nobody.
//
// A browser navigating gets the login page; anything else gets 401 and a
// sentence, because a fetch that follows a redirect to an HTML login form and
// swaps it into the dashboard is worse than an error.
func (s *Service) deny(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.Header.Get("X-Partial") != "1" &&
		strings.Contains(r.Header.Get("Accept"), "text/html") {
		next := r.URL.Path
		if r.URL.RawQuery != "" {
			next += "?" + r.URL.RawQuery
		}
		s.toLogin(w, r, "", next)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "sign in to use this dashboard",
		"login": "/login",
	})
}

// Authorize is what the typed API layer asks for permission with. It replaces
// the bearer-only check guard had before, and keeps it: the token still works,
// because the things that hold it are not people.
//
// The order is deliberate. A machine with the token is allowed first, so
// automation never depends on the members table. Then, with sign-in on, the
// question becomes what the signed-in person may do: everyone on the list may
// read, and an endpoint declaring "admin" needs an admin. With sign-in off,
// guard behaves exactly as it always has — the token, or a warning.
func (s *Service) Authorize(r *http.Request, roles []string) error {
	if s.machine(r) {
		return nil
	}
	if !s.Enabled() {
		if s.cfg.APIToken == "" {
			slog.Warn("GUARD_TOKEN is unset and nobody has to sign in — write endpoints are open",
				slog.String("path", r.URL.Path), slog.Any("roles", roles))
			return nil
		}
		return api.Unauthorized("a valid bearer token is required")
	}
	member, ok := state.From[model.Member](r.Context())
	if !ok {
		return api.Unauthorized("sign in to use this dashboard")
	}
	for _, role := range roles {
		if role == model.RoleAdmin && !member.IsAdmin() {
			return api.Forbidden("only an admin can do this")
		}
	}
	return nil
}

// Viewer is who a page is being rendered for. The zero value means nobody,
// which on an open instance is everybody.
func Viewer(ctx context.Context) model.Viewer { return state.Get[model.Viewer](ctx) }

// Member is the signed-in person's place on the list, for an endpoint that
// needs to know whether they are an admin.
func Member(ctx context.Context) (model.Member, bool) { return state.From[model.Member](ctx) }

// Login is the login page's view model.
func Login(ctx context.Context) model.LoginView { return state.Get[model.LoginView](ctx) }
