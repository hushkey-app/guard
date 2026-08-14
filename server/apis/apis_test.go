package apis_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/server/apis"
	"github.com/hushkey-app/guard/server/apis/contract"
	apistore "github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// server registers the generated table exactly the way main.go does, so these
// tests exercise the real paths, the real roles and the real decoding rather
// than a hand-wired mux that could drift from production.
func server(t *testing.T, token string) (*telemetry.Store, *httptest.Server) {
	t.Helper()
	store := telemetry.NewStore(100)
	t.Cleanup(func() { store.Close() })
	apistore.Use(store)

	mux := http.NewServeMux()
	api.Register(mux, api.Config{
		Authorize: func(r *http.Request, roles []string) error {
			if token == "" {
				return nil
			}
			if r.Header.Get("Authorization") != "Bearer "+token {
				return api.Unauthorized("a valid bearer token is required")
			}
			return nil
		},
	}, apis.FsApiRoutes()...)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return store, srv
}

func TestHealth(t *testing.T) {
	store, srv := server(t, "")
	if err := store.Add(telemetry.Event{Signal: "logs", Service: "api", Message: "hello"}); err != nil {
		t.Fatal(err)
	}

	var body contract.Health
	if code := get(t, srv.URL+"/healthz", &body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if body.Status != "ok" || body.Events != 1 {
		t.Fatalf("health = %#v", body)
	}
}

// The store is closed underneath: a process that is up but cannot read its
// database is not healthy, and answering 200 from memory is how an outage stays
// invisible until someone opens the dashboard.
func TestHealthFailsWhenTheStoreIsGone(t *testing.T) {
	store, srv := server(t, "")
	store.Close()

	response, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
}

func TestReadEndpoints(t *testing.T) {
	store, srv := server(t, "")
	if err := store.Add(telemetry.Event{Signal: "metrics", Service: "api", Name: "requests", Value: float64Ptr(3)}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/healthz", "/api/summary", "/api/events", "/api/events/1", "/api/logs",
		"/api/facets", "/api/metrics/series?name=requests", "/api/settings",
	} {
		response, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body) //nolint:errcheck
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, response.StatusCode)
		}
	}
}

// The query struct is decoded from the URL and validated before the handler
// runs, so a nonsense filter is a 400 with the offending field named — not a
// clamped value and a 200 nobody asked for.
func TestQueryValidation(t *testing.T) {
	_, srv := server(t, "")
	for _, tc := range []struct{ query, want string }{
		{"offset=-1", "offset"},
		{"limit=-5", "limit"},
		{"limit=abc", "limit"},
		{"from=yesterday", "from"},
	} {
		response, err := http.Get(srv.URL + "/api/events?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		json.NewDecoder(response.Body).Decode(&body) //nolint:errcheck
		response.Body.Close()

		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400", tc.query, response.StatusCode)
			continue
		}
		if !bytes.Contains([]byte(body["error"]+body["field"]), []byte(tc.want)) {
			t.Errorf("GET ?%s said %v, expected it to name %q", tc.query, body, tc.want)
		}
	}
}

// Roles are declared by the endpoint and interpreted by the application. The
// framework only carries the strings across.
func TestRolesAreEnforcedByTheApplication(t *testing.T) {
	store, srv := server(t, "secret")
	payload := []byte(`{"service":"worker","severity":"INFO","message":"job complete"}`)

	response, err := http.Post(srv.URL+"/api/logs", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a token = %d, want 401", response.StatusCode)
	}

	if code := post(t, srv.URL+"/api/logs", payload, "secret"); code != http.StatusAccepted {
		t.Fatalf("with the token = %d, want 202", code)
	}
	if got, err := store.Query(telemetry.Filter{Signal: "logs", Limit: 10}); err != nil || len(got) != 1 || got[0].Message != "job complete" {
		t.Fatalf("logs = %#v", got)
	}
}

// A body that fails Validate never reaches the handler, so nothing is stored.
func TestBodyValidation(t *testing.T) {
	store, srv := server(t, "")
	if code := post(t, srv.URL+"/api/logs", []byte(`{"service":"worker"}`), ""); code != http.StatusBadRequest {
		t.Fatalf("empty message = %d, want 400", code)
	}
	if got, _ := store.Query(telemetry.Filter{Signal: "logs", Limit: 10}); len(got) != 0 {
		t.Fatalf("a rejected log was stored anyway: %#v", got)
	}
}

func TestSettingsRequireTheTokenAndPersist(t *testing.T) {
	store, srv := server(t, "secret")
	payload := []byte(`{"retention_hours":72,"max_events":5000}`)

	if code := put(t, srv.URL+"/api/settings", payload, ""); code != http.StatusUnauthorized {
		t.Fatalf("without a token = %d, want 401", code)
	}
	if code := put(t, srv.URL+"/api/settings", payload, "secret"); code != http.StatusOK {
		t.Fatalf("with the token = %d, want 200", code)
	}
	settings, err := store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.RetentionHours != 72 || settings.MaxEvents != 5000 {
		t.Fatalf("settings = %#v", settings)
	}
}

// ---------------------------------------------------------------------------

func get(t *testing.T, url string, into any) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if into != nil {
		json.NewDecoder(response.Body).Decode(into) //nolint:errcheck
	}
	return response.StatusCode
}

func send(t *testing.T, method, url string, payload []byte, token string) int {
	t.Helper()
	request, _ := http.NewRequest(method, url, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func post(t *testing.T, url string, payload []byte, token string) int {
	return send(t, http.MethodPost, url, payload, token)
}

func put(t *testing.T, url string, payload []byte, token string) int {
	return send(t, http.MethodPut, url, payload, token)
}

func float64Ptr(v float64) *float64 { return &v }

// The provider endpoints take a node id and read the instance off the link,
// exactly as running a command takes an action id and reads its machine. A
// caller cannot name an instance, so a caller cannot aim a power switch at a
// box that is not the one on the row — and a machine with no link has nothing
// to aim at, which is a refusal rather than a guess.
func TestProviderEndpointsNeedALinkedMachine(t *testing.T) {
	store, srv := server(t, "secret")
	node, err := store.SaveNode(telemetry.Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"node_id":` + itoa(node.ID) + `,"action":"halt"}`)

	if code := post(t, srv.URL+"/api/cluster/provider/power", payload, ""); code != http.StatusUnauthorized {
		t.Fatalf("without a token = %d, want 401", code)
	}
	if code := post(t, srv.URL+"/api/cluster/provider/power", payload, "secret"); code != http.StatusBadRequest {
		t.Fatalf("an unlinked machine = %d, want 400", code)
	}
	// And the action is a closed list, checked before anything is dialled.
	bad := []byte(`{"node_id":` + itoa(node.ID) + `,"action":"destroy"}`)
	if code := post(t, srv.URL+"/api/cluster/provider/power", bad, "secret"); code != http.StatusBadRequest {
		t.Fatalf("an invented action = %d, want 400", code)
	}
}
