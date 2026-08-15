// Package auth is guard's front door: sign in with Google or with Apple, and
// nothing else.
//
// The shape of it is three rules, and they are worth stating before any code.
//
//  1. **Configured is on, unconfigured is off.** With no OAuth credentials in
//     the environment guard behaves exactly as it always has — open, or behind
//     GUARD_TOKEN — because the tool most people run is a container on a
//     laptop and a login screen it cannot get past would be a downgrade. Give
//     it Google credentials and the Google button appears; give it Apple's and
//     Apple's does; give it both and there are two. Nothing is declared twice:
//     the login page is drawn from the providers that could actually be built.
//
//  2. **The provider says who you are; the members list says whether you may
//     come in.** An OAuth provider will happily prove that a complete stranger
//     owns a Google account, so proving identity is not authorization. The
//     allowlist is internal/telemetry's auth_members table plus whatever
//     GUARD_ADMIN_EMAIL names — and the environment variable is the one that
//     cannot be deleted from the dashboard, which is what makes the list safe
//     to edit from the dashboard.
//
//  3. **The session is guard's, not the provider's.** Guard asks for an
//     identity token once, verifies it, and issues its own cookie. It keeps no
//     access token and asks for no refresh token, so nothing stored here can
//     read anything from Google or Apple afterwards. The database holds a
//     SHA-256 of each cookie, never the cookie.
//
// The OTLP endpoints are deliberately outside all of this. An exporter posting
// telemetry holds a bearer token, not a browser session, and putting a login
// screen in front of /v1/logs would break every collector pointed at guard.
package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Store is the part of the telemetry store this package needs. Narrow on
// purpose: sign-in reads and writes five tables' worth of nothing, and an
// interface this size is a test double that fits on a screen.
type Store interface {
	StartLogin(model.LoginState) error
	ClaimLogin(state string) (model.LoginState, error)
	CreateSession(model.Session) error
	Session(hash []byte) (model.Session, error)
	DeleteSession(hash []byte) error
	PurgeSessions() (int64, error)
	Member(email string) (model.Member, error)
	MarkMemberSeen(email, provider, name string) error
}

// Config is everything the environment says about signing in.
type Config struct {
	Google Google
	Apple  Apple
	// Admins are the addresses from GUARD_ADMIN_EMAIL: always allowed, always
	// admin, never removable from the page. This is the way back in.
	Admins []string
	// BaseURL is the public origin guard is reached at, e.g.
	// https://guard.example.com. Behind a proxy it should be set: the redirect
	// URI has to match what is registered with the provider byte for byte, and
	// deriving it from the request means trusting a header for it.
	BaseURL string
	// SessionTTL is how long a sign-in lasts. Seven days by default: long
	// enough that a dashboard is not a login screen, short enough that a
	// laptop left in a taxi stops working within the week.
	SessionTTL time.Duration
	// APIToken is GUARD_TOKEN. A caller presenting it is a machine, and skips
	// the browser flow entirely — that is how the exporters, the seed tool and
	// anything scripted keep working once sign-in is on.
	APIToken string

	// Client is the HTTP client used to reach the providers. Tests point it at
	// an httptest server; nothing else sets it.
	Client *http.Client
	// endpoints overrides where a provider lives, by provider id. Tests only —
	// it is unexported for exactly that reason.
	endpoints map[string]endpoints
	// now is the clock, for tests that need an expired token.
	now func() time.Time
}

const defaultSessionTTL = 7 * 24 * time.Hour

