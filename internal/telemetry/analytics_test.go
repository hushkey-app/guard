package telemetry

import (
	"path/filepath"
	"testing"
)

// analyticsTables is the schema this package promises the rest of analytics.
// Naming them here rather than asking sqlite_master for `analytics_%` is
// deliberate: a table dropped by a future edit would still pass a test that
// only counted what it found.
var analyticsTables = []string{
	"analytics_events",
	"analytics_rollup",
	"analytics_seen",
	"analytics_sources",
	"analytics_actions",
	"analytics_path_rules",
}

func hasTable(t *testing.T, store *Store, name string) bool {
	t.Helper()
	var found string
	err := store.rdb.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err != nil {
		return false
	}
	return found == name
}

func TestAnalyticsSchemaIsCreatedWithTheStore(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	for _, table := range analyticsTables {
		if !hasTable(t, store, table) {
			t.Fatalf("%s is missing from a fresh store", table)
		}
	}
}

// A migration that is not a no-op the second time is a migration that loses
// somebody's numbers on the next restart — so the test opens the same file
// twice with rows already in it.
func TestAnalyticsSchemaSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	store, err := Open(path, Settings{RetentionHours: 24, MaxEvents: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO analytics_rollup(day, path, action, events, sessions) VALUES(?, ?, ?, ?, ?)`, 20324, "/pricing", "page_view", 7, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO analytics_path_rules(pattern, replacement, position) VALUES(?, ?, ?)`, "/users/*", "/users/:id", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, Settings{RetentionHours: 24, MaxEvents: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	for _, table := range analyticsTables {
		if !hasTable(t, reopened, table) {
			t.Fatalf("%s is missing after reopening", table)
		}
	}
	var events, sessions int64
	if err := reopened.rdb.QueryRow(`SELECT events, sessions FROM analytics_rollup WHERE day = ? AND path = ? AND action = ?`, 20324, "/pricing", "page_view").Scan(&events, &sessions); err != nil {
		t.Fatal(err)
	}
	if events != 7 || sessions != 3 {
		t.Fatalf("the rollup came back as %d events over %d sessions", events, sessions)
	}
	var replacement string
	if err := reopened.rdb.QueryRow(`SELECT replacement FROM analytics_path_rules WHERE pattern = ?`, "/users/*").Scan(&replacement); err != nil {
		t.Fatal(err)
	}
	if replacement != "/users/:id" {
		t.Fatalf("the stored rule came back as %q", replacement)
	}
}

// The rollup is one row per day, path and action, and the seen table is what
// makes its session count exact. Both promises are the primary key rather than
// anything a caller does, so the schema is where they are checked.
func TestAnalyticsRollupCountsOncePerSession(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	for range 2 {
		if _, err := store.db.Exec(`INSERT INTO analytics_rollup(day, path, action, events, sessions) VALUES(?, ?, ?, 1, 1)
  ON CONFLICT(day, path, action) DO UPDATE SET events = events + 1`, 20324, "/pricing", "signup_click"); err != nil {
			t.Fatal(err)
		}
	}
	var events int64
	if err := store.rdb.QueryRow(`SELECT events FROM analytics_rollup`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("two events on one path landed as %d rows' worth", events)
	}

	// The second insert changes nothing, which is exactly how the writer knows
	// this session had already done this today.
	result, err := store.db.Exec(`INSERT OR IGNORE INTO analytics_seen(day, path, action, session) VALUES(?, ?, ?, ?)`, 20324, "/pricing", "signup_click", "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("the first sighting of a session changed %d rows", changed)
	}
	result, err = store.db.Exec(`INSERT OR IGNORE INTO analytics_seen(day, path, action, session) VALUES(?, ?, ?, ?)`, 20324, "/pricing", "signup_click", "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 0 {
		t.Fatalf("the second sighting of the same session changed %d rows", changed)
	}
}

// A duplicate pattern is a rule that can never match, because the first one
// always wins. Refusing it in the schema is what stops it being typed.
func TestAnalyticsPathRulePatternsAreUnique(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	if _, err := store.db.Exec(`INSERT INTO analytics_path_rules(pattern, replacement, position) VALUES(?, ?, 0)`, "/users/*", "/users/:id"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO analytics_path_rules(pattern, replacement, position) VALUES(?, ?, 1)`, "/users/*", "/users/:other"); err == nil {
		t.Fatal("a second rule with the same pattern was stored")
	}
}
