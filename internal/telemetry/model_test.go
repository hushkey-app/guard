package telemetry

import (
	"testing"
	"time"
)

func TestStoreBoundsFiltersAndSummarizes(t *testing.T) {
	store := NewStore(3)
	store.Add(
		Event{Signal: "logs", Service: "api", Instance: "a", Severity: "INFO", Message: "started"},
		Event{Signal: "logs", Service: "api", Instance: "a", Severity: "ERROR", Message: "failed checkout"},
		Event{Signal: "traces", Service: "api", Instance: "a", Name: "POST /orders"},
		Event{Signal: "metrics", Service: "worker", Instance: "b", Name: "queue.depth"},
	)

	got := store.Query(Filter{Limit: 100})
	if len(got) != 3 {
		t.Fatalf("stored events = %d, want 3", len(got))
	}
	if got[0].Signal != "metrics" || got[2].Message != "failed checkout" {
		t.Fatalf("unexpected newest-first bounded events: %#v", got)
	}
	filtered := store.Query(Filter{Signal: "logs", Query: "CHECKOUT", Limit: 10})
	if len(filtered) != 1 || filtered[0].Severity != "ERROR" {
		t.Fatalf("filtered events = %#v", filtered)
	}

	summary := store.Snapshot()
	if summary.Stored != 3 || summary.Received != 4 || summary.Logs != 1 || summary.Errors != 1 || summary.Spans != 1 || summary.Metrics != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.Instances) != 2 || time.Since(summary.Instances[0].LastSeen) > time.Second {
		t.Fatalf("unexpected instances: %#v", summary.Instances)
	}
}