// FromEnv reads the configuration. Every variable is optional; what is present
// decides what exists.
//
//	GUARD_GOOGLE_CLIENT_ID, GUARD_GOOGLE_CLIENT_SECRET
//	GUARD_APPLE_CLIENT_ID, GUARD_APPLE_TEAM_ID, GUARD_APPLE_KEY_ID,
//	GUARD_APPLE_PRIVATE_KEY or GUARD_APPLE_PRIVATE_KEY_FILE
//	GUARD_ADMIN_EMAIL           one address, or several separated by commas
//	GUARD_AUTH_BASE_URL         https://guard.example.com
//	GUARD_AUTH_SESSION_TTL      a Go duration, e.g. 168h
func FromEnv() Config {
	cfg := Config{
		Google: Google{
			ClientID:     os.Getenv("GUARD_GOOGLE_CLIENT_ID"),
			ClientSecret: os.Getenv("GUARD_GOOGLE_CLIENT_SECRET"),
		},
		Apple: Apple{
			ClientID:   os.Getenv("GUARD_APPLE_CLIENT_ID"),
			TeamID:     os.Getenv("GUARD_APPLE_TEAM_ID"),
			KeyID:      os.Getenv("GUARD_APPLE_KEY_ID"),
			PrivateKey: os.Getenv("GUARD_APPLE_PRIVATE_KEY"),
		},
		Admins:     splitList(os.Getenv("GUARD_ADMIN_EMAIL")),
		BaseURL:    strings.TrimRight(strings.TrimSpace(os.Getenv("GUARD_AUTH_BASE_URL")), "/"),
		SessionTTL: defaultSessionTTL,
		APIToken:   os.Getenv("GUARD_TOKEN"),
	}
	// The .p8 is a multi-line file, and a multi-line environment variable is a
	// deployment argument nobody wins. Naming the file is the other way.
	if path := strings.TrimSpace(os.Getenv("GUARD_APPLE_PRIVATE_KEY_FILE")); path != "" && cfg.Apple.PrivateKey == "" {
		key, err := os.ReadFile(path)
		if err != nil {
			slog.Error("apple signing key could not be read", slog.String("path", path), slog.Any("err", err))
		} else {
			cfg.Apple.PrivateKey = string(key)
		}
	}
	if ttl, err := time.ParseDuration(os.Getenv("GUARD_AUTH_SESSION_TTL")); err == nil && ttl > 0 {
		cfg.SessionTTL = ttl
	}
	return cfg
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = model.NormalizeEmail(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Service is the running thing: the providers that were configured, the store
// behind them, and the middleware everything else in guard passes through.
type Service struct {
	store     Store
	cfg       Config
	providers []Provider
	admins    map[string]bool
	now       func() time.Time
}

// New builds the service. It returns an error only for a configuration that
// cannot be honoured — a provider half-filled in, or an Apple key that is not a
// key. Absent credentials are not an error; they are the off switch.
//
// Half-filled is worth failing on. A GUARD_GOOGLE_CLIENT_ID with no secret
// beside it is somebody who meant to turn sign-in on, and silently starting
// without it is how an instance ends up open on the internet believing it is
// not.
func New(store Store, cfg Config) (*Service, error) {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = defaultSessionTTL
	}
	service := &Service{store: store, cfg: cfg, admins: map[string]bool{}, now: cfg.now}
	for _, email := range cfg.Admins {
		service.admins[model.NormalizeEmail(email)] = true
	}

	if partial(cfg.Google.ClientID, cfg.Google.ClientSecret) {
		return nil, errors.New("google sign-in needs both GUARD_GOOGLE_CLIENT_ID and GUARD_GOOGLE_CLIENT_SECRET")
	}
	if cfg.Google.configured() {
		google, err := newGoogle(cfg.Google, cfg.where(model.ProviderGoogle, googleEndpoints), cfg.Client, service.now)
		if err != nil {
			return nil, err
		}
		service.providers = append(service.providers, google)
	}

	if partial(cfg.Apple.ClientID, cfg.Apple.TeamID, cfg.Apple.KeyID, cfg.Apple.PrivateKey) {
		return nil, errors.New("apple sign-in needs GUARD_APPLE_CLIENT_ID, GUARD_APPLE_TEAM_ID, GUARD_APPLE_KEY_ID and a signing key")
	}
	if cfg.Apple.configured() {
		apple, err := newApple(cfg.Apple, cfg.where(model.ProviderApple, appleEndpoints), cfg.Client, service.now)
		if err != nil {
			return nil, err
		}
		service.providers = append(service.providers, apple)
	}

	if service.Enabled() && len(service.admins) == 0 {
		// Not fatal, because the members table may already have somebody in it
		// from a previous run — but loud, because the other case is an
		// instance nobody can sign in to.
		slog.Warn("sign-in is on with no GUARD_ADMIN_EMAIL — only addresses already in the members list can get in",
			slog.Any("providers", service.IDs()))
	}
	return service, nil
}

// partial reports a group of values where some are set and some are not.
func partial(values ...string) bool {
	set, blank := 0, 0
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			blank++
		} else {
			set++
		}
	}
	return set > 0 && blank > 0
}

