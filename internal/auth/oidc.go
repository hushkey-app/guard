package auth

// One OpenID Connect provider, and the two things guard asks of it: build the
// URL a browser is sent to, and turn the code that comes back into a verified
// identity.
//
// Google and Apple differ in five values and one signature — the endpoints, the
// scope, the response mode, and whether the client secret is a stored string or
// a JWT that has to be minted per request. Everything else is the same flow, so
// it is written once here and configured twice, in google.go and apple.go.
//
// Only the authorization-code flow, and only the id token. Guard asks for no
// refresh token and keeps no access token: it wants to know who you are once,
// at sign-in, and then its own session cookie is the login. Nothing here can be
// used to read anything from the provider afterwards, which is the point.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// An Identity is a person, as the provider just described them.
type Identity struct {
	Provider string
	Subject  string
	Email    string
	Name     string
	Picture  string
	Verified bool
}

// A Provider is one sign-in button and the flow behind it.
type Provider interface {
	ID() string
	Label() string
	// Authorize is the URL the browser is sent to. The redirect is passed in
	// rather than stored because guard may be reached at more than one origin,
	// and the exchange has to repeat whichever one was used.
	Authorize(redirect, state, nonce string) string
	// Exchange turns the code into an identity, or an error. It verifies the
	// id token's signature, issuer, audience, expiry and nonce; a nil error
	// means all five held.
	Exchange(ctx context.Context, code, redirect, nonce string) (Identity, error)
}

type endpoints struct {
	authorize string
	token     string
	keys      string
	// issuers is a list because Google's tokens say "accounts.google.com" or
	// "https://accounts.google.com" depending on the era of the client.
	issuers []string
}

type oidc struct {
	id    string
	label string

	clientID string
	// secret is called per exchange. Google's returns a stored string; Apple's
	// signs a fresh JWT, because Apple's "client secret" expires.
	secret func() (string, error)
	scope  string
	// extra is whatever the provider needs on the authorization URL that the
	// others do not — Apple's response_mode, Google's prompt.
	extra url.Values

	endpoints endpoints
	client    *http.Client
	keys      *keySet
	now       func() time.Time
}

func (o *oidc) ID() string    { return o.id }
func (o *oidc) Label() string { return o.label }

func (o *oidc) Authorize(redirect, state, nonce string) string {
	query := url.Values{
		"client_id":     {o.clientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {o.scope},
		"state":         {state},
		"nonce":         {nonce},
	}
	for key, values := range o.extra {
		query[key] = values
	}
	return o.endpoints.authorize + "?" + query.Encode()
}

func (o *oidc) Exchange(ctx context.Context, code, redirect, nonce string) (Identity, error) {
	secret, err := o.secret()
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {o.clientID},
		"client_secret": {secret},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoints.token,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := o.client.Do(request)
	if err != nil {
		return Identity{}, fmt.Errorf("%s did not answer: %w", o.label, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Identity{}, fmt.Errorf("read %s's answer: %w", o.label, err)
	}
	var token struct {
		IDToken     string `json:"id_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	// A provider that refuses says so in JSON with a 400; one that is broken
	// says so in HTML with a 500. Both are reported by status, so the second
	// does not surface as "unexpected character '<'".
	if err := json.Unmarshal(body, &token); err != nil {
		return Identity{}, fmt.Errorf("%s answered %s with something that is not JSON", o.label, response.Status)
	}
	if token.Error != "" {
		return Identity{}, fmt.Errorf("%s refused the code: %s %s", o.label, token.Error, token.Description)
	}
	if response.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%s answered %s", o.label, response.Status)
	}
	if token.IDToken == "" {
		return Identity{}, fmt.Errorf("%s returned no identity token", o.label)
	}
	claims, err := o.keys.verify(ctx, token.IDToken)
	if err != nil {
		return Identity{}, err
	}
	if err := claims.expect(o.endpoints.issuers, o.clientID, nonce, o.now()); err != nil {
		return Identity{}, err
	}
	return Identity{
		Provider: o.id,
		Subject:  claims.Subject,
		Email:    strings.TrimSpace(strings.ToLower(claims.Email)),
		Name:     strings.TrimSpace(claims.Name),
		Picture:  claims.Picture,
		Verified: bool(claims.EmailVerified),
	}, nil
}
