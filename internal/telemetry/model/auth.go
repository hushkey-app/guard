package model

// Sign-in: who is looking at the dashboard, when guard has been told to ask.
//
// These types live here for the same reason every other guard type does — the
// login page renders them, and the page tree compiles for wasm, so it cannot
// reach a package that opens SQLite or dials an identity provider. The OAuth
// machinery is in internal/auth; what a page can see of it is this file.

import (
	"errors"
	"net/mail"
	"strings"
	"time"
)

// The provider ids. Two, both spelled the same way in the environment
// variables, the URLs and the database, because a third spelling is how a
// callback ends up unroutable.
const (
	ProviderGoogle = "google"
	ProviderApple  = "apple"
)

// A LoginProvider is one button on the login box. It exists because the box is
// drawn from what is configured rather than from a hardcoded pair: an instance
// with only Google credentials must not offer an Apple button that can only
// fail.
type LoginProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// LoginView is everything the login page renders. The middleware builds it —
// the page is a template and knows nothing about environment variables.
//
// Empty Providers means sign-in is off, which the page still has to be able to
// say: somebody who bookmarked /login after the credentials were removed
// deserves a sentence rather than an empty card.
type LoginView struct {
	Providers []LoginProvider `json:"providers"`
	// Error is a short sentence for a sign-in that did not complete. It is a
	// message chosen from a fixed set by internal/auth, never text from the
	// provider or the query string — an error page that renders whatever
	// ?error= says is a phishing surface.
	Error string `json:"error,omitempty"`
	// Next is the path the visitor was trying to reach, carried through the
	// round trip so a bookmarked deep link survives signing in.
	Next string `json:"next,omitempty"`
}

// A Viewer is who the current request is. The zero value is "nobody", which is
// both a visitor on an open instance and a visitor who has not signed in yet —
// the page never needs to tell those apart, because the middleware has already
// decided whether the request may proceed.
type Viewer struct {
	Provider string `json:"provider,omitempty"`
	// Subject is the provider's own id for the person. It, not the email, is
	// the identity: an email address can be changed at the provider and
	// reassigned to somebody else, and a subject cannot.
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Name    string `json:"name,omitempty"`
	Picture string `json:"picture,omitempty"`
}

// SignedIn reports whether this request carries a session.
func (v Viewer) SignedIn() bool { return v.Subject != "" }

// Display is what the sidebar shows: the name if the provider gave one, the
// email otherwise. Apple hands over a name exactly once — on the very first
// authorization — so for most Apple sign-ins the email is all there is.
func (v Viewer) Display() string {
	if name := strings.TrimSpace(v.Name); name != "" {
		return name
	}
	if email := strings.TrimSpace(v.Email); email != "" {
		return email
	}
	return "Signed in"
}

// Initial is the one letter drawn in the avatar circle when there is no
// picture.
func (v Viewer) Initial() string {
	for _, r := range v.Display() {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// The two roles. Everything a member can do, an admin can do; an admin can
// also change who the members are, which is the only privilege guard has ever
// needed to draw a line around.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// A Member is somebody allowed to sign in.
//
// The list is the allowlist — there is no other one. An OAuth provider will
// happily prove that a complete stranger owns a Google account, which is not
// the question a dashboard is asking; the question is whether that address is
// on this list, and it is answered here rather than by the provider.
type Member struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	// Name and Provider are what the last sign-in reported, kept so the page
	// can show a person rather than a row. Both are empty until they sign in
	// for the first time, which is also how "invited, never arrived" is
	// visible without a column for it.
	Name     string    `json:"name,omitempty"`
	Provider string    `json:"provider,omitempty"`
	AddedBy  string    `json:"added_by,omitempty"`
	AddedAt  time.Time `json:"added_at"`
	// LastSeen is nil for a member who has never signed in.
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// Fixed marks the members that come from GUARD_ADMIN_EMAIL rather than
	// from the table: always allowed, always admin, and not removable from the
	// page. That is the lock on the door — an admin who removes every other
	// admin, including themselves, still cannot lock the owner out, and an
	// empty database still has somebody who can sign in.
	Fixed bool `json:"fixed,omitempty"`
}

func (m Member) IsAdmin() bool { return m.Role == RoleAdmin }

// Validate checks the address and the role. The address is parsed rather than
// pattern-matched: a member row that no provider will ever match is a silent
// "why can't I sign in".
func (m Member) Validate() error {
	if NormalizeEmail(m.Email) == "" {
		return errors.New("a member needs an email address")
	}
	address, err := mail.ParseAddress(NormalizeEmail(m.Email))
	if err != nil || address.Address != NormalizeEmail(m.Email) {
		return errors.New("that does not look like an email address")
	}
	if !strings.Contains(strings.SplitN(address.Address, "@", 2)[1], ".") {
		return errors.New("that does not look like an email address")
	}
	switch m.Role {
	case RoleAdmin, RoleMember, "":
	default:
		return errors.New("a member is either an admin or a member")
	}
	return nil
}

// NormalizeEmail is how an address is compared everywhere in guard: trimmed and
// lowercased. Addresses are technically case-sensitive to the left of the @ and
// no provider in the world treats them that way — Google and Apple both hand
// back lowercase — so matching case-sensitively would only ever produce a
// member who cannot sign in because of how they typed their own address.
func NormalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// A Session is one signed-in browser.
//
// Hash is a SHA-256 of the cookie value, never the value itself. The database
// file travels — it is copied to laptops, backed up, attached to bug reports —
// and a table of live session cookies in it would be a table of logins. Hashed,
// a stolen copy proves who signed in and grants nothing.
type Session struct {
	Hash      []byte
	Provider  string
	Subject   string
	Email     string
	Name      string
	Picture   string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Viewer is what a session looks like to a page.
func (s Session) Viewer() Viewer {
	return Viewer{Provider: s.Provider, Subject: s.Subject, Email: s.Email, Name: s.Name, Picture: s.Picture}
}

// A LoginState is one sign-in in flight: issued when the browser is sent to the
// provider, claimed once when it comes back, and expired if it never does.
//
// It is stored server-side rather than in a cookie, and that is not an
// implementation detail. Apple returns its callback as a cross-site form POST,
// and a Lax cookie — the only kind worth setting for a session — is not sent on
// one. A cookie that has to be SameSite=None to work is a cookie that is sent
// everywhere; a row in SQLite that is deleted the moment it is used is not.
type LoginState struct {
	State    string
	Provider string
	// Nonce is bound into the provider's id token and compared when it comes
	// back, which is what makes a stolen token from another session useless
	// here.
	Nonce string
	// Redirect is the exact redirect_uri sent to the provider. The token
	// exchange has to repeat it byte for byte, so it is remembered rather than
	// derived a second time from a request that may have arrived through a
	// different proxy.
	Redirect  string
	Next      string
	ExpiresAt time.Time
}