func (c Config) where(id string, fallback endpoints) endpoints {
	if override, ok := c.endpoints[id]; ok {
		return override
	}
	return fallback
}

// Enabled reports whether anybody has to sign in. False is guard's historical
// behaviour, unchanged.
func (s *Service) Enabled() bool { return s != nil && len(s.providers) > 0 }

// IDs names the configured providers, for logging.
func (s *Service) IDs() []string {
	out := make([]string, 0, len(s.providers))
	for _, provider := range s.providers {
		out = append(out, provider.ID())
	}
	return out
}

// Buttons is what the login page draws.
func (s *Service) Buttons() []model.LoginProvider {
	if s == nil {
		return nil
	}
	out := make([]model.LoginProvider, 0, len(s.providers))
	for _, provider := range s.providers {
		out = append(out, model.LoginProvider{ID: provider.ID(), Label: provider.Label()})
	}
	return out
}

func (s *Service) provider(id string) (Provider, bool) {
	for _, provider := range s.providers {
		if provider.ID() == id {
			return provider, true
		}
	}
	return nil, false
}

// member answers the only question that matters after a provider has spoken:
// is this address allowed in, and as what.
//
// GUARD_ADMIN_EMAIL is checked first and does not consult the table, so an
// owner can always sign in — including into a database whose members list is
// empty, which is every new instance.
func (s *Service) member(email string) (model.Member, bool) {
	email = model.NormalizeEmail(email)
	if email == "" {
		return model.Member{}, false
	}
	if s.admins[email] {
		return model.Member{Email: email, Role: model.RoleAdmin, Fixed: true}, true
	}
	member, err := s.store.Member(email)
	if err != nil {
		return model.Member{}, false
	}
	if member.Role == "" {
		member.Role = model.RoleMember
	}
	return member, true
}

// Fixed reports whether an address comes from the environment, which the
// members page uses to draw a row it must not offer to delete.
func (s *Service) Fixed(email string) bool { return s != nil && s.admins[model.NormalizeEmail(email)] }

// Admins is GUARD_ADMIN_EMAIL as member rows, so the page can list the people
// who are allowed in without a table row and say why they cannot be removed.
func (s *Service) Admins() []model.Member {
	if s == nil {
		return nil
	}
	out := make([]model.Member, 0, len(s.admins))
	for _, email := range s.cfg.Admins {
		email = model.NormalizeEmail(email)
		if email == "" {
			continue
		}
		out = append(out, model.Member{Email: email, Role: model.RoleAdmin, Fixed: true})
	}
	return out
}

// Startup logs what a person needs to know from the first ten lines of the log:
// whether the dashboard is behind a login, and who can get past it.
func (s *Service) Startup() {
	if !s.Enabled() {
		return
	}
	slog.Info("sign-in is on",
		slog.Any("providers", s.IDs()),
		slog.Int("admins", len(s.admins)),
		slog.String("session_ttl", s.cfg.SessionTTL.String()))
	if removed, err := s.store.PurgeSessions(); err == nil && removed > 0 {
		slog.Info("expired sessions removed", slog.Int64("sessions", removed))
	}
}

// String is for the startup line and for tests that want to say what got built.
func (s *Service) String() string {
	if !s.Enabled() {
		return "sign-in off"
	}
	return fmt.Sprintf("sign-in via %s", strings.Join(s.IDs(), " and "))
}
