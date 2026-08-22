package vaultproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// vault is a stand-in for guard-vault: it answers only what the real one
// answers, and only to a bearer token, so a test that gets a 200 out of it
// proves the header arrived.
func vaultFor(t *testing.T, key string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	mux := http.NewServeMux()
	answer := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "this endpoint needs a secrets key"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": map[string]string{"REDIS_URL": "redis://x"}})
	}
	mux.HandleFunc("GET /v1/secrets", answer)
	mux.HandleFunc("GET /v1/secrets/{key}", answer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &seen
}

func guardWith(t *testing.T, upstream string, enabled bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	if err := Register(mux, Config{Enabled: enabled, Upstream: upstream}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, url, auth string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

// The point of the second door: the same key, the same answer, a different
// port.
func TestTheSameKeyAnswersOnGuardsPort(t *testing.T) {
	vault, seen := vaultFor(t, "gsk_test")
	guard := guardWith(t, strings.TrimPrefix(vault.URL, "http://"), true)

	code, body := get(t, guard.URL+"/v1/secrets", "Bearer gsk_test")
	if code != http.StatusOK || !strings.Contains(body, "REDIS_URL") {
		t.Fatalf("%d %s", code, body)
	}
	code, _ = get(t, guard.URL+"/v1/secrets/REDIS_URL", "Bearer gsk_test")
	if code != http.StatusOK {
		t.Fatalf("one key answered %d", code)
	}
	if len(*seen) != 2 || !strings.HasSuffix((*seen)[0], "auth=Bearer gsk_test") {
		t.Fatalf("the vault saw %v", *seen)
	}
}

// The rule the vault keeps, kept here too. GUARD_TOKEN opens every write
// endpoint in guard's API; it must not open this one, and it does not because
// guard never inspects the header — it forwards it and the vault decides.
func TestGuardsOwnTokenDoesNotOpenThisDoor(t *testing.T) {
	vault, _ := vaultFor(t, "gsk_test")
	guard := guardWith(t, strings.TrimPrefix(vault.URL, "http://"), true)

	for _, auth := range []string{"", "Bearer guard-token", "Bearer gsk_wrong"} {
		code, _ := get(t, guard.URL+"/v1/secrets", auth)
		if code != http.StatusUnauthorized {
			t.Fatalf("%q answered %d, want 401", auth, code)
		}
	}
}

// Off is a 404 rather than a 401 or an empty 200: the route is not there at
// all, which is what an instance that never turned this on should look like
// from outside.
func TestOffMeansTheRouteDoesNotExist(t *testing.T) {
	vault, seen := vaultFor(t, "gsk_test")
	guard := guardWith(t, strings.TrimPrefix(vault.URL, "http://"), false)

	code, _ := get(t, guard.URL+"/v1/secrets", "Bearer gsk_test")
	if code != http.StatusNotFound {
		t.Fatalf("a disabled proxy answered %d", code)
	}
	if len(*seen) != 0 {
		t.Fatalf("the vault was called anyway: %v", *seen)
	}
}

// The vault is a separate process on its own restart schedule, so it being
// down is ordinary rather than exceptional — and the answer has to say which
// of the two things is wrong, since the caller cannot see either.
func TestAVaultThatIsNotAnsweringSaysSo(t *testing.T) {
	// Port 1 is reserved and nothing listens there.
	guard := guardWith(t, "127.0.0.1:1", true)
	code, body := get(t, guard.URL+"/v1/secrets", "Bearer gsk_test")
	if code != http.StatusBadGateway {
		t.Fatalf("answered %d, want 502", code)
	}
	if !strings.Contains(body, "guard-vault is not answering") {
		t.Fatalf("said %q", body)
	}
}

// Two routes, not a prefix: whatever the vault grows later does not appear on
// the public port because somebody forwarded a family of paths.
func TestOnlyTheTwoReadRoutesAreForwarded(t *testing.T) {
	vault, _ := vaultFor(t, "gsk_test")
	guard := guardWith(t, strings.TrimPrefix(vault.URL, "http://"), true)

	for _, path := range []string{"/v1/secrets/a/b", "/v1/logs", "/v1/", "/healthz"} {
		if code, _ := get(t, guard.URL+path, "Bearer gsk_test"); code != http.StatusNotFound {
			t.Fatalf("%s answered %d, want 404", path, code)
		}
	}
	// And a write is not a read, whatever the vault would do with it.
	request, _ := http.NewRequest(http.MethodPost, guard.URL+"/v1/secrets", nil)
	request.Header.Set("Authorization", "Bearer gsk_test")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("a POST reached the vault")
	}
}

// A listen address and a dial address are not the same string, and every
// wildcard form has to become something a dialer can use.
func TestAListenAddressBecomesSomethingDialable(t *testing.T) {
	for _, shape := range []struct{ listen, want string }{
		{"", "127.0.0.1:4319"},
		{":4319", "127.0.0.1:4319"},
		{"0.0.0.0:4319", "127.0.0.1:4319"},
		{"[::]:4319", "127.0.0.1:4319"},
		{"10.19.96.7:4319", "10.19.96.7:4319"},
		{"127.0.0.1:9999", "127.0.0.1:9999"},
		{"vault.internal", "vault.internal:4319"},
	} {
		if got := UpstreamFrom(shape.listen); got != shape.want {
			t.Fatalf("%q became %q, want %q", shape.listen, got, shape.want)
		}
	}
}
