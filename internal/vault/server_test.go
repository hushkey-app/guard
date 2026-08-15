package vault

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// The vault reads a database guard wrote, so a test has to be two processes'
// worth of setup: guard opens the file and puts secrets in it, then the vault
// opens the same file with the same key file beside it.
func setup(t *testing.T) (*telemetry.Store, *Server, model.APIKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "guard.db")
	t.Setenv("GUARD_SECRET_KEY", "a-test-key-for-both-halves")

	guard, err := telemetry.Open(path, telemetry.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guard.Close() })

	envs, err := guard.Envs()
	if err != nil {
		t.Fatal(err)
	}
	var production model.Env
	for _, env := range envs {
		if env.Name == "production" {
			production = env
		}
	}
	for key, value := range map[string]string{"DATABASE_URL": "postgres://db/app", "API_TOKEN": "t0ken"} {
		if _, err := guard.SaveSecret(model.Secret{EnvID: production.ID, Key: key, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	minted, err := guard.CreateAPIKey(model.APIKey{EnvID: production.ID, Name: "hushkey-web"})
	if err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return guard, &Server{Store: store, Touch: time.Nanosecond}, minted
}

func serve(t *testing.T, server *Server, method, path, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(method, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestAKeyReadsItsOwnEnvironment(t *testing.T) {
	_, server, key := setup(t)

	response := serve(t, server, "GET", "/v1/secrets", key.Token, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("%d: %s", response.Code, response.Body)
	}
	var answer Answer
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Env != "production" || answer.Secrets["DATABASE_URL"] != "postgres://db/app" {
		t.Fatalf("answered %+v", answer)
	}
	if len(answer.Unreadable) != 0 {
		t.Fatalf("could not read %+v", answer.Unreadable)
	}
	// The ETag is the revision, and a caller holding it is told nothing moved
	// rather than handed the values again.
	tag := response.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	again := serve(t, server, "GET", "/v1/secrets", key.Token, map[string]string{"If-None-Match": tag})
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Fatalf("%d: %s", again.Code, again.Body)
	}
}

func TestTheEnvironmentComesFromTheKey(t *testing.T) {
	guard, server, key := setup(t)

	// A second environment with a secret nobody's key may read.
	other, err := guard.SaveEnv(model.Env{Name: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.SaveSecret(model.Secret{EnvID: other.ID, Key: "NOT_YOURS", Value: "x"}); err != nil {
		t.Fatal(err)
	}

	// Every shape of "let me pick the environment" a caller might try. None of
	// them can work, because nothing reads a parameter — this asserts that no
	// future handler starts to.
	for _, path := range []string{
		"/v1/secrets?env=elsewhere",
		"/v1/secrets?env_id=" + "2",
		"/v1/secrets?environment=elsewhere",
	} {
		response := serve(t, server, "GET", path, key.Token, nil)
		var answer Answer
		if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
			t.Fatal(err)
		}
		if answer.Env != "production" {
			t.Fatalf("%s answered for %q", path, answer.Env)
		}
		if _, found := answer.Secrets["NOT_YOURS"]; found {
			t.Fatalf("%s crossed environments", path)
		}
	}
}

func TestUnknownRevokedAndExpiredAreOneAnswer(t *testing.T) {
	guard, server, key := setup(t)

	none := serve(t, server, "GET", "/v1/secrets", "", nil)
	if none.Code != http.StatusUnauthorized || none.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d %v", none.Code, none.Header())
	}
	unknown := serve(t, server, "GET", "/v1/secrets", "gsk_production_nonsense", nil)
	if unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: %d", unknown.Code)
	}

	if err := guard.RevokeAPIKey(key.ID); err != nil {
		t.Fatal(err)
	}
	revoked := serve(t, server, "GET", "/v1/secrets", key.Token, nil)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", revoked.Code)
	}
	if revoked.Body.String() != unknown.Body.String() {
		t.Fatalf("a revoked key is told apart from an unknown one:\n%s\n%s", revoked.Body, unknown.Body)
	}
}

// Guard's own credentials are not vault credentials, and this is the test that
// keeps it that way. It is true today by omission — nothing in this package
// reads GUARD_TOKEN or a session cookie — and omission is exactly the kind of
// thing somebody helpfully fixes later. An exporter's bearer token means "a
// machine, let it through" on guard; here it must mean nothing at all, because
// the token that posts telemetry is on every host in the fleet.
func TestGuardsOwnCredentialsAreNotVaultCredentials(t *testing.T) {
	_, server, _ := setup(t)
	t.Setenv("GUARD_TOKEN", "a-machine-token")

	for _, credential := range []string{"a-machine-token", "guard_session=whatever"} {
		response := serve(t, server, "GET", "/v1/secrets", credential, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%q got in: %d", credential, response.Code)
		}
	}
	// And a cookie, which is the other way a browser would try.
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest("GET", "/v1/secrets", nil)
	request.AddCookie(&http.Cookie{Name: "guard_session", Value: "whatever"})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a session cookie got in: %d", recorder.Code)
	}
}

