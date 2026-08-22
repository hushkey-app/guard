package telemetry

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
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

// beacon is one post from the tracker, with the parts a counting test does not
// care about filled in.
func beacon(session, path string, names ...string) model.Beacon {
	b := model.Beacon{Session: session, Path: path}
	for _, name := range names {
		b.Events = append(b.Events, model.TrackEvent{Name: name})
	}
	return b
}

func rollupRow(t *testing.T, store *Store, day int64, path, action string) (events, sessions int64) {
	t.Helper()
	err := store.rdb.QueryRow(`SELECT events, sessions FROM analytics_rollup WHERE day = ? AND path = ? AND action = ?`,
		day, path, action).Scan(&events, &sessions)
	if err != nil {
		t.Fatalf("no rollup row for day %d, %s, %s: %v", day, path, action, err)
	}
	return events, sessions
}

// The session count is the number this feature is judged on: a conversion rate
// whose denominator drifts is worse than no rate at all. So the test drives the
// three ways it could drift — the same session twice, two sessions, and the
// same session on the next day.
func TestAddAnalyticsCountsSessionsExactly(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for range 2 {
		if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), monday); err != nil {
			t.Fatal(err)
		}
	}
	events, sessions := rollupRow(t, store, epochDay(monday), "/pricing", "signup_click")
	if events != 2 || sessions != 1 {
		t.Fatalf("one session pressing twice counted %d events over %d sessions", events, sessions)
	}

	if err := store.addAnalyticsAt(beacon("b2", "/pricing", "signup_click"), monday); err != nil {
		t.Fatal(err)
	}
	events, sessions = rollupRow(t, store, epochDay(monday), "/pricing", "signup_click")
	if events != 3 || sessions != 2 {
		t.Fatalf("a second session counted %d events over %d sessions", events, sessions)
	}

	// Tomorrow is a new row, and the same session is a new session in it —
	// which is what makes "versus last month" a sum of days rather than a
	// query over a table that has been purged.
	tuesday := monday.Add(24 * time.Hour)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "signup_click"), tuesday); err != nil {
		t.Fatal(err)
	}
	events, sessions = rollupRow(t, store, epochDay(tuesday), "/pricing", "signup_click")
	if events != 1 || sessions != 1 {
		t.Fatalf("the next day counted %d events over %d sessions", events, sessions)
	}
	if events, sessions = rollupRow(t, store, epochDay(monday), "/pricing", "signup_click"); events != 3 || sessions != 2 {
		t.Fatalf("yesterday moved to %d events over %d sessions", events, sessions)
	}
}

// A path is a group, so the two spellings of one page have to arrive as one
// row — and a different page has to stay a different one.
func TestAddAnalyticsGroupsByNormalisedPath(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/Pricing/?utm_source=hn", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("b2", "/pricing#plans", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), "/pricing", actionPageView); events != 2 || sessions != 2 {
		t.Fatalf("two spellings of one page counted %d events over %d sessions", events, sessions)
	}
	var paths int
	if err := store.rdb.QueryRow(`SELECT COUNT(DISTINCT path) FROM analytics_rollup`).Scan(&paths); err != nil {
		t.Fatal(err)
	}
	if paths != 1 {
		t.Fatalf("one page landed as %d paths", paths)
	}
}

// The raw row is the feed somebody instrumenting a page reads that afternoon,
// so it carries both clocks and the props as they were sent.
func TestAddAnalyticsStoresTheRawRow(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	browser := at.Add(-3 * time.Second)
	b := model.Beacon{Session: "a1", Path: "/pricing", Events: []model.TrackEvent{
		{Name: "signup_click", At: browser.UnixMilli(), Props: map[string]string{"plan": "team"}},
		{Name: "page_view"},
	}}
	if err := store.addAnalyticsAt(b, at); err != nil {
		t.Fatal(err)
	}
	var receivedNS, timestampNS int64
	var props string
	err := store.rdb.QueryRow(`SELECT received_at_ns, timestamp_ns, props_json FROM analytics_events WHERE action = ?`, "signup_click").
		Scan(&receivedNS, &timestampNS, &props)
	if err != nil {
		t.Fatal(err)
	}
	if receivedNS != at.UnixNano() {
		t.Fatalf("the row was received at %d rather than %d", receivedNS, at.UnixNano())
	}
	if timestampNS != browser.UnixMilli()*int64(time.Millisecond) {
		t.Fatalf("the browser's clock came back as %d", timestampNS)
	}
	if props != `{"plan":"team"}` {
		t.Fatalf("the props came back as %s", props)
	}
	// An event with no clock of its own is not a row with no time on it: the
	// feed is read in order, and a zero would sort it before everything.
	if err := store.rdb.QueryRow(`SELECT timestamp_ns FROM analytics_events WHERE action = ?`, actionPageView).Scan(&timestampNS); err != nil {
		t.Fatal(err)
	}
	if timestampNS != at.UnixNano() {
		t.Fatalf("an event with no timestamp landed at %d", timestampNS)
	}
	// Nothing was written but the two rows: no props is an empty object, not
	// the JSON scalar null every reader would then have to special-case.
	if err := store.rdb.QueryRow(`SELECT props_json FROM analytics_events WHERE action = ?`, actionPageView).Scan(&props); err != nil {
		t.Fatal(err)
	}
	if props != "{}" {
		t.Fatalf("an event with no props came back as %s", props)
	}
}

// Names are discovered from what arrives, because a tool that wants them
// declared first is a tool people give up on mid-instrumentation. page_view is
// the exception: it is the Views column and can never be a column of its own.
func TestAddAnalyticsDiscoversActionNames(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	first := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), first); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("b2", "/pricing", "signup_click"), later); err != nil {
		t.Fatal(err)
	}

	var names []string
	rows, err := store.rdb.Query(`SELECT name FROM analytics_actions ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 1 || names[0] != "signup_click" {
		t.Fatalf("discovery found %v", names)
	}
	var firstSeen, lastSeen int64
	if err := store.rdb.QueryRow(`SELECT first_seen_ns, last_seen_ns FROM analytics_actions WHERE name = ?`, "signup_click").Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if firstSeen != first.UnixNano() || lastSeen != later.UnixNano() {
		t.Fatalf("the name was first seen at %d and last at %d", firstSeen, lastSeen)
	}
}

// A beacon that fails the edge is refused whole, and refused whole means
// nothing from it is stored — a batch half-written is a count nobody can
// reason about.
func TestAddAnalyticsRefusesTheWholeBeacon(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	b := beacon("a1", "/pricing", "page_view", "Signup_Click")
	if err := store.AddAnalytics(b); err == nil {
		t.Fatal("a beacon carrying a name that cannot be a column was stored")
	}
	var raw, rolled int
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_events`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_rollup`).Scan(&rolled); err != nil {
		t.Fatal(err)
	}
	if raw != 0 || rolled != 0 {
		t.Fatalf("the refused beacon left %d raw rows and %d rollup rows", raw, rolled)
	}
}
