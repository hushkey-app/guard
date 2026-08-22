package telemetry

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// A busy guard writes from a dozen places at once — the prober, the host
// sampler, the scheduler, the deploy streamer, ingest — and every one of them
// used to be able to come back with `database is locked (5)`.
//
// The cause was never the load. It was that a `BEGIN` which reads and then
// writes asks SQLite to upgrade a lock it did not take, and SQLite refuses that
// instantly: the busy handler is not consulted, so `busy_timeout` never had a
// chance to help. This test is that shape — concurrent writers, each doing a
// read-then-write transaction, against a file rather than memory — and it
// fails on the old configuration within a few iterations.
func TestConcurrentWritersNeverSeeADatabaseLocked(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "guard.db"), Settings{RetentionHours: 24, MaxEvents: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	const machines = 6
	nodes := make([]Node, 0, machines)
	actions := make([]model.NodeAction, 0, machines)
	for i := range machines {
		node, err := store.SaveNode(Node{Name: fmt.Sprintf("VPS-%d", i), URL: "http://127.0.0.1:9/health", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "dump", Command: "true"}})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
		actions = append(actions, saved[0])
	}

	const rounds = 40
	var wg sync.WaitGroup
	errs := make(chan error, machines*rounds*4)
	for i := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				// The scheduler's write: a transaction that reads the action
				// and then updates it.
				errs <- store.RecordRun(actions[i].ID, model.Run{
					RanAt: time.Now().UTC(), ExitCode: 0, Output: "ok", NodeID: nodes[i].ID,
				})
				// The prober's write, plus the rollup it does inside.
				errs <- store.RecordCheck(nodes[i].ID, Check{OK: true, StatusCode: 200, LatencyMS: 3})
				// Ingest, which batches through its own writer.
				errs <- store.Add(Event{
					Signal: "log", Timestamp: time.Now().UTC(), Service: "svc",
					Message: fmt.Sprintf("round %d", r), Severity: "INFO",
				})
				// The shape that used to fail outright: a transaction that
				// reads and then writes. A deferred BEGIN takes a read
				// snapshot at the SELECT, and the DELETE then asks to upgrade
				// it — which SQLite refuses without waiting the moment anybody
				// else has committed in between. Sign-in does exactly this,
				// once per callback.
				state := fmt.Sprintf("state-%d-%d", i, r)
				if err := store.StartLogin(model.LoginState{
					State: state, Provider: "google", Nonce: "n", Redirect: "http://localhost/cb",
					ExpiresAt: time.Now().UTC().Add(time.Minute),
				}); err != nil {
					errs <- err
				}
				if _, err := store.ClaimLogin(state); err != nil {
					errs <- err
				}
				// And a reader in the middle of all of it: in WAL a read must
				// never be what makes a write fail.
				if _, err := store.Nodes(); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	locked := 0
	var first error
	for err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
		}
		if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
			locked++
		}
	}
	if locked > 0 {
		t.Fatalf("%d writes came back locked; first error: %v", locked, first)
	}
	if first != nil {
		t.Fatalf("a write failed for another reason: %v", first)
	}
}
