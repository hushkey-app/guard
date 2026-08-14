package auth

// The sign-in tests run the whole flow against a mock identity provider: a
// httptest server that publishes a JWKS, mints id tokens signed with a key it
// generated for the test, and checks the client credentials guard sends it.
//
// Mocked at the provider rather than at guard's own seams, on purpose. What is
// worth testing here is not that a function was called — it is that the token
// exchange sends what Google and Apple require, that a token guard did not ask
// for is refused, and that a browser ends up with a cookie that works. All of
// that lives in the wire format, so the wire format is what the double speaks.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// ---------------------------------------------------------------------------
// The mock provider
// ---------------------------------------------------------------------------

type idp struct {
	t        *testing.T
	alg      string // RS256 like Google, ES256 like Apple
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	issuer   string
	clientID string
	server   *httptest.Server

	// What the next id token says. Each is a knob a test turns to describe a
	// token guard should refuse.
	subject       string
	email         string
	emailVerified any
	name          string
	nonce         string // when set, used instead of the nonce guard asked for
	audience      string // when set, used instead of the client id
	expiresIn     time.Duration

	// What the last exchange sent, so a test can assert on it.
	lastForm   url.Values
	lastSecret string
}

func newIDP(t *testing.T, alg, clientID string) *idp {
	t.Helper()
	provider := &idp{
		t: t, alg: alg, clientID: clientID, issuer: "https://idp.test",
		subject: "subject-1", email: "ana@example.com", emailVerified: true,
		name: "Ana Ruiz", expiresIn: time.Hour,
	}
	var err error
	switch alg {
	case "RS256":
		provider.rsaKey, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ES256":
		provider.ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", provider.keys)
	mux.HandleFunc("/token", provider.token)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	provider.server = httptest.NewServer(mux)
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *idp) endpoints() endpoints {
	return endpoints{
		authorize: p.server.URL + "/authorize",
		token:     p.server.URL + "/token",
		keys:      p.server.URL + "/keys",
		issuers:   []string{p.issuer},
	}
}

func (p *idp) keys(w http.ResponseWriter, r *http.Request) {
	key := map[string]string{"kid": "test-key", "use": "sig", "alg": p.alg}
	if p.rsaKey != nil {
		key["kty"] = "RSA"
		key["n"] = base64.RawURLEncoding.EncodeToString(p.rsaKey.N.Bytes())
		key["e"] = base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.rsaKey.E)).Bytes())
	} else {
		key["kty"] = "EC"
		key["crv"] = "P-256"
		key["x"] = base64.RawURLEncoding.EncodeToString(p.ecKey.X.Bytes())
		key["y"] = base64.RawURLEncoding.EncodeToString(p.ecKey.Y.Bytes())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{key}})
}

