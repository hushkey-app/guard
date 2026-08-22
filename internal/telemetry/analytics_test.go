package telemetry

import (
	"database/sql"
	"errors"
	"fmt"
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

// analyticsHealth is where the ceilings' counters are read from, because it is
// where the page reads them: a counter nothing on a screen can reach is a
// counter that can rot without a test noticing.
func analyticsHealth(t *testing.T, store *Store) model.AnalyticsHealth {
	t.Helper()
	health, err := store.AnalyticsHealth()
	if err != nil {
		t.Fatal(err)
	}
	return health
}

// The path ceiling is what stops a site with unbounded URLs — a search page, a
// signed link, somebody's crawler — from growing the rollup until SQLite is
// slow. Past it the count is still real; it is filed under the one name that
// says it was not kept by itself.
func TestAddAnalyticsRollsPathsPastTheCeilingIntoOther(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range maxAnalyticsPaths {
		if err := store.addAnalyticsAt(beacon("a1", fmt.Sprintf("/p/%d", i), "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}
	if capped := analyticsHealth(t, store).PathsCapped; capped != 0 {
		t.Fatalf("the paths up to the ceiling capped %d beacons", capped)
	}

	if err := store.addAnalyticsAt(beacon("b2", "/search/one", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("c3", "/search/two", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), pathOther, actionPageView); events != 2 || sessions != 2 {
		t.Fatalf("the two paths past the ceiling counted %d events over %d sessions", events, sessions)
	}
	var rows int
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_rollup WHERE path LIKE '/search/%'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a path past the ceiling kept %d rows of its own", rows)
	}
	var paths int
	if err := store.rdb.QueryRow(`SELECT COUNT(DISTINCT path) FROM analytics_rollup`).Scan(&paths); err != nil {
		t.Fatal(err)
	}
	if paths != maxAnalyticsPaths+1 {
		t.Fatalf("the day holds %d distinct paths", paths)
	}
	if capped := analyticsHealth(t, store).PathsCapped; capped != 2 {
		t.Fatalf("two beacons past the ceiling were counted as %d", capped)
	}

	// The URL that overflowed is still on its raw row, because that is what
	// somebody reads to write the path rule that stops it happening again.
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_events WHERE path = ?`, "/search/one").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the raw feed kept %d rows for the path that overflowed", rows)
	}

	// The ceiling closes the door on new paths; it does not move a page that
	// is already through it, which would be a page losing its numbers halfway
	// through the afternoon the flood started.
	if err := store.addAnalyticsAt(beacon("d4", "/p/0", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), "/p/0", actionPageView); events != 2 || sessions != 2 {
		t.Fatalf("a page already counted moved to %d events over %d sessions", events, sessions)
	}
}

// The other ceiling is answered the other way round: a name past it is refused
// rather than folded into a neighbour, because two teams' events in one column
// is a wrong number nobody can see is wrong.
func TestAddAnalyticsRefusesActionNamesPastTheCeiling(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	// Filled through the door discovery is actually filled through — names
	// arrive in batches, they are never declared.
	for i := 0; i < maxAnalyticsActions; i += model.MaxBeaconEvents {
		b := model.Beacon{Session: "a1", Path: "/pricing"}
		for j := range model.MaxBeaconEvents {
			b.Events = append(b.Events, model.TrackEvent{Name: fmt.Sprintf("act_%d", i+j)})
		}
		if err := store.addAnalyticsAt(b, at); err != nil {
			t.Fatal(err)
		}
	}
	var names int
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_actions`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if names != maxAnalyticsActions {
		t.Fatalf("discovery filled to %d names", names)
	}
	if refused := analyticsHealth(t, store).ActionsRefused; refused != 0 {
		t.Fatalf("filling discovery to the ceiling refused %d names", refused)
	}

	// The beacon carries all three: a name past the ceiling, a name already
	// discovered, and the reserved one. Only the first is refused, and the
	// batch around it still counts — a refusal is not the whole-beacon refusal
	// the edge limits are.
	b := beacon("b2", "/pricing", "one_too_many", "act_0", actionPageView)
	if err := store.addAnalyticsAt(b, at); err != nil {
		t.Fatal(err)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), "/pricing", actionPageView); events != 1 || sessions != 1 {
		t.Fatalf("page_view beside a refused name counted %d events over %d sessions", events, sessions)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), "/pricing", "act_0"); events != 2 || sessions != 2 {
		t.Fatalf("a discovered name beside a refused one counted %d events over %d sessions", events, sessions)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM analytics_actions WHERE name = ?`,
		`SELECT COUNT(*) FROM analytics_rollup WHERE action = ?`,
		`SELECT COUNT(*) FROM analytics_seen WHERE action = ?`,
		`SELECT COUNT(*) FROM analytics_events WHERE action = ?`,
	} {
		var rows int
		if err := store.rdb.QueryRow(query, "one_too_many").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("a refused name left %d rows behind: %s", rows, query)
		}
	}
	if refused := analyticsHealth(t, store).ActionsRefused; refused != 1 {
		t.Fatalf("one name past the ceiling was counted as %d", refused)
	}
}

// rule is a path rule with the id and position the save is going to decide.
func rule(pattern, replacement string) model.PathRule {
	return model.PathRule{Pattern: pattern, Replacement: replacement}
}

func savedRules(t *testing.T, store *Store, rules ...model.PathRule) {
	t.Helper()
	if _, err := store.SavePathRules(rules); err != nil {
		t.Fatal(err)
	}
}

// The point of a rule is that `/users/7` and `/users/8` are one page. The
// rollup is where that has to be true, because the rollup is what outlives the
// raw rows — and the raw row keeps the URL it arrived on, because that is what
// somebody was reading when they wrote the rule.
func TestAnalyticsPathRulesCollapseAPathIntoTheRollup(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	savedRules(t, store, rule("/users/*", "/users/:id"))
	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i, session := range []string{"a1", "b2"} {
		if err := store.addAnalyticsAt(beacon(session, fmt.Sprintf("/users/%d", i), "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}

	if events, sessions := rollupRow(t, store, epochDay(at), "/users/:id", actionPageView); events != 2 || sessions != 2 {
		t.Fatalf("two visits to one collapsed page counted %d events over %d sessions", events, sessions)
	}
	var rows int
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_rollup WHERE path LIKE '/users/_'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a collapsed path kept %d rollup rows of its own", rows)
	}
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_events WHERE path = ?`, "/users/0").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the raw feed kept %d rows for the URL that was collapsed", rows)
	}
}

// Order is the whole configuration: the same two rules in the other order are a
// different answer for `/users/new`, and a page that lets somebody drag them
// has to mean it.
func TestAnalyticsPathRulesTakeTheFirstMatch(t *testing.T) {
	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		rules   []model.PathRule
		counted string
	}{
		{"the page before the id", []model.PathRule{rule("/users/new", "/users/new"), rule("/users/*", "/users/:id")}, "/users/new"},
		{"the id before the page", []model.PathRule{rule("/users/*", "/users/:id"), rule("/users/new", "/users/new")}, "/users/:id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(100)
			t.Cleanup(func() { store.Close() })

			savedRules(t, store, tc.rules...)
			if err := store.addAnalyticsAt(beacon("a1", "/users/new", "page_view"), at); err != nil {
				t.Fatal(err)
			}
			if events, _ := rollupRow(t, store, epochDay(at), tc.counted, actionPageView); events != 1 {
				t.Fatalf("the visit counted %d events under %s", events, tc.counted)
			}
		})
	}
}

