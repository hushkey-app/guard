package secretsapi_test

// What these pin is the four rules in the package doc, and each one is here
// because it is true by construction today and construction changes.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/secretsapi"
	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type door struct {
	server *httptest.Server
	store  *telemetry.Store
	read   string
	write  string
	env    model.Env
	other  model.Env
}

func open(t *testing.T) door {
	t.Helper()
	t.Setenv("GUARD_SECRET_KEY", "a-test-key-for-the-writing-door")
	store, err := telemetry.Open(filepath.Join(t.TempDir(), "guard.db"), telemetry.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	spaces, err := store.Workspaces()
	if err != nil || len(spaces) == 0 {
		t.Fatalf("workspaces: %+v %v", spaces, err)
	}
	envs, err := store.Envs(spaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var develop, production model.Env
	for _, env := range envs {
		switch env.Name {
		case "develop":
			develop = env
		case "production":
			production = env
		}
	}
	if develop.ID == 0 || production.ID == 0 {
		t.Fatalf("a new workspace should be seeded with develop and production, got %+v", envs)
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: develop.ID, Key: "DATABASE_URL", Value: "postgres://dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: production.ID, Key: "DATABASE_URL", Value: "postgres://prod"}); err != nil {
		t.Fatal(err)
	}
	reader, err := store.CreateAPIKey(model.APIKey{EnvID: develop.ID, Name: "the app"})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.CreateAPIKey(model.APIKey{EnvID: develop.ID, Name: "the deploy", CanWrite: true})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	if _, err := secretsapi.Register(mux, secretsapi.Config{Enabled: true, Store: store}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return door{server: server, store: store, read: reader.Token, write: writer.Token,
		env: develop, other: production}
}

func (d door) call(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, d.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	answer, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(answer)
}

// A read key is a read key. This is the whole point of the column: every key
// that existed before this feature keeps meaning exactly what it meant.
func TestAReadKeyCannotWrite(t *testing.T) {
	d := open(t)
	for _, call := range []struct {
		method, path, body string
	}{
		{http.MethodPut, "/v1/secrets/NEW_ONE", `{"value":"x"}`},
		{http.MethodDelete, "/v1/secrets/DATABASE_URL", ""},
		{http.MethodPost, "/v1/secrets", `{"secrets":{"NEW_ONE":"x"}}`},
	} {
		code, body := d.call(t, call.method, call.path, d.read, call.body)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, wanted 403 — %s", call.method, call.path, code, body)
		}
		if !strings.Contains(body, "cannot change it") {
			t.Fatalf("%s %s said %q, which does not explain itself", call.method, call.path, body)
		}
	}
	// And it really did not happen.
	pairs, err := d.store.Secrets(d.env.ID)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("the environment should still hold exactly its one secret, got %+v %v", pairs, err)
	}
}

// The environment comes from the key, and nothing in a request may move it.
// Five spellings, because the one that works is the one nobody tried.
func TestTheEnvironmentCannotBeChosen(t *testing.T) {
	d := open(t)
	for _, path := range []string{
		"/v1/secrets?env=production",
		"/v1/secrets?env_id=" + strconv.FormatInt(d.other.ID, 10),
		"/v1/secrets?workspace=default&env=production",
		"/v1/secrets?env=" + strconv.FormatInt(d.other.ID, 10),
		"/v1/secrets/DATABASE_URL?env=production",
	} {
		code, body := d.call(t, http.MethodGet, path, d.read, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d — %s", path, code, body)
		}
		if strings.Contains(body, "postgres://prod") {
			t.Fatalf("GET %s reached production: %s", path, body)
		}
		if !strings.Contains(body, "postgres://dev") {
			t.Fatalf("GET %s did not answer from develop: %s", path, body)
		}
	}
	// Writing cannot cross either: the key names the environment, so this lands
	// in develop no matter what the query says.
	code, body := d.call(t, http.MethodPut, "/v1/secrets/ONLY_HERE?env=production", d.write, `{"value":"x"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d — %s", code, body)
	}
	if _, err := d.store.Secret(d.other.ID, "ONLY_HERE"); err == nil {
		t.Fatal("a develop key wrote into production")
	}
	if _, err := d.store.Secret(d.env.ID, "ONLY_HERE"); err != nil {
		t.Fatalf("the write did not land in develop: %v", err)
	}
}

// A write key sets, reads back and removes — the round trip a script makes.
func TestAWriteKeyDoesTheRoundTrip(t *testing.T) {
	d := open(t)
	if code, body := d.call(t, http.MethodPut, "/v1/secrets/API_TOKEN", d.write, `{"value":"sk-123"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d — %s", code, body)
	}
	code, body := d.call(t, http.MethodGet, "/v1/secrets/API_TOKEN", d.read, "")
	if code != http.StatusOK || !strings.Contains(body, "sk-123") {
		t.Fatalf("GET = %d — %s", code, body)
	}
	if code, body := d.call(t, http.MethodDelete, "/v1/secrets/API_TOKEN", d.write, ""); code != http.StatusOK {
		t.Fatalf("DELETE = %d — %s", code, body)
	}
	if code, _ := d.call(t, http.MethodGet, "/v1/secrets/API_TOKEN", d.read, ""); code != http.StatusNotFound {
		t.Fatalf("after the delete, GET = %d, wanted 404", code)
	}
	// A second delete is an error rather than a shrug. The idempotent way to
	// make an environment match something is apply with prune.
	if code, _ := d.call(t, http.MethodDelete, "/v1/secrets/API_TOKEN", d.write, ""); code != http.StatusNotFound {
		t.Fatalf("deleting what is gone = %d, wanted 404", code)
	}
}

// The deployment's cleanup: describe it, then do exactly that.
func TestApplyPrunesOnlyWhenAsked(t *testing.T) {
	d := open(t)
	const body = `{"secrets":{"DATABASE_URL":"postgres://dev","REDIS_URL":"redis://dev"},"prune":true,"dry_run":true}`
	code, answer := d.call(t, http.MethodPost, "/v1/secrets", d.write, body)
	if code != http.StatusOK {
		t.Fatalf("dry run = %d — %s", code, answer)
	}
	var dry model.ImportResult
	if err := json.Unmarshal([]byte(answer), &dry); err != nil {
		t.Fatal(err)
	}
	if len(dry.Added) != 1 || dry.Added[0] != "REDIS_URL" {
		t.Fatalf("added %v, wanted just REDIS_URL", dry.Added)
	}
	if len(dry.Unchanged) != 1 || dry.Unchanged[0] != "DATABASE_URL" {
		t.Fatalf("unchanged %v, wanted just DATABASE_URL", dry.Unchanged)
	}
	// A dry run changed nothing.
	pairs, _ := d.store.Secrets(d.env.ID)
	if len(pairs) != 1 {
		t.Fatalf("the dry run wrote something: %+v", pairs)
	}

	if code, answer := d.call(t, http.MethodPost, "/v1/secrets", d.write,
		`{"secrets":{"REDIS_URL":"redis://dev"},"prune":true}`); code != http.StatusOK {
		t.Fatalf("apply = %d — %s", code, answer)
	}
	pairs, _ = d.store.Secrets(d.env.ID)
	if len(pairs) != 1 || pairs[0].Key != "REDIS_URL" {
		t.Fatalf("prune should have left exactly REDIS_URL, got %+v", pairs)
	}
	// Production was not in the blast radius.
	others, _ := d.store.Secrets(d.other.ID)
	if len(others) != 1 || others[0].Key != "DATABASE_URL" {
		t.Fatalf("pruning develop touched production: %+v", others)
	}
}

// Pruning against nothing is refused. An empty body is far more often a bug in
// a script than somebody meaning "empty production".
func TestPruningAgainstNothingIsRefused(t *testing.T) {
	d := open(t)
	code, body := d.call(t, http.MethodPost, "/v1/secrets", d.write, `{"prune":true}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "refused") {
		t.Fatalf("= %d %s, wanted a refusal", code, body)
	}
	if pairs, _ := d.store.Secrets(d.env.ID); len(pairs) != 1 {
		t.Fatalf("it emptied the environment: %+v", pairs)
	}
}

// Unknown, revoked and expired are one answer, and GUARD_TOKEN is not one of
// the things that opens this.
func TestOnlyASecretsKeyOpensIt(t *testing.T) {
	d := open(t)
	revoked, err := d.store.CreateAPIKey(model.APIKey{EnvID: d.env.ID, Name: "gone", CanWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.RevokeAPIKey(revoked.ID); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "guard-admin-token", "gsk_made_up_nonsense", revoked.Token} {
		code, body := d.call(t, http.MethodGet, "/v1/secrets", token, "")
		if code != http.StatusUnauthorized {
			t.Fatalf("token %q = %d, wanted 401 — %s", token, code, body)
		}
	}
	// One answer for all of them: nothing says which of the three it was.
	_, unknown := d.call(t, http.MethodGet, "/v1/secrets", "gsk_made_up_nonsense", "")
	_, gone := d.call(t, http.MethodGet, "/v1/secrets", revoked.Token, "")
	if unknown != gone {
		t.Fatalf("an unknown key and a revoked one answer differently:\n%s\n%s", unknown, gone)
	}
}

// Off is off. The default instance publishes guard's port and must not be
// answering this on it.
func TestOffRegistersNothing(t *testing.T) {
	mux := http.NewServeMux()
	local, err := secretsapi.Register(mux, secretsapi.Config{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if local {
		t.Fatal("a disabled door claimed the routes")
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, path := range []string{"/v1/secrets", "/v1/secrets/DATABASE_URL", "/v1/whoami"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s = %d with the door off, wanted 404", path, response.StatusCode)
		}
	}
}

// whoami is what a script checks first, and it has to be right about the
// permission or the first write is a surprise.
func TestWhoamiSaysWhatTheKeyMay(t *testing.T) {
	d := open(t)
	for token, wanted := range map[string]bool{d.read: false, d.write: true} {
		code, body := d.call(t, http.MethodGet, "/v1/whoami", token, "")
		if code != http.StatusOK {
			t.Fatalf("= %d — %s", code, body)
		}
		var who secretsapi.Whoami
		if err := json.Unmarshal([]byte(body), &who); err != nil {
			t.Fatal(err)
		}
		if who.CanWrite != wanted {
			t.Fatalf("can_write = %v, wanted %v — %s", who.CanWrite, wanted, body)
		}
		if who.Env != "develop" {
			t.Fatalf("env = %q, wanted develop", who.Env)
		}
	}
}