func (p *idp) token(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	p.lastForm = form
	p.lastSecret = form.Get("client_secret")
	if form.Get("grant_type") != "authorization_code" || form.Get("code") == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
		return
	}
	audience := p.audience
	if audience == "" {
		audience = p.clientID
	}
	claims := map[string]any{
		"iss": p.issuer,
		"sub": p.subject,
		"aud": audience,
		"exp": time.Now().Add(p.expiresIn).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
	if p.nonce != "" {
		claims["nonce"] = p.nonce
	} else {
		claims["nonce"] = nonceFor(p.t, form.Get("code"))
	}
	if p.email != "" {
		claims["email"] = p.email
		claims["email_verified"] = p.emailVerified
	}
	if p.name != "" {
		claims["name"] = p.name
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id_token": p.sign(claims)})
}

// nonceFor is the trick that lets the mock answer with the right nonce without
// guard having to tell it: the test encodes the nonce into the code it hands
// back, exactly the way a real provider remembers it from the authorize step.
func nonceFor(t *testing.T, code string) string {
	t.Helper()
	_, nonce, _ := strings.Cut(code, ":")
	return nonce
}

func (p *idp) sign(claims map[string]any) string {
	header, err := json.Marshal(map[string]string{"alg": p.alg, "kid": "test-key", "typ": "JWT"})
	if err != nil {
		p.t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		p.t.Fatal(err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	var signature []byte
	if p.rsaKey != nil {
		signature, err = rsa.SignPKCS1v15(rand.Reader, p.rsaKey, 5 /* crypto.SHA256 */, digest[:])
		if err != nil {
			p.t.Fatal(err)
		}
	} else {
		r, s, err := ecdsa.Sign(rand.Reader, p.ecKey, digest[:])
		if err != nil {
			p.t.Fatal(err)
		}
		signature = make([]byte, 64)
		r.FillBytes(signature[:32])
		s.FillBytes(signature[32:])
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// ---------------------------------------------------------------------------
// The instance under test
// ---------------------------------------------------------------------------

type harness struct {
	t       *testing.T
	store   *telemetry.Store
	service *Service
	server  *httptest.Server
	client  *http.Client
}

// newHarness wires a service the way main.go does — the middleware around a mux
// with the sign-in routes on it — and gives back a browser that keeps cookies
// and does not follow redirects, so every hop can be asserted on.
func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	store := telemetry.NewStore(100)
	t.Cleanup(func() { store.Close() })
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second}
	}
	service, err := New(store, cfg)
	if err != nil {
		t.Fatalf("build the service: %v", err)
	}
	mux := http.NewServeMux()
	service.Register(mux)
	mux.HandleFunc("POST /v1/logs", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("/api/summary", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	})
	// Everything else is "a page", which is what the dashboard is.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		view := Login(r.Context())
		_, _ = io.WriteString(w, "path="+r.URL.Path+" viewer="+Viewer(r.Context()).Email+
			" providers="+strings.Join(providerIDs(view), ",")+" error="+view.Error)
	})
	server := httptest.NewServer(service.Guard(mux))
	t.Cleanup(server.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, store: store, service: service, server: server, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func providerIDs(view model.LoginView) []string {
	out := make([]string, 0, len(view.Providers))
	for _, provider := range view.Providers {
		out = append(out, provider.ID)
	}
	return out
}

func (h *harness) get(path string, headers ...[2]string) *http.Response {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Accept", "text/html")
	for _, header := range headers {
		request.Header.Set(header[0], header[1])
	}
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { response.Body.Close() })
	return response
}

func (h *harness) post(path string, form url.Values) *http.Response {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { response.Body.Close() })
	return response
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// signIn walks the whole browser flow and returns the final response.
//
// The code handed to the callback carries the nonce guard generated, which is
// how the mock knows what to put in the token — the same thing a real provider
// does by remembering the authorize request.
func (h *harness) signIn(provider string, extra url.Values) *http.Response {
	h.t.Helper()
	start := h.get("/auth/" + provider + "/start")
	if start.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("start = %d, want 303", start.StatusCode)
	}
	sent, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	state := sent.Query().Get("state")
	nonce := sent.Query().Get("nonce")
	form := url.Values{"code": {"code-1:" + nonce}, "state": {state}}
	for key, values := range extra {
		form[key] = values
	}
	if provider == model.ProviderApple {
		// Apple's cross-site form POST, which is why the state lives in SQLite
		// rather than in a cookie.
		return h.post("/auth/"+provider+"/callback", form)
	}
	return h.get("/auth/" + provider + "/callback?" + form.Encode())
}

func googleConfig(t *testing.T, provider *idp, admins ...string) Config {
	t.Helper()
	return Config{
		Google:    Google{ClientID: provider.clientID, ClientSecret: "google-secret"},
		Admins:    admins,
		BaseURL:   "https://guard.test",
		Client:    provider.server.Client(),
		endpoints: map[string]endpoints{model.ProviderGoogle: provider.endpoints()},
	}
}

// ---------------------------------------------------------------------------
// Off by default
// ---------------------------------------------------------------------------

// With no credentials guard is the tool it has always been: no login page in
// the way, no session, and the OTLP door open to whatever holds the token.
func TestSignInOffLeavesEverythingOpen(t *testing.T) {
	h := newHarness(t, Config{})
	if h.service.Enabled() {
		t.Fatal("sign-in should be off with no credentials")
	}
	if response := h.get("/logs"); response.StatusCode != http.StatusOK {
		t.Fatalf("page = %d, want 200", response.StatusCode)
	}
	if response := h.get("/auth/google/start"); response.StatusCode != http.StatusNotFound {
		t.Fatalf("start = %d, want 404 — there is no flow to start", response.StatusCode)
	}
}

// Half a configuration is somebody who meant to close the door. Starting
// anyway, open, is the failure mode worth refusing outright.
func TestHalfConfiguredIsFatal(t *testing.T) {
	store := telemetry.NewStore(10)
	t.Cleanup(func() { store.Close() })
	if _, err := New(store, Config{Google: Google{ClientID: "id-only"}}); err == nil {
		t.Fatal("a client id with no secret should not build")
	}
	if _, err := New(store, Config{Apple: Apple{ClientID: "id", TeamID: "team"}}); err == nil {
		t.Fatal("half an apple configuration should not build")
	}
}

