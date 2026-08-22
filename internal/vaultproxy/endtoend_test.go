package vaultproxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/internal/vault"
	"github.com/hushkey-app/guard/internal/vaultproxy"
)

// Both doors, one database, one answer.
//
// The unit tests above put a stand-in behind the proxy, which proves the header
// arrives and nothing else. This one runs the real vault server on the real
// store and asks the same question twice — once on the vault's own port and
// once through guard's — because "the second door is the same door" is the
// entire claim being made, and a stand-in cannot make it.
func TestBothDoorsGiveTheSameAnswer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	t.Setenv("GUARD_SECRET_KEY", "a-test-key-for-both-halves")

	guard, err := telemetry.Open(path, telemetry.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guard.Close() })

	spaces, err := guard.Workspaces()
	if err != nil || len(spaces) == 0 {
		t.Fatalf("workspaces: %+v %v", spaces, err)
	}
	envs, err := guard.Envs(spaces[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var production model.Env
	for _, env := range envs {
		if env.Name == "production" {
			production = env
		}
	}
	if _, err := guard.SaveSecret(model.Secret{EnvID: production.ID, Key: "REDIS_URL", Value: "redis://:pw@10.19.96.6:6379"}); err != nil {
		t.Fatal(err)
	}
	key, err := guard.CreateAPIKey(model.APIKey{EnvID: production.ID, Name: "pack"})
	if err != nil {
		t.Fatal(err)
	}

	store, err := vault.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// The vault's own port.
	vaultMux := http.NewServeMux()
	(&vault.Server{Store: store}).Register(vaultMux)
	direct := httptest.NewServer(vaultMux)
	t.Cleanup(direct.Close)

	// Guard's port, forwarding to it.
	guardMux := http.NewServeMux()
	if err := vaultproxy.Register(guardMux, vaultproxy.Config{
		Enabled: true, Upstream: strings.TrimPrefix(direct.URL, "http://"),
	}); err != nil {
		t.Fatal(err)
	}
	proxied := httptest.NewServer(guardMux)
	t.Cleanup(proxied.Close)

	read := func(base, token string) (int, string) {
		request, _ := http.NewRequest(http.MethodGet, base+"/v1/secrets/REDIS_URL", nil)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}

	directCode, directBody := read(direct.URL, key.Token)
	proxyCode, proxyBody := read(proxied.URL, key.Token)
	if directCode != http.StatusOK || proxyCode != http.StatusOK {
		t.Fatalf("direct %d %s / proxied %d %s", directCode, directBody, proxyCode, proxyBody)
	}
	var one, two model.Secret
	if err := json.Unmarshal([]byte(directBody), &one); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(proxyBody), &two); err != nil {
		t.Fatal(err)
	}
	if one.Value != "redis://:pw@10.19.96.6:6379" || one.Value != two.Value {
		t.Fatalf("direct %q, proxied %q", one.Value, two.Value)
	}

	// And the refusals match too: a door that is laxer than the other is worse
	// than no second door.
	for _, token := range []string{"", "not-a-key", key.Token + "x"} {
		directCode, _ := read(direct.URL, token)
		proxyCode, _ := read(proxied.URL, token)
		if directCode != proxyCode {
			t.Fatalf("token %q: the vault said %d and the proxy said %d", token, directCode, proxyCode)
		}
		if proxyCode == http.StatusOK {
			t.Fatalf("token %q was accepted", token)
		}
	}
}
