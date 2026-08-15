package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// fake is the two methods the prober needs. Declaring the interface in the
// prober's package is what lets this test exist without SQLite.
type fake struct {
	mu     sync.Mutex
	nodes  []model.Node
	checks map[int64][]model.Check
}

func (f *fake) Nodes() ([]model.Node, error) { return f.nodes, nil }

func (f *fake) NodesForProbe() ([]model.Node, error) { return f.nodes, nil }

func (f *fake) Node(id int64) (model.Node, error) {
	for _, node := range f.nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return model.Node{}, http.ErrNoLocation
}

func (f *fake) RecordCheck(nodeID int64, check model.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checks == nil {
		f.checks = map[int64][]model.Check{}
	}
	f.checks[nodeID] = append(f.checks[nodeID], check)
	return nil
}

func (f *fake) recorded(nodeID int64) []model.Check {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Check(nil), f.checks[nodeID]...)
}

// Anything from 200 to 399 is up. A health endpoint that answers 204, or
// redirects to one that does, is a healthy endpoint.
func TestCheckReadsTheStatusCode(t *testing.T) {
	for _, tc := range []struct {
		code int
		up   bool
	}{{200, true}, {204, true}, {301, true}, {400, false}, {404, false}, {500, false}, {503, false}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.code)
		}))
		check := (&Prober{Store: &fake{}}).Check(context.Background(), server.URL)
		server.Close()
		if check.OK != tc.up {
			t.Errorf("%d: ok = %v, want %v", tc.code, check.OK, tc.up)
		}
		if check.StatusCode != tc.code {
			t.Errorf("%d: status = %d", tc.code, check.StatusCode)
		}
		if !tc.up && check.Error == "" {
			t.Errorf("%d: a failed check with no reason", tc.code)
		}
	}
}

func TestCheckMeasuresLatencyAndTimesOut(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	prober := &Prober{Store: &fake{}, Timeout: 30 * time.Millisecond}
	check := prober.Check(context.Background(), slow.URL)
	if check.OK {
		t.Fatal("a server slower than the timeout was reported up")
	}
	if check.Error != "timed out" {
		t.Errorf("error = %q, want the readable version", check.Error)
	}

	quick := &Prober{Store: &fake{}, Timeout: time.Second}
	if measured := quick.Check(context.Background(), slow.URL); !measured.OK || measured.LatencyMS < 100 {
		t.Errorf("latency = %.0fms for a 150ms handler (ok=%v)", measured.LatencyMS, measured.OK)
	}
}

// The transport's errors name the dialer, the address and the syscall, which is
// three facts too many at 3am when the answer is "nothing is listening".
func TestCheckExplainsTransportFailures(t *testing.T) {
	prober := &Prober{Store: &fake{}, Timeout: time.Second}
	check := prober.Check(context.Background(), "http://127.0.0.1:9/health")
	if check.OK {
		t.Fatal("a closed port was reported up")
	}
	if check.Error != "connection refused — nothing is listening" {
		t.Errorf("error = %q", check.Error)
	}

	// A URL the store would never have accepted still must not panic here.
	if bad := prober.Check(context.Background(), "file:///etc/passwd"); bad.OK || bad.Error == "" {
		t.Errorf("file:// check = %#v", bad)
	}
}

func TestRoundSkipsPausedNodesAndRecordsTheRest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := &fake{nodes: []model.Node{
		{ID: 1, Name: "watched", URL: server.URL, Enabled: true},
		{ID: 2, Name: "paused", URL: server.URL, Enabled: false},
	}}
	(&Prober{Store: store, Timeout: time.Second}).Round(context.Background())

	if got := len(store.recorded(1)); got != 1 {
		t.Errorf("watched node recorded %d checks, want 1", got)
	}
	if got := len(store.recorded(2)); got != 0 {
		t.Errorf("paused node recorded %d checks, want none", got)
	}
}

// iconStore is a fake that also remembers icons — the optional half of the
// store interface.
type iconStore struct {
	fake
	stale bool
	icon  []byte
	kind  string
	saves int
}

func (i *iconStore) IconStale(int64) (bool, error) { return i.stale, nil }

func (i *iconStore) SaveIcon(_ int64, icon []byte, contentType string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.icon, i.kind, i.saves = icon, contentType, i.saves+1
	return nil
}

func TestIconIsFetchedFromHealthyNodes(t *testing.T) {
	const iconBody = "\x00\x00\x01\x00fake-icon-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			w.Header().Set("Content-Type", "image/x-icon")
			w.Write([]byte(iconBody)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// The health URL has a path; the icon is looked for at the origin's root.
	store := &iconStore{fake: fake{nodes: []model.Node{{ID: 1, Name: "n", URL: server.URL + "/api/health", Enabled: true}}}, stale: true}
	(&Prober{Store: store, Timeout: time.Second}).Round(context.Background())
	if string(store.icon) != iconBody || store.kind != "image/x-icon" {
		t.Fatalf("icon = %q (%s)", store.icon, store.kind)
	}

	// A fresh icon is not refetched: most machines have none, and remembering
	// that is what keeps this to one request a day.
	store.stale = false
	store.saves = 0
	(&Prober{Store: store, Timeout: time.Second}).Round(context.Background())
	if store.saves != 0 {
		t.Errorf("a fresh icon was fetched again %d times", store.saves)
	}
}

// Everything that is not an image is recorded as "no icon" rather than stored:
// a health endpoint answering JSON at /favicon.ico is the common case, and an
// unrecorded miss would be retried on every check forever.
func TestIconRejectsWhatIsNotAnImage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"a 404", http.StatusNotFound, "text/html", "not found"},
		{"an html page", http.StatusOK, "text/html", "<html></html>"},
		{"json", http.StatusOK, "application/json", `{"status":"ok"}`},
		{"an empty body", http.StatusOK, "image/x-icon", ""},
		{"something enormous", http.StatusOK, "image/png", strings.Repeat("x", maxIcon+1)},
	} {
		// The health endpoint answers normally — the icon is only ever asked
		// for from a machine that just passed its check.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/favicon.ico" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.Header().Set("Content-Type", tc.contentType)
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body)) //nolint:errcheck
		}))
		store := &iconStore{fake: fake{nodes: []model.Node{{ID: 1, URL: server.URL + "/health", Enabled: true}}}, stale: true}
		(&Prober{Store: store, Timeout: time.Second}).Round(context.Background())
		server.Close()

		if len(store.icon) != 0 {
			t.Errorf("%s was stored as an icon", tc.name)
		}
		if store.saves != 1 {
			t.Errorf("%s: recorded %d attempts, want 1 — an unrecorded miss retries forever", tc.name, store.saves)
		}
	}
}