// ---------------------------------------------------------------------------
// Google
// ---------------------------------------------------------------------------

func TestGoogleSignInEndToEnd(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))

	// A page, from nobody: the login page, with the destination remembered.
	denied := h.get("/logs")
	if denied.StatusCode != http.StatusSeeOther {
		t.Fatalf("guarded page = %d, want 303", denied.StatusCode)
	}
	if location := denied.Header.Get("Location"); location != "/login?next=%2Flogs" {
		t.Fatalf("sent to %q", location)
	}

	// The authorize URL has to carry everything the provider needs, and the
	// redirect has to be the one registered in the console.
	start := h.get("/auth/google/start?next=/traces")
	sent, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := sent.Query()
	for field, want := range map[string]string{
		"client_id":     "google-client",
		"response_type": "code",
		"scope":         "openid email profile",
		"redirect_uri":  "https://guard.test/auth/google/callback",
	} {
		if query.Get(field) != want {
			t.Fatalf("%s = %q, want %q", field, query.Get(field), want)
		}
	}
	if query.Get("state") == "" || query.Get("nonce") == "" {
		t.Fatal("the authorize URL needs a state and a nonce")
	}

	// Come back with the code.
	callback := h.get("/auth/google/callback?" + url.Values{
		"code":  {"code-1:" + query.Get("nonce")},
		"state": {query.Get("state")},
	}.Encode())
	if callback.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %d, want 303: %s", callback.StatusCode, body(t, callback))
	}
	if location := callback.Header.Get("Location"); location != "/traces" {
		t.Fatalf("landed on %q, want the page they asked for", location)
	}
	if provider.lastForm.Get("redirect_uri") != "https://guard.test/auth/google/callback" {
		t.Fatalf("the exchange sent redirect_uri = %q", provider.lastForm.Get("redirect_uri"))
	}
	if provider.lastSecret != "google-secret" {
		t.Fatalf("the exchange sent client_secret = %q", provider.lastSecret)
	}

	// And now the dashboard answers, as somebody.
	page := h.get("/logs")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("page after sign-in = %d", page.StatusCode)
	}
	if text := body(t, page); !strings.Contains(text, "viewer=ana@example.com") {
		t.Fatalf("the page does not know who it is for: %q", text)
	}

	// The session survives a JSON request too, which is what the dashboard
	// actually makes every three seconds.
	if response := h.get("/api/summary"); response.StatusCode != http.StatusOK {
		t.Fatalf("api = %d, want 200", response.StatusCode)
	}

	// Signing out ends it, and the next page asks again.
	out := h.post("/auth/logout", nil)
	if out.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d", out.StatusCode)
	}
	if after := h.get("/logs"); after.StatusCode != http.StatusSeeOther {
		t.Fatalf("page after logout = %d, want the login page", after.StatusCode)
	}
}

// The members list is the allowlist. A provider proving somebody's identity
// perfectly is not a reason to let them in.
func TestSignInRefusedForNonMember(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	provider.email = "stranger@example.com"
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))

	response := h.signIn(model.ProviderGoogle, nil)
	if location := response.Header.Get("Location"); location != "/login?error=forbidden" {
		t.Fatalf("sent to %q, want the forbidden message", location)
	}
	if len(response.Cookies()) > 0 {
		t.Fatal("a refused sign-in must not set a cookie")
	}
	if page := h.get("/logs"); page.StatusCode != http.StatusSeeOther {
		t.Fatal("a refused sign-in must not open the dashboard")
	}
}

