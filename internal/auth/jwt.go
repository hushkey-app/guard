package auth

// The identity token, and the keys it is checked against.
//
// Guard verifies the id token itself rather than pulling in a JWT library, and
// that is a deliberately small amount of code: two algorithms (RS256 for
// Google, ES256 for Apple), a key set fetched from the provider and cached, and
// a claim check that refuses everything it was not told to expect. There is no
// "none" algorithm here, no algorithm read from the token and looked up in a
// table, and no unverified parse — the three ways this is normally got wrong.
//
// The token is the whole login. Everything else in the flow — the state, the
// nonce, the single-use row in SQLite — only protects the delivery of it.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// leeway forgives a small clock difference between guard and the provider.
// Sixty seconds is what everyone uses; a login that fails because a laptop's
// clock drifted a second is a bug report nobody can reproduce.
const leeway = 60 * time.Second

// claims is the part of an id token guard reads. Everything else a provider
// sends is ignored on purpose: a claim that is not read cannot be trusted by
// accident.
type claims struct {
	Issuer        string   `json:"iss"`
	Subject       string   `json:"sub"`
	Audience      audience `json:"aud"`
	Expiry        int64    `json:"exp"`
	IssuedAt      int64    `json:"iat"`
	Nonce         string   `json:"nonce"`
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	Name          string   `json:"name"`
	Picture       string   `json:"picture"`
}

// audience is one string or a list of them. The specification allows both and
// Google has sent both over the years.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) has(want string) bool {
	for _, value := range a {
		if value == want {
			return true
		}
	}
	return false
}

// flexBool is a bool that may arrive as the string "true". Apple sends
// email_verified that way, and a strict decode would fail the whole login over
// a pair of quotes.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var native bool
	if err := json.Unmarshal(data, &native); err == nil {
		*b = flexBool(native)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	*b = flexBool(text == "true")
	return nil
}

// keySet is a provider's published signing keys, cached.
//
// Providers rotate keys without warning, so an unknown key id is not an error:
// it is a reason to fetch again, at most once a minute. That rate limit matters
// — without it, a token signed with a key that will never exist turns every
// failed login into a request to the provider.
type keySet struct {
	url    string
	client *http.Client

	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
}

const keyRefresh = time.Minute

func (k *keySet) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	k.mu.Lock()
	key, ok := k.keys[kid]
	stale := time.Since(k.fetched) > keyRefresh
	k.mu.Unlock()
	if ok {
		return key, nil
	}
	if !stale {
		return nil, fmt.Errorf("no signing key %q", kid)
	}
	fetched, err := fetchKeys(ctx, k.client, k.url)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.keys, k.fetched = fetched, time.Now()
	key, ok = k.keys[kid]
	k.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no signing key %q", kid)
	}
	return key, nil
}

// jwk is one key from a JWKS document, in the two shapes guard accepts.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func fetchKeys(ctx context.Context, client *http.Client, url string) (map[string]crypto.PublicKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch signing keys: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch signing keys: %s", response.Status)
	}
	var document struct {
		Keys []jwk `json:"keys"`
	}
	// 1 MiB is several times the largest key set any provider publishes, and
	// the difference between a bad answer and an out-of-memory.
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&document); err != nil {
		return nil, fmt.Errorf("read signing keys: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		public, err := key.public()
		if err != nil || key.Kid == "" {
			continue
		}
		keys[key.Kid] = public
	}
	if len(keys) == 0 {
		return nil, errors.New("the provider published no usable signing keys")
	}
	return keys, nil
}

func (k jwk) public() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		exponent := new(big.Int).SetBytes(e)
		if !exponent.IsInt64() || exponent.Int64() <= 0 {
			return nil, errors.New("unusable rsa exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, err
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, err
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}, nil
	default:
		return nil, fmt.Errorf("unsupported key type %q", k.Kty)
	}
}

// verify checks an id token's signature against the key set and returns its
// claims. It does not check them — that is expect's job, one layer up, because
// what a claim has to say differs per provider and this part does not.
func (k *keySet) verify(ctx context.Context, token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("the identity token is not a JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims{}, errors.New("the identity token's header is not readable")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return claims{}, errors.New("the identity token's header is not readable")
	}
	// The algorithm is checked against the two guard implements before the key
	// is even looked up. "none", and an HMAC algorithm verified with a public
	// key as its secret, are both forgeries this line refuses.
	if header.Alg != "RS256" && header.Alg != "ES256" {
		return claims{}, fmt.Errorf("unsupported signing algorithm %q", header.Alg)
	}
	key, err := k.key(ctx, header.Kid)
	if err != nil {
		return claims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims{}, errors.New("the identity token's signature is not readable")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch header.Alg {
	case "RS256":
		public, ok := key.(*rsa.PublicKey)
		if !ok {
			return claims{}, errors.New("the signing key is not an RSA key")
		}
		if err := rsa.VerifyPKCS1v15(public, crypto.SHA256, digest[:], signature); err != nil {
			return claims{}, errors.New("the identity token's signature does not check out")
		}
	case "ES256":
		public, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return claims{}, errors.New("the signing key is not an EC key")
		}
		// JWS carries r and s as two fixed-width halves, not as the ASN.1
		// structure crypto/ecdsa's other verifier expects.
		if len(signature) != 64 {
			return claims{}, errors.New("the identity token's signature is the wrong length")
		}
		r := new(big.Int).SetBytes(signature[:32])
		s := new(big.Int).SetBytes(signature[32:])
		if !ecdsa.Verify(public, digest[:], r, s) {
			return claims{}, errors.New("the identity token's signature does not check out")
		}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errors.New("the identity token's payload is not readable")
	}
	var out claims
	if err := json.Unmarshal(payload, &out); err != nil {
		return claims{}, errors.New("the identity token's payload is not readable")
	}
	return out, nil
}

// expect is the claim check: the token has to come from the issuer guard asked,
// be addressed to this client, still be valid, and carry the nonce this
// particular sign-in generated.
//
// The nonce is the one that catches the interesting attack. Without it a token
// minted for another session of the same application is a valid login here.
func (c claims) expect(issuers []string, audience, nonce string, now time.Time) error {
	matched := false
	for _, issuer := range issuers {
		if c.Issuer == issuer {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("the identity token came from %q", c.Issuer)
	}
	if !c.Audience.has(audience) {
		return errors.New("the identity token was issued for a different client")
	}
	if c.Subject == "" {
		return errors.New("the identity token names nobody")
	}
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0).Add(leeway)) {
		return errors.New("the identity token has expired")
	}
	if c.IssuedAt != 0 && now.Add(leeway).Before(time.Unix(c.IssuedAt, 0)) {
		return errors.New("the identity token was issued in the future")
	}
	if nonce != "" && c.Nonce != nonce {
		return errors.New("the identity token belongs to a different sign-in")
	}
	return nil
}
