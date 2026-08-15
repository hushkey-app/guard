package auth

// Google, which is the easy one: a client id, a client secret that is a string
// somebody pasted, and endpoints that have not moved in a decade.
//
// Console setup, because it is the part that goes wrong: the credential is an
// *OAuth client ID* of type "Web application", and its Authorised redirect URI
// must be exactly the callback below — scheme, host, port and path, no trailing
// slash. Google compares it as a string.

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Google is the credential pair from the Google Cloud console.
type Google struct {
	ClientID     string
	ClientSecret string
}

func (g Google) configured() bool {
	return strings.TrimSpace(g.ClientID) != "" && strings.TrimSpace(g.ClientSecret) != ""
}

var googleEndpoints = endpoints{
	authorize: "https://accounts.google.com/o/oauth2/v2/auth",
	token:     "https://oauth2.googleapis.com/token",
	keys:      "https://www.googleapis.com/oauth2/v3/certs",
	// Both spellings are current: which one a token carries depends on how old
	// the client registration is, and neither is a downgrade.
	issuers: []string{"https://accounts.google.com", "accounts.google.com"},
}

func newGoogle(cfg Google, where endpoints, client *http.Client, now func() time.Time) (Provider, error) {
	id := strings.TrimSpace(cfg.ClientID)
	secret := strings.TrimSpace(cfg.ClientSecret)
	if id == "" || secret == "" {
		return nil, errors.New("google needs a client id and a client secret")
	}
	return &oidc{
		id:       model.ProviderGoogle,
		label:    "Continue with Google",
		clientID: id,
		secret:   func() (string, error) { return secret, nil },
		scope:    "openid email profile",
		// select_account, because a dashboard is exactly the kind of thing
		// somebody opens from a browser already signed in to the wrong Google
		// account, and silently landing in it is worse than one extra click.
		extra:     url.Values{"prompt": {"select_account"}},
		endpoints: where,
		client:    client,
		keys:      &keySet{url: where.keys, client: client},
		now:       now,
	}, nil
}
