package auth

// Apple, which is the awkward one, in three ways that all have to be handled
// rather than worked around:
//
//  1. There is no client secret. What Apple calls one is a JWT that guard signs
//     itself with a P-256 key downloaded from the developer portal, valid for
//     at most six months — so it is minted per exchange rather than stored.
//  2. The callback is a cross-site form POST, not a redirect with a query, as
//     soon as any scope is requested. That is why the sign-in state lives in
//     SQLite: a SameSite=Lax cookie is not sent on a cross-site POST, and the
//     alternative is a session cookie that is sent everywhere.
//  3. The name is sent exactly once, in a `user` form field on the very first
//     authorization, and never again — not even in the id token. Miss it and
//     the only way to see it again is for the person to remove the app from
//     their Apple ID and sign in afresh.
//
// Portal setup: the client id is a *Services ID* (not the App ID), its Return
// URL is the callback below exactly, and the key is a Sign in with Apple key
// whose .p8 contents go in GUARD_APPLE_PRIVATE_KEY (or the file named by
// GUARD_APPLE_PRIVATE_KEY_FILE).

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Apple is what the developer portal hands out: a Services ID, the team it
// belongs to, and one signing key.
type Apple struct {
	ClientID   string // the Services ID, e.g. app.hushkey.guard.signin
	TeamID     string
	KeyID      string
	PrivateKey string // the contents of the .p8 file
}

func (a Apple) configured() bool {
	return strings.TrimSpace(a.ClientID) != "" && strings.TrimSpace(a.TeamID) != "" &&
		strings.TrimSpace(a.KeyID) != "" && strings.TrimSpace(a.PrivateKey) != ""
}

var appleEndpoints = endpoints{
	authorize: "https://appleid.apple.com/auth/authorize",
	token:     "https://appleid.apple.com/auth/token",
	keys:      "https://appleid.apple.com/auth/keys",
	issuers:   []string{"https://appleid.apple.com"},
}

func newApple(cfg Apple, where endpoints, client *http.Client, now func() time.Time) (Provider, error) {
	clientID := strings.TrimSpace(cfg.ClientID)
	team := strings.TrimSpace(cfg.TeamID)
	keyID := strings.TrimSpace(cfg.KeyID)
	if clientID == "" || team == "" || keyID == "" {
		return nil, errors.New("apple needs a services id, a team id and a key id")
	}
	key, err := parseP8(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &oidc{
		id:       model.ProviderApple,
		label:    "Continue with Apple",
		clientID: clientID,
		secret: func() (string, error) {
			return appleSecret(key, team, keyID, clientID, now())
		},
		scope: "name email",
		// form_post is not optional: Apple rejects the request outright if a
		// scope is asked for and the response mode is not this.
		extra:     url.Values{"response_mode": {"form_post"}},
		endpoints: where,
		client:    client,
		keys:      &keySet{url: where.keys, client: client},
		now:       now,
	}, nil
}

// parseP8 reads the key Apple hands out. It arrives as PKCS#8 PEM, and people
// paste it into an environment variable with the newlines turned into \n about
// half the time, so both spellings are accepted.
func parseP8(material string) (*ecdsa.PrivateKey, error) {
	material = strings.TrimSpace(strings.ReplaceAll(material, `\n`, "\n"))
	if material == "" {
		return nil, errors.New("apple needs a signing key")
	}
	block, _ := pem.Decode([]byte(material))
	if block == nil {
		return nil, errors.New("apple's signing key is not PEM — paste the whole .p8 file, BEGIN line included")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apple's signing key could not be read: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apple's signing key is not an EC key")
	}
	return key, nil
}

// appleSecret mints the client secret JWT for one exchange.
//
// Thirty minutes, not the six months Apple allows. A secret that lives no
// longer than the request it was made for cannot be replayed if it leaks in a
// log, and nothing here needs it to survive.
func appleSecret(key *ecdsa.PrivateKey, team, keyID, clientID string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"iss": team,
		"iat": now.Unix(),
		"exp": now.Add(30 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com",
		"sub": clientID,
	})
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	// JWS wants r and s as two fixed 32-byte halves. Left-padded, because a
	// big.Int drops leading zero bytes and one signature in 256 is short.
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// appleUser is the `user` field Apple posts alongside the code, on the first
// authorization only.
type appleUser struct {
	Name struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	} `json:"name"`
	Email string `json:"email"`
}

// nameFromApple reads that field. A malformed one is not an error worth failing
// a sign-in over — the name is decoration, and the id token is the identity.
func nameFromApple(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var user appleUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(user.Name.FirstName) + " " + strings.TrimSpace(user.Name.LastName))
}