// Somebody added to the list from the members page can sign in with no
// restart — the check reads the table on every sign-in, not at startup.
func TestStoredMemberMaySignIn(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	provider.email = "bo@example.com"
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))

	if _, err := h.store.SaveMember(model.Member{Email: "BO@example.com", Role: model.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if location := h.signIn(model.ProviderGoogle, nil).Header.Get("Location"); location != "/" {
		t.Fatalf("sent to %q, want the dashboard", location)
	}
	// And the sign-in is recorded against the row, which is what makes the
	// members page readable.
	member, err := h.store.Member("bo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if member.LastSeen == nil || member.Provider != model.ProviderGoogle {
		t.Fatalf("the sign-in was not recorded: %#v", member)
	}
}

// Removing somebody has to reach the browser they left open. It does, on their
// next request, because the member is read per request rather than trusted from
// the session row.
func TestRemovingAMemberEndsTheirSession(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	provider.email = "bo@example.com"
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))
	if _, err := h.store.SaveMember(model.Member{Email: "bo@example.com"}); err != nil {
		t.Fatal(err)
	}
	h.signIn(model.ProviderGoogle, nil)
	if page := h.get("/logs"); page.StatusCode != http.StatusOK {
		t.Fatalf("page = %d, want 200 before the removal", page.StatusCode)
	}
	if err := h.store.RemoveMember("bo@example.com"); err != nil {
		t.Fatal(err)
	}
	if page := h.get("/logs"); page.StatusCode != http.StatusSeeOther {
		t.Fatalf("page = %d, want the login page after the removal", page.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Apple
// ---------------------------------------------------------------------------

func appleKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// Apple's callback is a cross-site form POST carrying the name exactly once,
// and its client secret is a JWT guard has to sign itself. Both are checked
// here, the second by verifying the signature the mock received.
func TestAppleSignInFormPost(t *testing.T) {
	provider := newIDP(t, "ES256", "app.hushkey.guard")
	provider.name = "" // Apple's id token carries no name
	provider.email = "ana@example.com"
	h := newHarness(t, Config{
		Apple: Apple{
			ClientID: provider.clientID, TeamID: "TEAM123", KeyID: "KEY123", PrivateKey: appleKey(t),
		},
		Admins:    []string{"ana@example.com"},
		BaseURL:   "https://guard.test",
		Client:    provider.server.Client(),
		endpoints: map[string]endpoints{model.ProviderApple: provider.endpoints()},
	})

	// The authorize URL must ask for form_post, or Apple refuses the request.
	start := h.get("/auth/apple/start")
	sent, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if sent.Query().Get("response_mode") != "form_post" {
		t.Fatalf("response_mode = %q", sent.Query().Get("response_mode"))
	}

	form := url.Values{
		"code":  {"code-1:" + sent.Query().Get("nonce")},
		"state": {sent.Query().Get("state")},
		"user":  {`{"name":{"firstName":"Ana","lastName":"Ruiz"},"email":"ana@example.com"}`},
	}
	response := h.post("/auth/apple/callback", form)
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %d: %s", response.StatusCode, body(t, response))
	}
	if location := response.Header.Get("Location"); location != "/" {
		t.Fatalf("sent to %q", location)
	}

	// The name Apple sent once is the name the dashboard shows.
	page := body(t, h.get("/"))
	if !strings.Contains(page, "viewer=ana@example.com") {
		t.Fatalf("not signed in: %q", page)
	}

	// The client secret is a JWT, signed with the .p8, addressed to Apple.
	claims := unsafeClaims(t, provider.lastSecret)
	if claims["iss"] != "TEAM123" || claims["sub"] != provider.clientID || claims["aud"] != "https://appleid.apple.com" {
		t.Fatalf("client secret claims = %#v", claims)
	}
	if header := unsafeHeader(t, provider.lastSecret); header["kid"] != "KEY123" || header["alg"] != "ES256" {
		t.Fatalf("client secret header = %#v", header)
	}
}

func unsafeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func unsafeHeader(t *testing.T, token string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	return header
}

// Apple sends email_verified as the string "true". A strict decode would fail
// every Apple sign-in on a pair of quotes.
func TestAppleStringBooleanIsAccepted(t *testing.T) {
	provider := newIDP(t, "ES256", "app.hushkey.guard")
	provider.emailVerified = "true"
	h := newHarness(t, Config{
		Apple:     Apple{ClientID: provider.clientID, TeamID: "T", KeyID: "K", PrivateKey: appleKey(t)},
		Admins:    []string{"ana@example.com"},
		BaseURL:   "https://guard.test",
		Client:    provider.server.Client(),
		endpoints: map[string]endpoints{model.ProviderApple: provider.endpoints()},
	})
	if location := h.signIn(model.ProviderApple, nil).Header.Get("Location"); location != "/" {
		t.Fatalf("sent to %q, want the dashboard", location)
	}
}

// ---------------------------------------------------------------------------
// The tokens guard must refuse
// ---------------------------------------------------------------------------

func TestRefusedTokens(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*idp)
		want   string
	}{
		{"a token for another session", func(p *idp) { p.nonce = "somebody-elses-nonce" }, "/login?error=failed"},
		{"a token for another client", func(p *idp) { p.audience = "another-client" }, "/login?error=failed"},
		{"a token from another issuer", func(p *idp) { p.issuer = "https://evil.test" }, "/login?error=failed"},
		{"an expired token", func(p *idp) { p.expiresIn = -2 * time.Hour }, "/login?error=failed"},
		{"an unverified address", func(p *idp) { p.emailVerified = false }, "/login?error=unverified"},
		{"no address at all", func(p *idp) { p.email = "" }, "/login?error=unverified"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provider := newIDP(t, "RS256", "google-client")
			// The issuer knob has to move before the endpoints are read, so
			// the break is applied first and the config built after.
			test.break_(provider)
			h := newHarness(t, Config{
				Google:  Google{ClientID: provider.clientID, ClientSecret: "google-secret"},
				Admins:  []string{"ana@example.com"},
				BaseURL: "https://guard.test",
				Client:  provider.server.Client(),
				endpoints: map[string]endpoints{model.ProviderGoogle: {
					authorize: provider.server.URL + "/authorize",
					token:     provider.server.URL + "/token",
					keys:      provider.server.URL + "/keys",
					// Guard keeps expecting the real issuer; a token from
					// anywhere else is somebody else's.
					issuers: []string{"https://idp.test"},
				}},
			})
			response := h.signIn(model.ProviderGoogle, nil)
			if location := response.Header.Get("Location"); location != test.want {
				t.Fatalf("sent to %q, want %q", location, test.want)
			}
			if page := h.get("/logs"); page.StatusCode != http.StatusSeeOther {
				t.Fatal("a refused token must not open the dashboard")
			}
		})
	}
}

