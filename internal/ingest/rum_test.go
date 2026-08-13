package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mirairoad/guard/internal/telemetry"
)

func browserServer(t *testing.T, config Browser) (*telemetry.Store, *httptest.Server) {
	t.Helper()
	store := telemetry.NewStore(1000)
	t.Cleanup(func() { store.Close() })
	mux := http.NewServeMux()
	Handler{Store: store}.RegisterBrowser(mux, config)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return store, server
}

// A span claiming to be from the api service, posted by a stranger.
const impostor = `{"resourceSpans":[{"resource":{"attributes":[
{"key":"service.name","value":{"stringValue":"api"}},
{"key":"service.instance.id","value":{"stringValue":"api-1"}}]},
"scopeSpans":[{"spans":[{"traceId":"AAAAAAAAAAAAAAAAAAAAAQ==","spanId":"AAAAAAAAAAE=",
"name":"document load","kind":1,"startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`

func post(t *testing.T, url, origin, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

// An unauthenticated write endpoint that appears by default is a hole somebody
// finds before you do.
func TestBrowserIntakeIsOffUntilOriginsAreNamed(t *testing.T) {
	if (Browser{}).Enabled() {
		t.Fatal("a zero configuration is enabled")
	}
	_, server := browserServer(t, Browser{})
	if response := post(t, server.URL+"/v1/rum/traces", "https://app.example.com", impostor); response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — nothing should be mounted", response.StatusCode)
	}
}

// The identity is assigned, never accepted. Without this a visitor writes to
// the dashboard of whatever service they care to name.
func TestBrowserTelemetryCannotClaimAnotherService(t *testing.T) {
	store, server := browserServer(t, Browser{
		Origins: []string{"https://app.example.com"}, Service: "browser", Instance: "v1.2.3",
	})
	if response := post(t, server.URL+"/v1/rum/traces", "https://app.example.com", impostor); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	events, err := store.Query(telemetry.Filter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("%d events stored", len(events))
	}
	if events[0].Service != "browser" || events[0].Instance != "v1.2.3" {
		t.Fatalf("stored as %s/%s — the payload's claim was honoured", events[0].Service, events[0].Instance)
	}
	// Marked, so a view can include or exclude the public half deliberately.
	if events[0].Attributes["telemetry.source"] != "browser" {
		t.Errorf("attributes = %v", events[0].Attributes)
	}
}

func TestBrowserOriginRules(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"https://app.example.com"}})

	for _, tc := range []struct {
		name, origin string
		want         int
	}{
		{"the allowed origin", "https://app.example.com", http.StatusOK},
		{"a different origin", "https://evil.example.com", http.StatusForbidden},
		{"a lookalike", "https://app.example.com.evil.com", http.StatusForbidden},
		// A relay posting server-to-server sends no Origin, and is allowed:
		// whatever fronts guard already made that decision.
		{"no origin at all", "", http.StatusOK},
	} {
		response := post(t, server.URL+"/v1/rum/traces", tc.origin, impostor)
		if response.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, response.StatusCode, tc.want)
		}
		if tc.want == http.StatusOK && tc.origin != "" {
			if got := response.Header.Get("Access-Control-Allow-Origin"); got != tc.origin {
				t.Errorf("%s: allow-origin = %q", tc.name, got)
			}
		}
	}

	// Credentials are never allowed: this endpoint neither reads cookies nor
	// wants them, and wildcard-plus-credentials is the classic hole.
	response := post(t, server.URL+"/v1/rum/traces", "https://app.example.com", impostor)
	if response.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("credentials are allowed on the public intake")
	}
}

func TestBrowserPreflight(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"https://app.example.com"}})
	request, _ := http.NewRequest(http.MethodOptions, server.URL+"/v1/rum/traces", nil)
	request.Header.Set("Origin", "https://app.example.com")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight = %d", response.StatusCode)
	}
	if !strings.Contains(response.Header.Get("Access-Control-Allow-Methods"), "POST") {
		t.Error("preflight does not allow POST")
	}
}

func TestBrowserRateLimit(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"*"}, PerMinute: 3})

	var limited bool
	for range 5 {
		if post(t, server.URL+"/v1/rum/traces", "https://anything.example.com", impostor).StatusCode == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("five requests against a budget of three were all accepted")
	}
}

// A browser batch is a few spans. The collector's 16MB budget is sized for a
// gateway forwarding thousands, and is not the right budget for a stranger.
func TestBrowserBodyLimit(t *testing.T) {
	_, server := browserServer(t, Browser{Origins: []string{"*"}})
	huge := `{"resourceSpans":[{"resource":{"attributes":[{"key":"filler","value":{"stringValue":"` +
		strings.Repeat("x", maxBrowserBody+1024) + `"}}]}}]}`

	response := post(t, server.URL+"/v1/rum/traces", "https://app.example.com", huge)
	if response.StatusCode == http.StatusOK {
		t.Fatalf("a %d byte body was accepted", len(huge))
	}
}

func TestRateLimiterRefills(t *testing.T) {
	limiter := &rateLimiter{perMinute: 60, seen: map[string]*bucket{}}
	for range 60 {
		if !limiter.allow("10.0.0.1") {
			t.Fatal("the budget ran out early")
		}
	}
	if limiter.allow("10.0.0.1") {
		t.Fatal("the budget did not run out")
	}
	// A different address has its own budget.
	if !limiter.allow("10.0.0.2") {
		t.Fatal("one address exhausted another's budget")
	}
}
