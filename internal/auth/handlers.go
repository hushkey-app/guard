package auth

// The three URLs the browser visits: start a sign-in, come back from one, sign
// out. They are ordinary handlers on the mux rather than typed endpoints,
// because none of them answers with JSON — every one of them ends in a redirect
// or a Set-Cookie, which is what a browser flow is.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// The reasons a sign-in can end early. The browser is redirected to
// /login?error=<one of these>, and the login page renders the sentence beside
// it — never text from the query string and never text from the provider,
// because a page that prints whatever ?error= says is a phishing surface.
const (
	errCancelled  = "cancelled"
	errExpired    = "expired"
	errFailed     = "failed"
	errForbidden  = "forbidden"
	errUnverified = "unverified"
	errSignedOut  = "signed_out"
)

var loginMessages = map[string]string{
	errCancelled:  "That sign-in was cancelled.",
	errExpired:    "That sign-in expired before it finished. Try again.",
	errFailed:     "That sign-in could not be completed. Try again.",
	errForbidden:  "That address is not on the members list for this instance.",
	errUnverified: "The provider has not verified that email address.",
	errSignedOut:  "You are signed out.",
}

// Register mounts the sign-in URLs. Called even when sign-in is off — with no
// providers configured every one of them answers 404, which is the honest
// answer for a flow that cannot be started.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/{provider}/start", s.start)
	// Both methods, one handler: Google comes back as a redirect with a query,
	// Apple as a cross-site form POST. The difference is entirely in where the
	// parameters live, and ParseForm reads both.
	mux.HandleFunc("GET /auth/{provider}/callback", s.callbackHandler)
	mux.HandleFunc("POST /auth/{provider}/callback", s.callbackHandler)
	mux.HandleFunc("POST /auth/logout", s.logout)
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.provider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	state, err := randomString()
	if err != nil {
		s.fail(w, r, errFailed, "generate a sign-in state", err)
		return
	}
	nonce, err := randomString()
	if err != nil {
		s.fail(w, r, errFailed, "generate a sign-in nonce", err)
		return
	}
	redirect := s.callback(r, provider.ID())
	pending := model.LoginState{
		State:     state,
		Provider:  provider.ID(),
		Nonce:     nonce,
		Redirect:  redirect,
		Next:      safeNext(r.URL.Query().Get("next")),
		ExpiresAt: s.now().UTC().Add(stateLifetime),
	}
	if err := s.store.StartLogin(pending); err != nil {
		s.fail(w, r, errFailed, "store the sign-in state", err)
		return
	}
	// 303, not 302: this is a GET either way, and 303 is the one that says so.
	http.Redirect(w, r, provider.Authorize(redirect, state, nonce), http.StatusSeeOther)
}

func (s *Service) callbackHandler(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.provider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	// 1 MiB: Apple's form carries a `user` blob, and nothing legitimate here is
	// larger than a few kilobytes.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, errFailed, "read the provider's answer", err)
		return
	}
	// A refusal at the provider — the consent screen was cancelled, or the
	// account is not permitted to use this client. It is not an error on
	// guard's side and it should not read like one.
	if reason := r.FormValue("error"); reason != "" {
		slog.Info("sign-in was refused at the provider",
			slog.String("provider", provider.ID()), slog.String("reason", reason))
		s.toLogin(w, r, errCancelled, "/")
		return
	}
	pending, err := s.store.ClaimLogin(r.FormValue("state"))
	if err != nil {
		// Unknown, replayed or expired — indistinguishable on purpose, and all
		// three mean "start again".
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("sign-in state could not be read", slog.Any("err", err))
		}
		s.toLogin(w, r, errExpired, "/")
		return
	}
	if pending.Provider != provider.ID() {
		slog.Warn("sign-in came back to the wrong provider",
			slog.String("started", pending.Provider), slog.String("returned", provider.ID()))
		s.toLogin(w, r, errFailed, "/")
		return
	}
	code := r.FormValue("code")
	if code == "" {
		s.toLogin(w, r, errFailed, pending.Next)
		return
	}
	identity, err := provider.Exchange(r.Context(), code, pending.Redirect, pending.Nonce)
	if err != nil {
		s.fail(w, r, errFailed, "exchange the sign-in code", err)
		return
	}
	// Apple sends the name once, in the form rather than the token, and only on
	// the first authorization ever. Taken only when the token had none, so a
	// forged form field cannot overwrite what the provider signed for.
	if identity.Name == "" {
		identity.Name = nameFromApple(r.FormValue("user"))
	}
	if identity.Email == "" || !identity.Verified {
		slog.Warn("sign-in carried no verified email",
			slog.String("provider", identity.Provider), slog.String("subject", identity.Subject))
		s.toLogin(w, r, errUnverified, pending.Next)
		return
	}
	member, allowed := s.member(identity.Email)
	if !allowed {
		// Logged at info with the address: an instance somebody is trying to
		// get into should say who, and this is the line an admin reads before
		// adding them to the list.
		slog.Info("sign-in refused — not a member",
			slog.String("email", identity.Email), slog.String("provider", identity.Provider))
		s.toLogin(w, r, errForbidden, pending.Next)
		return
	}
	if err := s.issue(w, r, identity, member); err != nil {
		s.fail(w, r, errFailed, "store the session", err)
		return
	}
	if err := s.store.MarkMemberSeen(identity.Email, identity.Provider, identity.Name); err != nil {
		// Decoration for the members page. It is not worth failing a sign-in
		// that has otherwise entirely succeeded.
		slog.Warn("could not record the sign-in", slog.Any("err", err))
	}
	slog.Info("signed in",
		slog.String("email", identity.Email),
		slog.String("provider", identity.Provider),
		slog.String("role", member.Role))
	http.Redirect(w, r, safeNext(pending.Next), http.StatusSeeOther)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	s.clear(w, r)
	s.toLogin(w, r, errSignedOut, "/")
}

// toLogin sends the browser back to the login page with a reason it may render.
func (s *Service) toLogin(w http.ResponseWriter, r *http.Request, reason, next string) {
	query := url.Values{}
	if reason != "" {
		query.Set("error", reason)
	}
	if next = safeNext(next); next != "/" {
		query.Set("next", next)
	}
	target := "/login"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// fail logs what actually went wrong and shows the person a sentence. The two
// are deliberately different: the log gets the provider's words, the browser
// gets one of six fixed messages.
func (s *Service) fail(w http.ResponseWriter, r *http.Request, reason, doing string, err error) {
	slog.Error("sign-in failed", slog.String("doing", doing), slog.Any("err", err))
	s.toLogin(w, r, reason, "/")
}