// A state is single use. Replaying a callback — from history, from a log, from
// somebody's referer header — finds nothing.
func TestStateIsSingleUse(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))

	start := h.get("/auth/google/start")
	sent, _ := url.Parse(start.Header.Get("Location"))
	callback := "/auth/google/callback?" + url.Values{
		"code":  {"code-1:" + sent.Query().Get("nonce")},
		"state": {sent.Query().Get("state")},
	}.Encode()

	if location := h.get(callback).Header.Get("Location"); location != "/" {
		t.Fatalf("first callback sent to %q", location)
	}
	if location := h.get(callback).Header.Get("Location"); location != "/login?error=expired" {
		t.Fatalf("replayed callback sent to %q, want the expired message", location)
	}
}

// A callback with a state guard never issued is the classic login-CSRF, and it
// is refused before any code is exchanged.
func TestUnknownStateIsRefused(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))
	response := h.get("/auth/google/callback?code=code-1:nonce&state=invented")
	if location := response.Header.Get("Location"); location != "/login?error=expired" {
		t.Fatalf("sent to %q", location)
	}
	if provider.lastForm != nil {
		t.Fatal("the code must not be exchanged for an unknown state")
	}
}

// Cancelling at the provider is not an error on guard's side and must not read
// like one.
func TestProviderRefusalReadsAsCancelled(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))
	response := h.get("/auth/google/callback?error=access_denied&state=whatever")
	if location := response.Header.Get("Location"); location != "/login?error=cancelled" {
		t.Fatalf("sent to %q", location)
	}
}

// ---------------------------------------------------------------------------
// What the guard lets past without a session
// ---------------------------------------------------------------------------

func TestTelemetryDoorStaysOpen(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))

	// An exporter has no cookie and cannot get one. If sign-in reached /v1,
	// every collector pointed at guard would stop working the day it was
	// switched on.
	response, err := h.client.Post(h.server.URL+"/v1/logs", "application/x-protobuf", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("otlp = %d, want 202", response.StatusCode)
	}
	// And the login page itself, obviously — with the buttons that were built.
	page := h.get("/login")
	if page.StatusCode != http.StatusOK {
		t.Fatalf("login page = %d", page.StatusCode)
	}
	if text := body(t, page); !strings.Contains(text, "providers=google") {
		t.Fatalf("the login page was not told what exists: %q", text)
	}
}

// A JSON caller gets JSON, not a redirect into an HTML login form it would
// paste into the dashboard.
func TestApiAnswers401WithoutASession(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))
	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/summary", nil)
	request.Header.Set("Accept", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("api = %d, want 401", response.StatusCode)
	}
	var answer map[string]string
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if answer["login"] != "/login" {
		t.Fatalf("the 401 does not say where to go: %#v", answer)
	}
}

