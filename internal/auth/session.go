package auth

// The cookie, and the two derived strings around it: where guard is reached
// (so a redirect URI can be built) and where to send somebody afterwards.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// cookieName is guard's session cookie. Prefixed with the application because a
// browser sends every cookie for a host to every port on it: two things on
// localhost both calling their cookie "session" log each other out.
const cookieName = "guard_session"

// stateLifetime bounds a sign-in in flight. Ten minutes is longer than any
// consent screen and shorter than a coffee break.
const stateLifetime = 10 * time.Minute

// randomString is the source of every unguessable value here: the session
// token, the state, the nonce. 32 bytes from crypto/rand, url-safe.
func randomString() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// hashToken is what the database stores. See model.Session for why.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// issue creates the session and sets the cookie.
func (s *Service) issue(w http.ResponseWriter, r *http.Request, identity Identity, member model.Member) error {
	token, err := randomString()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	name := identity.Name
	if name == "" {
		name = member.Name
	}
	session := model.Session{
		Hash:      hashToken(token),
		Provider:  identity.Provider,
		Subject:   identity.Subject,
		Email:     model.NormalizeEmail(identity.Email),
		Name:      name,
		Picture:   identity.Picture,
		CreatedAt: now,
		ExpiresAt: now.Add(s.cfg.SessionTTL),
	}
	if err := s.store.CreateSession(session); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:  cookieName,
		Value: token,
		Path:  "/",
		// Lax, not Strict: Strict would withhold the cookie on the very
		// navigation that follows the provider's redirect, so the first thing
		// a person sees after signing in is the login page again.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   secure(r),
		Expires:  session.ExpiresAt,
		MaxAge:   int(s.cfg.SessionTTL / time.Second),
	})
	return nil
}

// clear ends the session named by the request's cookie and tells the browser to
// forget it. The row goes first: a cookie the browser keeps is harmless once
// nothing answers for it, and the reverse is not true.
func (s *Service) clear(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" {
		_ = s.store.DeleteSession(hashToken(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   secure(r),
		MaxAge:   -1,
	})
}

// session reads the request's session, if it has a live one.
func (s *Service) session(r *http.Request) (model.Session, bool) {
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return model.Session{}, false
	}
	session, err := s.store.Session(hashToken(cookie.Value))
	if err != nil {
		return model.Session{}, false
	}
	return session, true
}

// secure decides whether the cookie may only travel over TLS. Derived from the
// request rather than configured, so a laptop on http://localhost still gets a
// working login and a deployment behind TLS still gets a cookie that refuses
// to leave it.
func secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(forwarded(r, "X-Forwarded-Proto"), "https")
}

// forwarded reads the first value of a proxy header. A chain of proxies appends
// to these, and the leftmost is the one that faced the browser.
func forwarded(r *http.Request, name string) string {
	value := r.Header.Get(name)
	if value == "" {
		return ""
	}
	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = value[:comma]
	}
	return strings.TrimSpace(value)
}

// origin is the public base URL guard is reached at.
//
// GUARD_AUTH_BASE_URL wins when set, and behind a proxy it should be: the
// redirect URI has to match what is registered at the provider exactly, and
// everything else here is derived from headers a client can write. Without it,
// the worst a forged header does is send somebody to a redirect URI the
// provider will refuse — but "the provider refuses" is a worse error message
// than a configured string.
func (s *Service) origin(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	host := forwarded(r, "X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// callback is the redirect URI for one provider — the string that has to be
// registered in the Google console or the Apple portal, character for
// character.
func (s *Service) callback(r *http.Request, provider string) string {
	return s.origin(r) + "/auth/" + provider + "/callback"
}

// safeNext keeps the "where was I going" parameter from becoming an open
// redirect. Only a path on this host survives: anything scheme-relative
// ("//evil.example"), absolute, or backslashed becomes the dashboard root.
func safeNext(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") {
		return "/"
	}
	if strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "/"
	}
	// /login is a legal path and a useless destination: signing in to be sent
	// back to the login page reads as a failure.
	if value == "/login" || strings.HasPrefix(value, "/login?") || strings.HasPrefix(value, "/auth/") {
		return "/"
	}
	return value
}