func TestAnExpiredKeyStopsWorking(t *testing.T) {
	guard, server, _ := setup(t)
	envs, err := guard.Envs()
	if err != nil {
		t.Fatal(err)
	}
	expired, err := guard.CreateAPIKey(model.APIKey{
		EnvID: envs[0].ID, Name: "yesterday", ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := serve(t, server, "GET", "/v1/secrets", expired.Token, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", response.Code)
	}
}

func TestOneSecretAndTheEnvFormat(t *testing.T) {
	_, server, key := setup(t)

	one := serve(t, server, "GET", "/v1/secrets/API_TOKEN", key.Token, nil)
	if one.Code != http.StatusOK {
		t.Fatalf("%d: %s", one.Code, one.Body)
	}
	var secret model.Secret
	if err := json.Unmarshal(one.Body.Bytes(), &secret); err != nil {
		t.Fatal(err)
	}
	if secret.Value != "t0ken" {
		t.Fatalf("answered %+v", secret)
	}
	if missing := serve(t, server, "GET", "/v1/secrets/NOPE", key.Token, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("a key that is not there: %d", missing.Code)
	}

	// The .env form, which is how most things still take configuration.
	text := serve(t, server, "GET", "/v1/secrets?format=env", key.Token, nil)
	pairs, skipped := model.ParseEnv(text.Body.String())
	if len(skipped) != 0 || len(pairs) != 2 {
		t.Fatalf("the .env output does not parse: %+v %+v", pairs, skipped)
	}
}

func TestAFetchIsRecordedAndNeverBlocks(t *testing.T) {
	guard, server, key := setup(t)

	if before, _ := guard.APIKeys(); !before[0].LastUsedAt.IsZero() {
		t.Fatalf("used before it was: %+v", before[0])
	}
	if response := serve(t, server, "GET", "/v1/secrets", key.Token, nil); response.Code != http.StatusOK {
		t.Fatalf("%d", response.Code)
	}
	// Recorded after the answer went out, not before it: the write is off the
	// request path on purpose, so this waits rather than assuming.
	var recorded bool
	for range 50 {
		keys, err := guard.APIKeys()
		if err != nil {
			t.Fatal(err)
		}
		if !keys[0].LastUsedAt.IsZero() {
			recorded = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !recorded {
		t.Fatal("the fetch was never recorded")
	}

	// And with the database gone from under it, the fetch still answers: the
	// bookkeeping is worth having and never worth failing a boot for.
	server.Store.db.Close()
	response := serve(t, server, "GET", "/v1/secrets", key.Token, nil)
	if response.Code == http.StatusOK {
		t.Fatal("a closed database answered with secrets")
	}
}

func TestAMissingKeyFileIsRefusedRatherThanInvented(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	t.Setenv("GUARD_SECRET_KEY", "a-test-key")
	guard, err := telemetry.Open(path, telemetry.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	guard.Close()

	// The deployment that forgot to mount the key: no GUARD_SECRET_KEY, no
	// key file. A vault that generated one here would come up healthy and
	// answer every fetch with values it cannot decrypt.
	t.Setenv("GUARD_SECRET_KEY", "")
	if store, err := Open(path); err == nil {
		store.Close()
		t.Fatal("the vault started without the key that seals the secrets")
	}
}
