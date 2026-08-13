package cluster

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

// fake is the two methods the prober needs. Declaring the interface in the
// prober's package is what lets this test exist without SQLite.
type fake struct {
	mu     sync.Mutex
	nodes  []model.Node
	checks map[int64][]model.Check
}

func (f *fake) Nodes() ([]model.Node, error) { return f.nodes, nil }

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
