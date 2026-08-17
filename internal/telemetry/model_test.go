package telemetry

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreBoundsFiltersAndSummarizes(t *testing.T) {
	store := NewStore(3)
	t.Cleanup(func() { store.Close() })
	if err := store.Add(
		Event{Signal: "logs", Service: "api", Instance: "a", Severity: "INFO", Message: "started"},
		Event{Signal: "logs", Service: "api", Instance: "a", Severity: "ERROR", Message: "failed checkout"},
		Event{Signal: "traces", Service: "api", Instance: "a", Name: "POST /orders"},
		Event{Signal: "metrics", Service: "worker", Instance: "b", Name: "queue.depth"},
	); err != nil {
		t.Fatal(err)
	}

	got, err := store.Query(Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("stored events = %d, want 3", len(got))
	}
	if got[0].Signal != "metrics" || got[2].Message != "failed checkout" {
		t.Fatalf("unexpected newest-first bounded events: %#v", got)
	}
	filtered, err := store.Query(Filter{Signal: "logs", Query: "CHECKOUT", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Severity != "ERROR" {
		t.Fatalf("filtered events = %#v", filtered)
	}
	paged, err := store.Query(Filter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(paged) != 1 || paged[0].Signal != "traces" {
		t.Fatalf("second event page = %#v", paged)
	}
	empty, err := store.Query(Filter{Signal: "does-not-exist", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty query = %#v, want non-nil empty slice", empty)
	}

	summary, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Stored != 3 || summary.Received != 4 || summary.Logs != 1 || summary.Errors != 1 || summary.Spans != 1 || summary.Metrics != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(summary.Instances) != 2 || time.Since(summary.Instances[0].LastSeen) > time.Second {
		t.Fatalf("unexpected instances: %#v", summary.Instances)
	}
}

func TestSQLitePersistsFiltersAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	store, err := Open(path, Settings{RetentionHours: 24, MaxEvents: 1000})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Add(
		Event{Signal: "logs", Timestamp: now.Add(-2 * time.Hour), Service: "api", Severity: "INFO", Message: "old"},
		Event{Signal: "logs", Timestamp: now, Service: "api", Severity: "ERROR", Message: "new"},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSettings(Settings{RetentionHours: 48, MaxEvents: 500}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, Settings{RetentionHours: 1, MaxEvents: 100})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	settings, err := reopened.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.RetentionHours != 48 || settings.MaxEvents != 500 || settings.DatabasePath != path {
		t.Fatalf("settings = %#v", settings)
	}
	events, err := reopened.Query(Filter{Signal: "logs", Severity: "ERROR", From: now.Add(-time.Minute), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "new" {
		t.Fatalf("filtered events = %#v", events)
	}
	summary, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Received != 2 || summary.Stored != 2 {
		t.Fatalf("persistent summary = %#v", summary)
	}
}

func TestSQLiteBatchesConcurrentWriters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "guard.db"), Settings{RetentionHours: 24, MaxEvents: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	const writers = 64
	start := make(chan struct{})
	errs := make(chan error, writers)
	var group sync.WaitGroup
	group.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer group.Done()
			<-start
			errs <- store.Add(Event{Signal: "logs", Service: "batch-test", Message: "concurrent event"})
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	summary, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Received != writers || summary.Stored != writers {
		t.Fatalf("batched summary = %#v, want %d events", summary, writers)
	}
}
