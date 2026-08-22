package telemetry

import (
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
	if capped, _ := store.AnalyticsCaps(); capped != 0 {
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
	if capped, _ := store.AnalyticsCaps(); capped != 2 {
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
	if _, refused := store.AnalyticsCaps(); refused != 0 {
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
	if _, refused := store.AnalyticsCaps(); refused != 1 {
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
	if capped, _ := store.AnalyticsCaps(); capped != 0 {
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