// A store that does not do icons simply never gets asked for one.
func TestIconIsOptional(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store := &fake{nodes: []model.Node{{ID: 1, URL: server.URL, Enabled: true}}}
	(&Prober{Store: store, Timeout: time.Second}).Round(context.Background())
	if len(store.recorded(1)) != 1 {
		t.Error("the check itself should still have happened")
	}
}

// Each node keeps its own clock: a pass checks what is due and leaves the rest
// alone, so a three-second node and a five-minute one can share a cluster.
func TestRoundChecksOnlyWhatIsDue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	now := time.Now()
	store := &fake{nodes: []model.Node{
		{ID: 1, Name: "never checked", URL: server.URL, Enabled: true, IntervalSeconds: 3},
		{ID: 2, Name: "due", URL: server.URL, Enabled: true, IntervalSeconds: 3, CheckedAt: now.Add(-10 * time.Second)},
		{ID: 3, Name: "not due", URL: server.URL, Enabled: true, IntervalSeconds: 300, CheckedAt: now.Add(-10 * time.Second)},
	}}

	next := (&Prober{Store: store, Timeout: time.Second}).Round(context.Background())

	if len(store.recorded(1)) != 1 {
		t.Error("a node that has never been checked is due immediately")
	}
	if len(store.recorded(2)) != 1 {
		t.Error("a node past its interval was not checked")
	}
	if len(store.recorded(3)) != 0 {
		t.Error("a node inside its interval was checked anyway")
	}
	// The sleep is until the *soonest* node needs attention. Both fast nodes
	// were just checked, so that is three seconds — not the five minutes the
	// slow one has left, and not a fixed tick.
	if next != 3*time.Second {
		t.Errorf("next pass in %s, want 3s", next)
	}
}

// A cluster where nothing has been checked yet still has to schedule itself:
// checking everything once and then sleeping out the idle timer would leave a
// freshly added node unwatched for half a minute.
func TestRoundSchedulesNodesThatHaveNeverBeenChecked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := &fake{nodes: []model.Node{{ID: 1, URL: server.URL, Enabled: true, IntervalSeconds: 5}}}
	next := (&Prober{Store: store, Timeout: time.Second, Interval: 30 * time.Second}).Round(context.Background())
	if next != 5*time.Second {
		t.Errorf("next pass in %s, want the node's own 5s rather than the idle sleep", next)
	}
}

// The sleep must never be zero, or a node whose interval is shorter than its
// own check takes would spin the loop with no gap at all.
func TestRoundAlwaysSleeps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := &fake{nodes: []model.Node{{ID: 1, URL: server.URL, Enabled: true, IntervalSeconds: 1}}}
	prober := &Prober{Store: store, Timeout: time.Second}
	if next := prober.Round(context.Background()); next <= 0 {
		t.Fatalf("next pass in %s", next)
	}

	// And with nothing at all to watch, it does not busy-loop over an empty
	// table either.
	empty := &Prober{Store: &fake{}, Timeout: time.Second, Interval: 30 * time.Second}
	if next := empty.Round(context.Background()); next != 30*time.Second {
		t.Errorf("idle sleep = %s, want the configured 30s", next)
	}
}

// The button asks a different question from the scheduler: not "what is due"
// but "is it back yet".
func TestRoundAllIgnoresTheSchedule(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := &fake{nodes: []model.Node{
		{ID: 1, URL: server.URL, Enabled: true, IntervalSeconds: 3600, CheckedAt: time.Now()},
		{ID: 2, URL: server.URL, Enabled: false, IntervalSeconds: 1},
	}}
	(&Prober{Store: store, Timeout: time.Second}).RoundAll(context.Background())

	if len(store.recorded(1)) != 1 {
		t.Error("a node checked a second ago was skipped by the button")
	}
	if len(store.recorded(2)) != 0 {
		t.Error("a paused node was checked")
	}
}

// A round must not take as long as the sum of its timeouts: ten nodes and one
// black hole would put a serial prober minutes behind its own ticker.
func TestRoundChecksNodesConcurrently(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	var nodes []model.Node
	for id := range int64(6) {
		nodes = append(nodes, model.Node{ID: id + 1, Name: "n", URL: slow.URL, Enabled: true})
	}
	store := &fake{nodes: nodes}

	start := time.Now()
	(&Prober{Store: store, Timeout: 2 * time.Second}).Round(context.Background())
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("six 120ms checks took %s — they are running one after another", elapsed)
	}
	for _, node := range nodes {
		if len(store.recorded(node.ID)) != 1 {
			t.Errorf("node %d was not checked", node.ID)
		}
	}
}