// A path no rule matches is its own page. Said as a test because the loop that
// returns early on a match is one edit away from returning the last rule's
// replacement for everything.
func TestAnalyticsPathRulesLeaveAnUnmatchedPathAlone(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	savedRules(t, store, rule("/users/*", "/users/:id"))
	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if events, _ := rollupRow(t, store, epochDay(at), "/pricing", actionPageView); events != 1 {
		t.Fatalf("an unmatched path counted %d events under itself", events)
	}
}

// The honest half of applying rules at ingest, and the sentence the page has to
// carry: a rule shapes what is counted from the moment it is stored, and the
// days already rolled up stay as they were counted.
func TestAnalyticsPathRulesDoNotRewriteHistory(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/users/7", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	savedRules(t, store, rule("/users/*", "/users/:id"))
	if err := store.addAnalyticsAt(beacon("b2", "/users/8", "page_view"), at); err != nil {
		t.Fatal(err)
	}

	if events, sessions := rollupRow(t, store, epochDay(at), "/users/7", actionPageView); events != 1 || sessions != 1 {
		t.Fatalf("the day counted before the rule reads %d events over %d sessions", events, sessions)
	}
	if events, sessions := rollupRow(t, store, epochDay(at), "/users/:id", actionPageView); events != 1 || sessions != 1 {
		t.Fatalf("the visit after the rule counted %d events over %d sessions", events, sessions)
	}
}

// A rule is applied before the ceiling, which is what makes it a fix for the
// flood rather than a tidier way to read one: a thousand user pages behind one
// rule are one path, and nothing is capped.
func TestAnalyticsPathRulesApplyBeforeTheCeiling(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	savedRules(t, store, rule("/users/*", "/users/:id"))
	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range maxAnalyticsPaths + 5 {
		if err := store.addAnalyticsAt(beacon("a1", fmt.Sprintf("/users/%d", i), "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}

	var paths int
	if err := store.rdb.QueryRow(`SELECT COUNT(DISTINCT path) FROM analytics_rollup`).Scan(&paths); err != nil {
		t.Fatal(err)
	}
	if paths != 1 {
		t.Fatalf("a thousand URLs behind one rule became %d paths", paths)
	}
	if capped := analyticsHealth(t, store).PathsCapped; capped != 0 {
		t.Fatalf("the rule that stops the flood still capped %d beacons", capped)
	}
}

// The three ways a rule can be refused, all of them before anything is written.
// A rule stored and silently unable to fire is the failure this feature would
// be blamed for, since the number it produces looks exactly like a page nobody
// visited.
func TestAnalyticsPathRulesRefuseWhatCouldNeverFire(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []model.PathRule
	}{
		{"a pattern that will not compile", []model.PathRule{rule("/users/[", "/users/:id")}},
		{"two rules for one pattern", []model.PathRule{rule("/users/*", "/users/:id"), rule("/users/*", "/users/:other")}},
		{"two rules for one pattern, typed in different cases", []model.PathRule{rule("/Users/*", "/users/:id"), rule("/users/*", "/users/:other")}},
		{"a pattern that is not a path", []model.PathRule{rule("users/*", "/users/:id")}},
		{"a rule with nothing to collapse to", []model.PathRule{rule("/users/*", "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(100)
			t.Cleanup(func() { store.Close() })

			if _, err := store.SavePathRules(tc.rules); err == nil {
				t.Fatal("the rule was stored")
			}
			stored, err := store.PathRules()
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 0 {
				t.Fatalf("a refused save left %d rules behind", len(stored))
			}
		})
	}
}

// Saving replaces the list, in the order it was sent, and the halves are stored
// as they will be applied — so what the page shows after a save is what the
// next beacon is going to be counted under.
func TestAnalyticsPathRulesAreReplacedInOrder(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	savedRules(t, store, rule("/users/*", "/users/:id"), rule("/orders/*", "/orders/:id"))
	stored, err := store.SavePathRules([]model.PathRule{rule("/Blog/*", "/Blog/:Slug/")})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("the replaced list holds %d rules", len(stored))
	}
	if stored[0].Pattern != "/blog/*" || stored[0].Replacement != "/blog/:slug" {
		t.Fatalf("the rule was stored as %q → %q", stored[0].Pattern, stored[0].Replacement)
	}
	if stored[0].Position != 0 || stored[0].ID == 0 {
		t.Fatalf("the stored rule is id %d at position %d", stored[0].ID, stored[0].Position)
	}
}

// The preview is the save's own preparation run against sample paths, which is
// the only way the dialog can promise what the press will do. It takes the
// rules rather than reading the stored ones, because it exists to prove one
// that has not been stored yet.
func TestAnalyticsPathRulePreviewProvesARuleBeforeItIsStored(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	candidate := []model.PathRule{rule("/users/new", "/users/new"), rule("/users/*", "/users/:id")}
	preview, err := store.PreviewPathRules(candidate, []string{"/users/7", "/users/new", "/pricing/", "/Users/8?ref=x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/users/:id", "/users/new", "/pricing", "/users/:id"}
	for i, path := range want {
		if preview[i] != path {
			t.Fatalf("the preview made %q of sample %d, not %q", preview[i], i, path)
		}
	}

	stored, err := store.PathRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("the preview stored %d rules", len(stored))
	}
	if _, err := store.PreviewPathRules([]model.PathRule{rule("/users/[", "/users/:id")}, []string{"/users/7"}); err == nil {
		t.Fatal("the preview accepted a pattern the save would refuse")
	}
}

// pathRow finds one line of the grid, because a test that indexed into the
// slice would be asserting the ordering by accident everywhere.
func pathRow(t *testing.T, grid []model.PathRow, path string) model.PathRow {
	t.Helper()
	for _, row := range grid {
		if row.Path == path {
			return row
		}
	}
	t.Fatalf("the grid has no row for %s", path)
	return model.PathRow{}
}

// The grid is the feature, and the rate is the only reason a column is on it:
// the sessions that did the action over the sessions that saw the page. So the
// test builds a page three sessions saw and two of them pressed, and asserts
// the arithmetic rather than the counts it is made of.
func TestAnalyticsPathsRatesAgainstTheSessionsThatSawThePage(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, b := range []model.Beacon{
		beacon("a1", "/pricing", "page_view", "signup_click"),
		beacon("b2", "/pricing", "page_view"),
		beacon("c3", "/pricing", "page_view", "signup_click"),
	} {
		if err := store.addAnalyticsAt(b, at); err != nil {
			t.Fatal(err)
		}
	}

	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	row := pathRow(t, grid, "/pricing")
	if row.Views != 3 || row.Sessions != 3 {
		t.Fatalf("the page was %d views over %d sessions", row.Views, row.Sessions)
	}
	cell := row.Actions["signup_click"]
	if cell.Events != 2 || cell.Sessions != 2 {
		t.Fatalf("the action was %d events over %d sessions", cell.Events, cell.Sessions)
	}
	if cell.Rate != 2.0/3.0 {
		t.Fatalf("two sessions of three converted at %v", cell.Rate)
	}
	// page_view is the Views column. A cell for it would draw every page in the
	// product converting at 100% in a column of its own.
	if _, drawn := row.Actions[actionPageView]; drawn {
		t.Fatal("page_view came back as a column")
	}
}

// The rule this feature would be least forgiven for breaking: a dash, never a
// zero. An action that never happened on a path has no cell at all, so the
// renderer cannot draw 0.0% beside a page that has no button for it.
func TestAnalyticsPathsLeaveANeverSeenActionOut(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), at); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("a1", "/docs", "page_view"), at); err != nil {
		t.Fatal(err)
	}

	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	docs := pathRow(t, grid, "/docs")
	if cell, drawn := docs.Actions["signup_click"]; drawn {
		t.Fatalf("a page with no signup button carries a cell of %d at %v", cell.Events, cell.Rate)
	}
	if len(docs.Actions) != 0 {
		t.Fatalf("a page nobody pressed anything on carries %d cells", len(docs.Actions))
	}
}

// Ordered by views, because the grid is read from the top and the page nobody
// visits is not the one somebody opened it for. Ties break on the path so a
// background refresh cannot reshuffle the rows under a reader.
func TestAnalyticsPathsAreOrderedByViews(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := store.addAnalyticsAt(beacon(fmt.Sprintf("s%d", i), "/", "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("a1", "/docs", "page_view"), at); err != nil {
		t.Fatal(err)
	}

	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/", "/docs", "/pricing"}
	if len(grid) != len(want) {
		t.Fatalf("the grid has %d rows", len(grid))
	}
	for i, path := range want {
		if grid[i].Path != path {
			t.Fatalf("row %d is %s, not %s", i, grid[i].Path, path)
		}
	}
}

// A window is whole UTC days at both ends — the rollup has no finer grain —
// and both ends are included, or the page somebody is watching while they
// instrument it draws nothing.
func TestAnalyticsPathsCountOnlyTheWindow(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	tuesday := monday.Add(24 * time.Hour)
	wednesday := monday.Add(48 * time.Hour)
	for _, day := range []time.Time{monday, tuesday, wednesday} {
		if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), day); err != nil {
			t.Fatal(err)
		}
	}

	grid, err := store.AnalyticsPaths(monday, tuesday)
	if err != nil {
		t.Fatal(err)
	}
	row := pathRow(t, grid, "/pricing")
	// Two days of one returning session is two sessions, which is the unit the
	// rollup is keyed in — and the same unit both halves of the rate use, so
	// the visit that came back cannot move the percentage.
	if row.Views != 2 || row.Sessions != 2 {
		t.Fatalf("two days of the window came back as %d views over %d sessions", row.Views, row.Sessions)
	}
	if cell := row.Actions["signup_click"]; cell.Events != 2 || cell.Rate != 1 {
		t.Fatalf("the action came back as %d events at %v", cell.Events, cell.Rate)
	}
}

// A path can carry an action and no page view — a tracker firing on a route the
// page view never reached. The counts are real and are kept; the rate is not,
// because there is no denominator, and 0% would be a conversion figure guard
// invented.
func TestAnalyticsPathsHaveNoRateWithoutAPageView(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "signup_click"), at); err != nil {
		t.Fatal(err)
	}

	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	row := pathRow(t, grid, "/pricing")
	if row.Views != 0 || row.Sessions != 0 {
		t.Fatalf("a path nobody viewed reads %d views over %d sessions", row.Views, row.Sessions)
	}
	cell := row.Actions["signup_click"]
	if cell.Events != 1 || cell.Sessions != 1 {
		t.Fatalf("the action that did happen came back as %d events over %d sessions", cell.Events, cell.Sessions)
	}
	if cell.Rate != 0 {
		t.Fatalf("a rate of %v was computed against no sessions", cell.Rate)
	}
}

// The chart behind an opened row: that path's days, in order, page views only.
//
// Oldest first, because the renderer draws what it is handed and a series that
// arrived out of order is a line that zigzags backwards through the week.
func TestAnalyticsPathSeriesIsOnePointPerDayOfPageViews(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	wednesday := monday.Add(48 * time.Hour)
	for _, b := range []model.Beacon{
		beacon("a1", "/pricing", "page_view", "signup_click"),
		beacon("b2", "/pricing", "page_view"),
	} {
		if err := store.addAnalyticsAt(b, monday); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.addAnalyticsAt(beacon("c3", "/pricing", "page_view"), wednesday); err != nil {
		t.Fatal(err)
	}
	// Another path on the same days, to prove the series is one path's and not
	// the product's.
	if err := store.addAnalyticsAt(beacon("d4", "/docs", "page_view"), monday); err != nil {
		t.Fatal(err)
	}

	series, err := store.AnalyticsPathSeries("/pricing", monday, wednesday)
	if err != nil {
		t.Fatal(err)
	}
	// Tuesday is absent rather than zero. The rollup cannot tell a page nobody
	// visited from a day guard was not running to hear about it, and a zero
	// drawn for both makes an outage read as a quiet Tuesday.
	if len(series) != 2 {
		t.Fatalf("three days with two of them seen came back as %d points", len(series))
	}
	if !series[0].Day.Equal(monday.Truncate(24 * time.Hour)) {
		t.Fatalf("the first point is %v, not the day it happened on", series[0].Day)
	}
	// Midnight UTC, because the point is a whole day rather than the moment
	// inside it the first beacon happened to arrive.
	if series[0].Day.Hour() != 0 || series[0].Day.Location() != time.UTC {
		t.Fatalf("a day came back as %v", series[0].Day)
	}
	// Two sessions, and the action on one of them is not a page view: a chart
	// counting every event would say the page was read three times.
	if series[0].Views != 2 || series[0].Sessions != 2 {
		t.Fatalf("monday came back as %d views over %d sessions", series[0].Views, series[0].Sessions)
	}
	if !series[1].Day.After(series[0].Day) {
		t.Fatalf("the points came back %v then %v", series[0].Day, series[1].Day)
	}
	if series[1].Views != 1 {
		t.Fatalf("wednesday came back as %d views", series[1].Views)
	}
}

// The window bounds the series, the same way it bounds the grid above it — a
// chart drawn over more days than the figures beside it is two windows on one
// screen.
func TestAnalyticsPathSeriesStopsAtTheWindow(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, day := range []time.Time{monday, monday.Add(24 * time.Hour), monday.Add(48 * time.Hour)} {
		if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view"), day); err != nil {
			t.Fatal(err)
		}
	}

	series, err := store.AnalyticsPathSeries("/pricing", monday, monday.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 {
		t.Fatalf("a two-day window came back as %d points", len(series))
	}
}

// A path nobody has been to is an empty series rather than an error or a null:
// the fold has to say "nothing here" in words, and it can only do that if the
// read succeeds.
func TestAnalyticsPathSeriesIsEmptyForAnUnknownPath(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	series, err := store.AnalyticsPathSeries("/nowhere", at, at)
	if err != nil {
		t.Fatal(err)
	}
	if series == nil {
		t.Fatal("an unknown path came back as null rather than an empty series")
	}
	if len(series) != 0 {
		t.Fatalf("an unknown path came back with %d points", len(series))
	}
}

// campaign is a beacon that arrived through a link somebody published: the
// three UTM keys and the host the browser came from.
func campaign(session, path string, source model.BeaconSource, referrer string, names ...string) model.Beacon {
	b := beacon(session, path, names...)
	b.Source, b.Referrer = source, referrer
	return b
}

func sourceSessions(t *testing.T, store *Store, day int64, path string, source model.BeaconSource, referrer string) int64 {
	t.Helper()
	var sessions int64
	err := store.rdb.QueryRow(`SELECT sessions FROM analytics_sources
WHERE day = ? AND path = ? AND source = ? AND medium = ? AND campaign = ? AND referrer_host = ?`,
		day, path, source.Source, source.Medium, source.Campaign, referrer).Scan(&sessions)
	if err != nil {
		t.Fatalf("no source row for %s on day %d (%+v, %q): %v", path, day, source, referrer, err)
	}
	return sessions
}

// A source is where a *session* came from, so the number beside it has to move
// once per session however many times that session posts — the same promise the
// rollup makes, and the one a conversion figure is only worth reading against.
func TestAnalyticsSourcesCountASessionOnce(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	launch := model.BeaconSource{Source: "hn", Medium: "referral", Campaign: "launch"}
	for range 2 {
		if err := store.addAnalyticsAt(campaign("a1", "/pricing", launch, "news.ycombinator.com", "page_view", "signup_click"), monday); err != nil {
			t.Fatal(err)
		}
	}
	if sessions := sourceSessions(t, store, epochDay(monday), "/pricing", launch, "news.ycombinator.com"); sessions != 1 {
		t.Fatalf("one session posting twice counted %d sessions", sessions)
	}

	if err := store.addAnalyticsAt(campaign("b2", "/pricing", launch, "news.ycombinator.com", "page_view"), monday); err != nil {
		t.Fatal(err)
	}
	if sessions := sourceSessions(t, store, epochDay(monday), "/pricing", launch, "news.ycombinator.com"); sessions != 2 {
		t.Fatalf("a second session counted %d sessions", sessions)
	}

	// Tomorrow is a new row, because the rollup is keyed by day and a campaign
	// that ran for a week is a week of rows rather than one that never closes.
	tuesday := monday.Add(24 * time.Hour)
	if err := store.addAnalyticsAt(campaign("a1", "/pricing", launch, "news.ycombinator.com", "page_view"), tuesday); err != nil {
		t.Fatal(err)
	}
	if sessions := sourceSessions(t, store, epochDay(tuesday), "/pricing", launch, "news.ycombinator.com"); sessions != 1 {
		t.Fatalf("the next day counted %d sessions", sessions)
	}
	if sessions := sourceSessions(t, store, epochDay(monday), "/pricing", launch, "news.ycombinator.com"); sessions != 2 {
		t.Fatalf("yesterday moved to %d sessions", sessions)
	}
}

// Attribution rides the page view that brought the session to the page, so a
// beacon that is only an action attributes nothing: it is the same session
// pressing a button on a page it was already counted on.
func TestAnalyticsSourcesRideThePageView(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	twitter := model.BeaconSource{Source: "twitter"}
	if err := store.addAnalyticsAt(campaign("a1", "/pricing", twitter, "t.co", "signup_click"), at); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_sources`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("an action with no page view behind it wrote %d source rows", rows)
	}
}

// What arrives is whatever somebody posted, because the door is public: a full
// referrer URL is a stranger's private path, and two spellings of one campaign
// are two halves of a number nobody can add up.
func TestAnalyticsSourcesNormaliseWhatArrived(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	shouted := model.BeaconSource{Source: "  HackerNews ", Campaign: "Launch"}
	if err := store.addAnalyticsAt(campaign("a1", "/pricing", shouted, "https://News.YCombinator.com/item?id=42", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	folded := model.BeaconSource{Source: "hackernews", Campaign: "launch"}
	if err := store.addAnalyticsAt(campaign("b2", "/pricing", folded, "news.ycombinator.com", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if sessions := sourceSessions(t, store, epochDay(at), "/pricing", folded, "news.ycombinator.com"); sessions != 2 {
		t.Fatalf("two spellings of one campaign counted %d sessions", sessions)
	}
	var rows int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_sources`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("one campaign landed as %d rows", rows)
	}
}

// The list under an opened row, and the property that makes it readable beside
// the figures above it: every session that saw the page is in exactly one line
// of it, including the ones that arrived through nothing at all.
func TestAnalyticsPathSourcesAccountForEverySession(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	hn := model.BeaconSource{Source: "hn", Medium: "referral"}
	for _, b := range []model.Beacon{
		beacon("a1", "/pricing", "page_view"),
		beacon("b2", "/pricing", "page_view"),
		campaign("c3", "/pricing", hn, "news.ycombinator.com", "page_view"),
		campaign("d4", "/pricing", model.BeaconSource{}, "duckduckgo.com", "page_view"),
		// Another page, to prove the list belongs to the path it was opened on.
		campaign("e5", "/docs", hn, "news.ycombinator.com", "page_view"),
	} {
		if err := store.addAnalyticsAt(b, at); err != nil {
			t.Fatal(err)
		}
	}

	sources, err := store.AnalyticsPathSources("/pricing", at, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("three ways onto one page came back as %d rows: %+v", len(sources), sources)
	}
	// Biggest first: the list is read from the top, and direct traffic is a
	// line of it rather than the remainder somebody has to work out.
	if sources[0].Sessions != 2 || sources[0].Source != "" || sources[0].Referrer != "" {
		t.Fatalf("the largest row is %+v", sources[0])
	}
	var counted int64
	for _, row := range sources {
		counted += row.Sessions
	}
	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	// The sources have to add up to the sessions the row above them says, or
	// the shares in the fold are against a total that is on screen and does not
	// match.
	if row := pathRow(t, grid, "/pricing"); counted != row.Sessions {
		t.Fatalf("%d sessions were attributed against %d on the path", counted, row.Sessions)
	}
}

// The window bounds the list the same way it bounds the chart beside it.
func TestAnalyticsPathSourcesStopAtTheWindow(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	tuesday := monday.Add(24 * time.Hour)
	hn := model.BeaconSource{Source: "hn"}
	if err := store.addAnalyticsAt(campaign("a1", "/pricing", hn, "news.ycombinator.com", "page_view"), monday); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(campaign("b2", "/pricing", hn, "news.ycombinator.com", "page_view"), tuesday); err != nil {
		t.Fatal(err)
	}

	sources, err := store.AnalyticsPathSources("/pricing", monday, monday)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Sessions != 1 {
		t.Fatalf("a one-day window came back as %+v", sources)
	}
	// Two days is the same campaign summed, not two rows: the rollup is keyed
	// by day and the list is what happened over the window.
	if sources, err = store.AnalyticsPathSources("/pricing", monday, tuesday); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Sessions != 2 {
		t.Fatalf("a two-day window came back as %+v", sources)
	}
}

// The third ceiling, and the one a public door makes necessary: the four
// strings come off a query string on somebody else's site, so a bot appending
// a random campaign is a row per session kept for as long as the rollup.
func TestAnalyticsSourcesPastTheCeilingAreCountedUnderOther(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range maxAnalyticsSources {
		one := model.BeaconSource{Campaign: fmt.Sprintf("c%d", i)}
		if err := store.addAnalyticsAt(campaign(fmt.Sprintf("s%d", i), "/pricing", one, "", "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}
	if capped := analyticsHealth(t, store).SourcesCapped; capped != 0 {
		t.Fatalf("the campaigns up to the ceiling capped %d sessions", capped)
	}

	for _, session := range []string{"x1", "x2"} {
		flood := model.BeaconSource{Campaign: "flood-" + session}
		if err := store.addAnalyticsAt(campaign(session, "/pricing", flood, "", "page_view"), at); err != nil {
			t.Fatal(err)
		}
	}
	if sessions := sourceSessions(t, store, epochDay(at), "/pricing", model.BeaconSource{Source: sourceOther}, ""); sessions != 2 {
		t.Fatalf("the two campaigns past the ceiling counted %d sessions", sessions)
	}
	var rows int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_sources WHERE campaign LIKE 'flood-%'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("a campaign past the ceiling kept %d rows of its own", rows)
	}
	if capped := analyticsHealth(t, store).SourcesCapped; capped != 2 {
		t.Fatalf("two sessions past the ceiling were counted as %d", capped)
	}

	// A campaign already counted today is still itself: the ceiling closes the
	// door on new ones rather than moving what is already through it.
	first := model.BeaconSource{Campaign: "c0"}
	if err := store.addAnalyticsAt(campaign("x3", "/pricing", first, "", "page_view"), at); err != nil {
		t.Fatal(err)
	}
	if sessions := sourceSessions(t, store, epochDay(at), "/pricing", first, ""); sessions != 2 {
		t.Fatalf("a campaign already counted moved to %d sessions", sessions)
	}

	// The fold draws the top of the list, never the whole tail — a page whose
	// traffic came from a hundred campaigns has an answer that is "a hundred
	// campaigns", not a column of ones somebody scrolls.
	sources, err := store.AnalyticsPathSources("/pricing", at, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != maxPathSources {
		t.Fatalf("a path with %d campaigns drew %d rows", maxAnalyticsSources+1, len(sources))
	}
}

// The strip is two windows of equal length, because a number on its own is not
// a measurement. Sessions are counted site-wide rather than summed per path —
// one visit reading three pages is one session, and the ratio is the whole
// point of the row.
func TestAnalyticsSummaryComparesTheWindowBeforeIt(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	monday := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	tuesday := monday.Add(24 * time.Hour)
	// The window: one session reading two pages and pressing once, and a second
	// session reading one — two sessions, three views, one action.
	if err := store.addAnalyticsAt(beacon("a1", "/", "page_view"), monday); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), monday); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("b2", "/", "page_view"), tuesday); err != nil {
		t.Fatal(err)
	}
	// The window before it, of equal length: two days back, one session, one view.
	if err := store.addAnalyticsAt(beacon("c3", "/", "page_view"), monday.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	summary, err := store.AnalyticsSummary(monday, tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Window.Sessions != 2 || summary.Window.Views != 3 {
		t.Fatalf("the window is %d sessions over %d views", summary.Window.Sessions, summary.Window.Views)
	}
	if summary.Window.ViewsPerSession != 1.5 || summary.Window.ActionsPerSession != 0.5 {
		t.Fatalf("the ratios are %v views and %v actions per session",
			summary.Window.ViewsPerSession, summary.Window.ActionsPerSession)
	}
	if summary.Previous.Sessions != 1 || summary.Previous.Views != 1 {
		t.Fatalf("the previous window is %d sessions over %d views", summary.Previous.Sessions, summary.Previous.Views)
	}
	if summary.Previous.ActionsPerSession != 0 {
		t.Fatalf("a window with no actions reads %v actions per session", summary.Previous.ActionsPerSession)
	}
}

// An empty window is silence rather than zero, and a ratio over no sessions is
// the arithmetic that would otherwise produce a number nobody measured.
func TestAnalyticsSummaryIsSilentOnAnEmptyWindow(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	summary, err := store.AnalyticsSummary(at, at)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Window != (model.AnalyticsWindow{}) || summary.Previous != (model.AnalyticsWindow{}) {
		t.Fatalf("a window nothing arrived in reads %+v", summary)
	}
	grid, err := store.AnalyticsPaths(at, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(grid) != 0 {
		t.Fatalf("a window nothing arrived in has %d rows", len(grid))
	}
}

func rawAnalyticsRows(t *testing.T, store *Store, session string) int64 {
	t.Helper()
	var count int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_events WHERE session = ?`, session).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func seenAnalyticsRows(t *testing.T, store *Store, day int64) int64 {
	t.Helper()
	var count int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_seen WHERE day = ?`, day).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The one promise the rollup exists to make: it outlives the rows it was
// counted from. A sweep that took the numbers with the raw feed would leave
// analytics answering "versus last month" with a day of data.
func TestAnalyticsPurgeKeepsTheRollupWhenTheRawFeedGoes(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	now := time.Now().UTC()
	// Past the twenty-four hours an in-memory store keeps its telemetry for,
	// which is the same window the raw analytics rows are swept on.
	old := now.Add(-48 * time.Hour)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), old); err != nil {
		t.Fatal(err)
	}
	if err := store.addAnalyticsAt(beacon("b2", "/pricing", "page_view"), now); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Purge(); err != nil {
		t.Fatal(err)
	}

	if rows := rawAnalyticsRows(t, store, "a1"); rows != 0 {
		t.Fatalf("the raw feed kept %d rows from two days ago", rows)
	}
	if rows := rawAnalyticsRows(t, store, "b2"); rows != 1 {
		t.Fatalf("the raw feed has %d of this minute's rows", rows)
	}
	events, sessions := rollupRow(t, store, epochDay(old), "/pricing", "signup_click")
	if events != 1 || sessions != 1 {
		t.Fatalf("the rollup for a swept day reads %d events over %d sessions", events, sessions)
	}
}

// Seen is purged behind the rollup, and what that costs is the point of the
// test: the counts stand afterwards, exactly as they were counted, and there is
// no longer anything to recount them from.
func TestAnalyticsPurgeDropsSeenBehindTheRollup(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	now := time.Now().UTC()
	yesterday := now.Add(-24 * time.Hour)      // inside both windows
	lastWeek := now.Add(-10 * 24 * time.Hour)  // past seen, inside the rollup
	lastYear := now.Add(-200 * 24 * time.Hour) // past both
	for _, at := range []time.Time{yesterday, lastWeek, lastYear} {
		for range 2 {
			if err := store.addAnalyticsAt(beacon("a1", "/pricing", "page_view", "signup_click"), at); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := store.Purge(); err != nil {
		t.Fatal(err)
	}

	if rows := seenAnalyticsRows(t, store, epochDay(yesterday)); rows != 2 {
		t.Fatalf("yesterday kept %d seen rows", rows)
	}
	if rows := seenAnalyticsRows(t, store, epochDay(lastWeek)); rows != 0 {
		t.Fatalf("a day behind the seen window kept %d seen rows", rows)
	}
	// The exactness is spent, and the number it bought is still there.
	events, sessions := rollupRow(t, store, epochDay(lastWeek), "/pricing", "signup_click")
	if events != 2 || sessions != 1 {
		t.Fatalf("the counts moved to %d events over %d sessions once seen had gone", events, sessions)
	}
	var days int64
	if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM analytics_rollup WHERE day = ?`, epochDay(lastYear)).Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days != 0 {
		t.Fatalf("a day behind the rollup window kept %d rows", days)
	}
}

// The two windows are numbers somebody types, so they are validated where the
// other two are — and a save that does not carry them at all leaves them alone,
// because JSON cannot tell "zero days" from a form that predates the field.
func TestAnalyticsRetentionSettings(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	settings, err := store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.AnalyticsRollupDays != model.DefaultAnalyticsRollupDays || settings.AnalyticsSeenDays != model.DefaultAnalyticsSeenDays {
		t.Fatalf("a fresh store starts with %d rollup days and %d seen days", settings.AnalyticsRollupDays, settings.AnalyticsSeenDays)
	}

	if err := store.UpdateSettings(Settings{RetentionHours: 12, MaxEvents: 500}); err != nil {
		t.Fatal(err)
	}
	if settings, err = store.Settings(); err != nil {
		t.Fatal(err)
	}
	if settings.RetentionHours != 12 || settings.AnalyticsRollupDays != model.DefaultAnalyticsRollupDays || settings.AnalyticsSeenDays != model.DefaultAnalyticsSeenDays {
		t.Fatalf("a save of the other two numbers left %+v", settings)
	}

	if err := store.UpdateSettings(Settings{RetentionHours: 12, MaxEvents: 500, AnalyticsRollupDays: 30, AnalyticsSeenDays: 3}); err != nil {
		t.Fatal(err)
	}
	for _, refused := range []Settings{
		{RetentionHours: 12, MaxEvents: 500, AnalyticsRollupDays: model.MaxAnalyticsDays + 1, AnalyticsSeenDays: 3},
		{RetentionHours: 12, MaxEvents: 500, AnalyticsRollupDays: 30, AnalyticsSeenDays: 60},
	} {
		if err := store.UpdateSettings(refused); err == nil {
			t.Fatalf("stored %d rollup days against %d seen days", refused.AnalyticsRollupDays, refused.AnalyticsSeenDays)
		}
	}
	if settings, err = store.Settings(); err != nil {
		t.Fatal(err)
	}
	if settings.AnalyticsRollupDays != 30 || settings.AnalyticsSeenDays != 3 {
		t.Fatalf("a refused save moved the windows to %d and %d", settings.AnalyticsRollupDays, settings.AnalyticsSeenDays)
	}
}

// A fresh store is silent rather than zero-ish: nothing has been thrown away,
// nothing has arrived, and the door has not been mounted — which is the state
// the page draws "analytics is off" from, and the one it must not draw
// "the tracker is broken" from.
func TestAnalyticsHealthIsSilentBeforeAnythingArrives(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	health := analyticsHealth(t, store)
	if health.Enabled {
		t.Error("a store nothing mounted a door on says analytics is on")
	}
	if !health.LastEvent.IsZero() {
		t.Errorf("a store that has received nothing last received something at %s", health.LastEvent)
	}
	if health.Rejected != 0 || health.ActionsRefused != 0 || health.PathsCapped != 0 {
		t.Errorf("a fresh store has already thrown something away: %+v", health)
	}
	if health.Actions != 0 || health.SeenRows != 0 {
		t.Errorf("a fresh store knows %d actions over %d seen rows", health.Actions, health.SeenRows)
	}
}

// The four numbers the health page is for, each moved by the thing it counts.
// A tracker being silently dropped is the failure mode people take weeks to
// notice, so every way guard drops one has to reach a number somebody can look
// at.
func TestAnalyticsHealthCountsWhatWasThrownAway(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	store.AnalyticsOpened()

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", actionPageView, "signup_click"), at); err != nil {
		t.Fatal(err)
	}
	store.AnalyticsRejected()
	store.AnalyticsRejected()

	health := analyticsHealth(t, store)
	if !health.Enabled {
		t.Error("the door was mounted and the health says analytics is off")
	}
	if health.Rejected != 2 {
		t.Errorf("two refused beacons were counted as %d", health.Rejected)
	}
	// page_view is the Views column rather than a discovery, so one action is
	// the honest answer to how many names the grid could grow a column for.
	if health.Actions != 1 {
		t.Errorf("one discovered name beside page_view was counted as %d", health.Actions)
	}
	if health.SeenRows != 2 {
		t.Errorf("one session doing two things on one path left %d seen rows", health.SeenRows)
	}
	if !health.LastEvent.Equal(at) {
		t.Errorf("the last event landed at %s, want %s", health.LastEvent, at)
	}
}

// The ceilings are the other half of the same question, and they are counted
// where they are enforced rather than beside the door — a name past discovery
// is refused inside a beacon guard otherwise accepted, so the two numbers can
// never be one.
func TestAnalyticsHealthCarriesTheCeilings(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for i := range maxAnalyticsPaths {
		if err := store.addAnalyticsAt(beacon("a1", fmt.Sprintf("/p/%d", i), actionPageView), at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxAnalyticsActions; i += model.MaxBeaconEvents {
		b := model.Beacon{Session: "a1", Path: "/p/0"}
		for j := range model.MaxBeaconEvents {
			b.Events = append(b.Events, model.TrackEvent{Name: fmt.Sprintf("act_%d", i+j)})
		}
		if err := store.addAnalyticsAt(b, at); err != nil {
			t.Fatal(err)
		}
	}
	// One beacon past both: a path the day has no room for, carrying a name
	// discovery has no room for.
	if err := store.addAnalyticsAt(beacon("b2", "/search/one", actionPageView, "one_too_many"), at); err != nil {
		t.Fatal(err)
	}

	health := analyticsHealth(t, store)
	if health.PathsCapped != 1 {
		t.Errorf("one beacon past the path ceiling was counted as %d", health.PathsCapped)
	}
	if health.ActionsRefused != 1 {
		t.Errorf("one name past the discovery ceiling was counted as %d", health.ActionsRefused)
	}
	if health.Actions != maxAnalyticsActions {
		t.Errorf("discovery holds %d names, want the ceiling", health.Actions)
	}
	if health.Rejected != 0 {
		t.Errorf("a beacon the door accepted was counted as %d rejections", health.Rejected)
	}
}

// Deleting an action is not hiding a column: the rows counted under it go from
// every table that holds them, including the raw feed somebody is watching
// while they instrument the page. What must not go with it is the page views on
// the same path, which are a different action.
func TestDeletingAnAnalyticsActionTakesEveryRowUnderIt(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	for _, session := range []string{"a1", "b2"} {
		if err := store.addAnalyticsAt(beacon(session, "/pricing", actionPageView, "signup_click"), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteAnalyticsAction("signup_click"); err != nil {
		t.Fatal(err)
	}

	day := epochDay(at)
	for table, column := range map[string]string{
		"analytics_rollup": "action",
		"analytics_seen":   "action",
		"analytics_events": "action",
	} {
		var left int64
		if err := store.rdb.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, "signup_click").Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left != 0 {
			t.Errorf("%s still holds %d rows for a deleted action", table, left)
		}
	}
	if events, sessions := rollupRow(t, store, day, "/pricing", actionPageView); events != 2 || sessions != 2 {
		t.Errorf("deleting an action took the page views with it: %d events over %d sessions", events, sessions)
	}
	// A name that is not there is not a deletion. Answering as though it were
	// would tell somebody their rollup rows are gone when nothing was read —
	// and page_view is never in the table, so the one name that must survive is
	// refused by the same line.
	for _, name := range []string{"signup_click", actionPageView} {
		if err := store.DeleteAnalyticsAction(name); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("deleting %s that is not there = %v", name, err)
		}
	}
}

// Pinning is the whole set in one write, because pinning a column and putting
// it second is one decision. A name nothing has ever sent is refused, and the
// refusal is the whole request rather than the rows before the bad one.
func TestPinningAnalyticsActionsIsOneOrderedDecision(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	if err := store.addAnalyticsAt(beacon("a1", "/pricing", actionPageView, "signup_click", "docs_search"), at); err != nil {
		t.Fatal(err)
	}
	pinned, err := store.PinAnalyticsActions([]string{"docs_search", "signup_click"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned) != 2 || pinned[0].Name != "docs_search" || pinned[1].Position != 1 {
		t.Fatalf("pinned = %#v", pinned)
	}

	if _, err := store.PinAnalyticsActions([]string{"signup_click", "never_fired"}); err == nil {
		t.Fatal("a column that could have no cells was pinned")
	}
	// The unpin at the top of the write is inside the transaction, so a refused
	// list leaves the order that was there rather than nothing pinned at all.
	after, err := store.AnalyticsActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || !after[0].Pinned || after[0].Name != "docs_search" {
		t.Fatalf("a refused pin was half applied: %#v", after)
	}
}

// The drill: a row on /analytics is a path, and this is what turns it into the
// spans of the sessions that saw it. The ids never leave the database — the
// filter carries the path, the subquery carries the join — so the link off a
// path row is short enough to share and still means "these visits".
func TestAnalyticsFilterWalksAPathIntoItsSessions(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	at := time.Now().UTC()
	for _, b := range []model.Beacon{
		beacon("aaaa1111", "/pricing", actionPageView, "signup_click"),
		beacon("bbbb2222", "/docs", actionPageView),
	} {
		if err := store.addAnalyticsAt(b, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Add(
		Event{Signal: "traces", Service: "browser", Name: "documentLoad", Timestamp: at,
			Attributes: map[string]any{"rum.session_id": "aaaa1111"}},
		// The second spelling, because the two are one session everywhere else.
		Event{Signal: "traces", Service: "browser", Name: "POST /signup", Timestamp: at,
			Attributes: map[string]any{"session.id": "aaaa1111"}},
		Event{Signal: "traces", Service: "browser", Name: "documentLoad", Timestamp: at,
			Attributes: map[string]any{"rum.session_id": "bbbb2222"}},
		// A span from something that is not a browser at all.
		Event{Signal: "traces", Service: "api", Name: "GET /pricing", Timestamp: at},
	); err != nil {
		t.Fatal(err)
	}

	spans, err := store.Query(Filter{Signal: "traces", RUMPath: "/pricing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("the sessions that saw /pricing produced %d spans, want 2: %#v", len(spans), spans)
	}
	for _, span := range spans {
		if span.Service != "browser" {
			t.Errorf("%s came back for a path only browser sessions can have seen", span.Name)
		}
	}

	// A path nobody has been on is nothing, never everything: a filter that
	// fell through to "no clause" would answer with the whole table and read
	// as a page with a lot of traffic on it.
	spans, err = store.Query(Filter{Signal: "traces", RUMPath: "/nowhere"})
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 0 {
		t.Fatalf("a path with no sessions matched %d spans", len(spans))
	}
}