// The bearer token is how everything that is not a person keeps working.
func TestApiTokenSkipsTheBrowserFlow(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	cfg := googleConfig(t, provider, "ana@example.com")
	cfg.APIToken = "s3cret"
	h := newHarness(t, cfg)

	request, _ := http.NewRequest(http.MethodGet, h.server.URL+"/api/summary", nil)
	request.Header.Set("Authorization", "Bearer s3cret")
	response, err := h.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("api with the token = %d, want 200", response.StatusCode)
	}
}

// Already signed in and asking for the login page: there is nothing there.
func TestLoginPageRedirectsWhenSignedIn(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	h := newHarness(t, googleConfig(t, provider, "ana@example.com"))
	h.signIn(model.ProviderGoogle, nil)
	if response := h.get("/login"); response.StatusCode != http.StatusSeeOther {
		t.Fatalf("login page while signed in = %d, want 303", response.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// A member reads; an admin changes things. Both are decided from the list
// rather than from anything the browser sent.
func TestAuthorizeSeparatesMembersFromAdmins(t *testing.T) {
	provider := newIDP(t, "RS256", "google-client")
	provider.email = "bo@example.com"
	cfg := googleConfig(t, provider, "ana@example.com")
	h := newHarness(t, cfg)
	if _, err := h.store.SaveMember(model.Member{Email: "bo@example.com", Role: model.RoleMember}); err != nil {
		t.Fatal(err)
	}

	// The endpoint layer asks with the request, so the test asks the same way:
	// through the middleware, from a handler that records the answers.
	var readErr, writeErr error
	mux := http.NewServeMux()
	mux.HandleFunc("/api/probe", func(w http.ResponseWriter, r *http.Request) {
		readErr = h.service.Authorize(r, nil)
		writeErr = h.service.Authorize(r, []string{model.RoleAdmin})
	})
	guarded := httptest.NewServer(h.service.Guard(mux))
	defer guarded.Close()

	h.signIn(model.ProviderGoogle, nil)
	// Move the cookie the harness collected onto a request to the second
	// server, which is the same service behind a different mux.
	request, _ := http.NewRequest(http.MethodGet, guarded.URL+"/api/probe", nil)
	for _, cookie := range h.client.Jar.Cookies(mustParse(t, h.server.URL)) {
		request.AddCookie(cookie)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if readErr != nil {
		t.Fatalf("a member may not read: %v", readErr)
	}
	if writeErr == nil {
		t.Fatal("a member must not pass an admin endpoint")
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Small parts
// ---------------------------------------------------------------------------

// safeNext is the difference between "carry on where you were" and an open
// redirect somebody puts in an email.
func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                       "/",
		"/logs":                  "/logs",
		"/logs?service=api":      "/logs?service=api",
		"//evil.example":         "/",
		"https://evil.example":   "/",
		"/\\evil.example":        "/",
		"/login":                 "/",
		"/login?error=forbidden": "/",
		"/auth/google/start":     "/",
	}
	for input, want := range cases {
		if got := safeNext(input); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", input, got, want)
		}
	}
}

// The environment's admin is allowed with an empty database, which is the state
// every new instance is in.
func TestEnvironmentAdminNeedsNoRow(t *testing.T) {
	store := telemetry.NewStore(10)
	t.Cleanup(func() { store.Close() })
	service, err := New(store, Config{
		Google: Google{ClientID: "id", ClientSecret: "secret"},
		Admins: []string{"Leo@Hushkey.app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	member, ok := service.member("leo@hushkey.app")
	if !ok || !member.IsAdmin() || !member.Fixed {
		t.Fatalf("member = %#v, ok = %v", member, ok)
	}
	if _, ok := service.member("someone@else.test"); ok {
		t.Fatal("an address on no list must not be allowed")
	}
}

// A key set that rotates: an unknown key id is a reason to fetch again, not a
// reason to fail — but only once a minute, so a forged token cannot be used to
// hammer the provider.
func TestKeySetRefetchesOnce(t *testing.T) {
	provider := newIDP(t, "RS256", "client")
	fetches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		provider.keys(w, r)
	}))
	defer server.Close()

	keys := &keySet{url: server.URL, client: server.Client()}
	if _, err := keys.key(t.Context(), "test-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.key(t.Context(), "test-key"); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("fetched %d times for a key it already had", fetches)
	}
	if _, err := keys.key(t.Context(), "unknown"); err == nil {
		t.Fatal("an unknown key id should fail")
	}
	if fetches != 1 {
		t.Fatalf("fetched %d times — the refetch should be rate limited", fetches)
	}
}
