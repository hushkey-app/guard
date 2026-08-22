package telemetry

// What people did in a browser, stored the only way a single binary over SQLite
// can afford to store it.
//
// One rule carries the whole shape: **the rollup is the truth and the raw feed
// is a courtesy.** `analytics_events` is swept by the same retention the rest of
// the telemetry has, because it is there to be read the afternoon somebody is
// instrumenting a page. `analytics_rollup` is one row per day, path and action,
// which for a real product is thousands of rows a month rather than millions —
// so "versus last month" is a question guard can still answer after the raw
// rows have gone, which is the question analytics is actually asked.
//
// `analytics_seen` is what makes the session counts exact rather than a sketch:
// one row per day, path, action and session, inserted OR IGNORE, so the write
// that changed nothing is the write that says this session had already done
// this. It is the only table here that grows with traffic rather than with
// content, and it is purged behind the rollup — after which the counts stand
// and cannot be recomputed. That is the honest half of the trade, and the docs
// say it out loud.
//
// The two tables that end in a person's decision — `analytics_actions` and
// `analytics_path_rules` — are configuration and travel in the backup. The four
// that are counted are not, for the same reason logs are not.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	// Imported as glob because Match is the only thing wanted from it and
	// `path` is what half the variables in this file are called.
	glob "path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateAnalytics(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS analytics_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  -- Two clocks, and the sweep reads guard's. timestamp_ns is when the browser
  -- says it happened and is worth keeping — it is what orders a session — but
  -- it comes from a stranger's machine, and a retention window keyed on it
  -- would delete a row on arrival or keep one forever depending on how wrong
  -- somebody's clock is.
  received_at_ns INTEGER NOT NULL,
  timestamp_ns INTEGER NOT NULL,
  session TEXT NOT NULL,
  path TEXT NOT NULL,
  action TEXT NOT NULL,
  props_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS analytics_events_received ON analytics_events(received_at_ns DESC);
CREATE TABLE IF NOT EXISTS analytics_rollup (
  -- A day is a whole UTC day, counted by epochDay, the same as the status
  -- page's rollup. UTC rather than the server's zone because the people
  -- reading a product's numbers are not all where the box was provisioned.
  day INTEGER NOT NULL,
  path TEXT NOT NULL,
  action TEXT NOT NULL,
  events INTEGER NOT NULL DEFAULT 0,
  sessions INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(day, path, action)
);
CREATE TABLE IF NOT EXISTS analytics_seen (
  day INTEGER NOT NULL,
  path TEXT NOT NULL,
  action TEXT NOT NULL,
  session TEXT NOT NULL,
  PRIMARY KEY(day, path, action, session)
-- WITHOUT ROWID, and it is the only table here that is: the whole row is its
-- key, so the usual shape would store every value twice — once in the index
-- that answers the INSERT OR IGNORE and once in a table nothing ever reads by
-- rowid. This is the table that grows with traffic, so that is the one place
-- the difference is worth a line of explanation.
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS analytics_sources (
  day INTEGER NOT NULL,
  path TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  medium TEXT NOT NULL DEFAULT '',
  campaign TEXT NOT NULL DEFAULT '',
  referrer_host TEXT NOT NULL DEFAULT '',
  sessions INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(day, path, source, medium, campaign, referrer_host)
);
-- Discovery is capped at a couple of hundred names (see the cardinality
-- ceilings), which is why nothing here carries an index: the pinned columns
-- are read by scanning a table that cannot grow past a page.
CREATE TABLE IF NOT EXISTS analytics_actions (
  name TEXT PRIMARY KEY,
  pinned INTEGER NOT NULL DEFAULT 0,
  position INTEGER NOT NULL DEFAULT 0,
  first_seen_ns INTEGER NOT NULL DEFAULT 0,
  last_seen_ns INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS analytics_path_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pattern TEXT NOT NULL,
  replacement TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0
);
-- First match wins, so a second rule with the same pattern can never fire. It
-- would sit on the page looking like configuration and do nothing, which is
-- worse than being refused when it is typed.
CREATE UNIQUE INDEX IF NOT EXISTS analytics_path_rules_pattern ON analytics_path_rules(pattern);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate analytics: %w", err)
	}
	return nil
}

// actionPageView is the one reserved name. It is counted like any other action
// — it is what the Views column is — but it is never offered as a column of its
// own, so it is not a discovery anybody has to decide about.
const actionPageView = "page_view"

// The two cardinality ceilings. They are constants beside the code that
// enforces them rather than numbers on a settings page, because both have one
// right answer: a table that grows until SQLite is slow is not a table anybody
// chose. Every analytics product ever pointed at a site with unbounded URLs has
// learned the first one.
//
// The two are answered differently on purpose — a path over the ceiling is
// counted somewhere honest, a name over it is refused — and the reason is under
// each of the functions below.
const (
	maxAnalyticsPaths   = 1000
	maxAnalyticsActions = 200
)

// pathOther is where a day's paths go once it has as many as guard will keep.
// A row whose count is real and whose name says what happened beats a table
// nobody capped. It can never collide with a page: NormalisePath gives every
// path a leading slash and this has none.
const pathOther = "(other)"

// analyticsCaps counts what the ceilings threw away.
//
// In memory, so the numbers are "since this process started" — a refused action
// name is the one thing guard deliberately never writes down, so there is
// nowhere else it could be counted from. They are numbers on a page rather than
// a log line because a tracker firing an action that silently does not exist is
// the failure mode people take weeks to notice.
type analyticsCaps struct {
	pathsCapped    atomic.Int64
	actionsRefused atomic.Int64
}

// AnalyticsCaps reports what the ceilings refused since guard started: beacons
// whose path rolled into (other), and events whose name arrived after discovery
// was full.
func (s *Store) AnalyticsCaps() (pathsCapped, actionsRefused int64) {
	return s.analytics.pathsCapped.Load(), s.analytics.actionsRefused.Load()
}

// AddAnalytics folds one beacon into the tables that count: the raw row
// somebody instrumenting a page is watching, the seen row that makes the
// session count exact, and the rollup that outlives both.
//
// One transaction per beacon rather than one per event, because a batch is what
// the tracker actually sends and fifty commits would be fifty trips through the
// single writer everything else in guard is already queued behind.
func (s *Store) AddAnalytics(b model.Beacon) error {
	return s.addAnalyticsAt(b, time.Now().UTC())
}

// addAnalyticsAt is AddAnalytics with the clock passed in, which is how the
// tests cross a day boundary without waiting for one.
//
// The day is guard's clock, never the browser's. An event's own timestamp is
// kept on the raw row because it is what orders a session, but it comes from a
// stranger's machine: a rollup keyed on it would let one visitor with a wrong
// laptop write a day into next year, and the seen row that is supposed to make
// that day's count exact would be filed under the same wrong day forever.
func (s *Store) addAnalyticsAt(b model.Beacon, at time.Time) error {
	// Validated here as well as at the door. These action names become the
	// grid's columns, so "a name that can be a column" has to be true of the
	// store rather than of one handler — a second caller must not be able to
	// write one that cannot.
	if err := b.Validate(); err != nil {
		return err
	}
	// Normalised again for the same reason: the door is public, so the path is
	// whatever somebody posted, and a path that skipped it would be a second
	// row for a page that already has one.
	path := model.NormalisePath(b.Path)
	day := epochDay(at)
	receivedNS := at.UTC().UnixNano()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The rules are read once per beacon, inside the transaction that is about
	// to use them, rather than held in memory: they are somebody's
	// configuration on a table this ceiling already bounds, and a cached copy
	// would be a second answer to "what are the rules" that a save could leave
	// stale on one process while the page showed the other.
	rules, err := pathRules(tx)
	if err != nil {
		return err
	}
	// Rules shape the rollup, which is the truth, and are applied before the
	// ceiling, which is the whole point of them: collapsing `/users/*` is what
	// stops a day's thousand paths ever being reached. The raw row keeps the
	// URL as it arrived, because that is where somebody reads the path that
	// made them write the rule.
	//
	// Which row this beacon counts against, then, is the collapsed path until
	// the day has as many as guard keeps.
	counted, capped, err := analyticsCountedPath(tx, day, applyPathRules(rules, path))
	if err != nil {
		return err
	}
	// The discovery ceiling is read once per beacon rather than once per event:
	// this transaction holds the single writer, so no name can appear under it,
	// and a batch of fifty events must not be fifty counts of the same table.
	var names int64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM analytics_actions`).Scan(&names); err != nil {
		return fmt.Errorf("count analytics actions: %w", err)
	}

	raw, err := tx.Prepare(`INSERT INTO analytics_events(received_at_ns, timestamp_ns, session, path, action, props_json) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer raw.Close()
	seen, err := tx.Prepare(`INSERT OR IGNORE INTO analytics_seen(day, path, action, session) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer seen.Close()
	rollup, err := tx.Prepare(`INSERT INTO analytics_rollup(day, path, action, events, sessions) VALUES(?,?,?,1,?)
ON CONFLICT(day, path, action) DO UPDATE SET events = events + 1, sessions = sessions + ?`)
	if err != nil {
		return err
	}
	defer rollup.Close()
	discovered, err := tx.Prepare(`INSERT INTO analytics_actions(name, first_seen_ns, last_seen_ns) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET last_seen_ns = excluded.last_seen_ns`)
	if err != nil {
		return err
	}
	defer discovered.Close()
	found, err := tx.Prepare(`SELECT 1 FROM analytics_actions WHERE name = ?`)
	if err != nil {
		return err
	}
	defer found.Close()

	var refused int64
	for _, event := range b.Events {
		// A name arriving after discovery is full is **refused** — never
		// truncated and never folded into a neighbour, because two teams'
		// events in one column is a wrong number nobody can see is wrong,
		// where a missing one is a number on the health page. Refused whole,
		// the raw row included: an event in the feed that the grid can never
		// show is an event somebody spends an afternoon looking for.
		//
		// page_view is exempt because it is not a discovery — it is the Views
		// column, and a cap that could silence it would cap the page itself.
		if event.Name != actionPageView {
			var one int
			switch err := found.QueryRow(event.Name).Scan(&one); {
			case err == nil:
			case errors.Is(err, sql.ErrNoRows):
				if names >= maxAnalyticsActions {
					refused++
					continue
				}
				names++
			default:
				return fmt.Errorf("read analytics actions: %w", err)
			}
		}
		props := "{}"
		if len(event.Props) > 0 {
			// A nil or empty map marshals to `null` and to `{}` respectively,
			// and a column of `null` text is a column every reader has to
			// special-case.
			encoded, err := json.Marshal(event.Props)
			if err != nil {
				return fmt.Errorf("encode analytics props: %w", err)
			}
			props = string(encoded)
		}
		timestampNS := receivedNS
		if event.At > 0 {
			timestampNS = event.At * int64(time.Millisecond)
		}
		// The raw row keeps the path as it arrived even when the rollup counts
		// it under (other). The feed is bounded by rows rather than by distinct
		// paths, so the URLs that overflowed cost nothing to keep — and they
		// are exactly what somebody needs to write the path rule that stops it
		// happening again.
		if _, err := raw.Exec(receivedNS, timestampNS, b.Session, path, event.Name, props); err != nil {
			return fmt.Errorf("store analytics event: %w", err)
		}
		result, err := seen.Exec(day, counted, event.Name, b.Session)
		if err != nil {
			return fmt.Errorf("record analytics session: %w", err)
		}
		// The write that changed nothing is the whole distinct count: the row
		// is its own key, so an ignored insert means this session had already
		// done this on this path today and only `events` moves.
		firstToday, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if _, err := rollup.Exec(day, counted, event.Name, firstToday, firstToday); err != nil {
			return fmt.Errorf("roll up analytics event: %w", err)
		}
		if event.Name == actionPageView {
			continue
		}
		if _, err := discovered.Exec(event.Name, receivedNS, receivedNS); err != nil {
			return fmt.Errorf("record analytics action: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Counted after the commit, because a beacon that rolled back refused
	// nothing: a health number that moves on a write that did not happen is a
	// number somebody chases for an afternoon.
	if capped {
		s.analytics.pathsCapped.Add(1)
	}
	s.analytics.actionsRefused.Add(refused)
	return nil
}

// analyticsCountedPath answers which rollup row a beacon counts against: its
// own path, or (other) once the day has as many distinct paths as guard keeps.
//
// A path already counted today is always itself. The ceiling closes the door on
// new paths rather than moving ones already through it, so a page does not stop
// having numbers halfway through the afternoon the flood started.
//
// The count is only asked for a path guard has not seen today, and it reads a
// table this very ceiling bounds — which is the tidy half of a cap: enforcing
// it can never cost more than the cap allows.
func analyticsCountedPath(tx *sql.Tx, day int64, path string) (string, bool, error) {
	var one int
	switch err := tx.QueryRow(`SELECT 1 FROM analytics_rollup WHERE day = ? AND path = ? LIMIT 1`, day, path).Scan(&one); {
	case err == nil:
		return path, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", false, fmt.Errorf("read analytics paths: %w", err)
	}
	var distinct int64
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT path) FROM analytics_rollup WHERE day = ?`, day).Scan(&distinct); err != nil {
		return "", false, fmt.Errorf("count analytics paths: %w", err)
	}
	if distinct >= maxAnalyticsPaths {
		return pathOther, true, nil
	}
	return path, false, nil
}

// The path rules: how a URL with an id in it becomes the page it is.
//
// `/users/*` → `/users/:id`, ordered, and the first match wins. They are
// applied at ingest rather than at read, which is the trade this feature makes
// out loud: a rule shapes what is counted from the moment it is stored and
// cannot rewrite the days already rolled up. The alternative — rules applied
// when the grid is drawn — would mean every read re-deciding what a path is,
// and a rollup keyed on something that changes underneath it is a rollup whose
// rows nobody can add up.
//
// A pattern is a glob rather than a regular expression, because the thing being
// written is a URL shape and `/users/*` is what somebody types when they mean
// one. `*` stops at a separator, which is what makes that rule mean "a user"
// rather than "everything under /users" — a second level is a second `*`.

// pathRuleColumns is one list, read through the pool by the page and through
// the transaction by ingest, so a column added for one cannot go missing in the
// other.
const pathRuleColumns = `id,pattern,replacement,position`

// analyticsQuerier is the half of *sql.DB and *sql.Tx a rule read needs. Both,
// because the page reads the rules through the pool and ingest reads them
// inside the transaction that is about to count against them.
type analyticsQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// PathRules reads the rules in the order they are applied.
func (s *Store) PathRules() ([]model.PathRule, error) { return pathRules(s.rdb) }

func pathRules(q analyticsQuerier) ([]model.PathRule, error) {
	rows, err := q.Query(`SELECT ` + pathRuleColumns + ` FROM analytics_path_rules ORDER BY position, id`)
	if err != nil {
		return nil, fmt.Errorf("read path rules: %w", err)
	}
	defer rows.Close()
	var out []model.PathRule
	for rows.Next() {
		var rule model.PathRule
		if err := rows.Scan(&rule.ID, &rule.Pattern, &rule.Replacement, &rule.Position); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// SavePathRules replaces the list with the one that was sent, in the order it
// was sent in.
//
// Replaced wholesale rather than matched by id the way a machine's commands
// are: a rule has no history to keep — nothing records when one last fired, and
// nothing counted under it points back at it — so the id is a handle for the
// page and not a thing worth preserving across an edit.
func (s *Store) SavePathRules(rules []model.PathRule) ([]model.PathRule, error) {
	prepared, err := preparePathRules(rules)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.Exec(`DELETE FROM analytics_path_rules`); err != nil {
		return nil, err
	}
	for position, rule := range prepared {
		if _, err := tx.Exec(`INSERT INTO analytics_path_rules(pattern, replacement, position) VALUES(?,?,?)`,
			rule.Pattern, rule.Replacement, position); err != nil {
			return nil, fmt.Errorf("store path rule: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.PathRules()
}

// PreviewPathRules answers what a set of rules would make of a set of paths,
// and stores nothing.
//
// It takes the rules rather than reading the stored ones on purpose: the dialog
// exists to prove a rule *before* it is saved, and a preview of what is already
// there could not do that. It is the same preparation and the same application
// the save runs, for the reason the `.env` import runs one call twice — a
// dialog that describes something other than what happens is worse than no
// dialog.
func (s *Store) PreviewPathRules(rules []model.PathRule, paths []string) ([]string, error) {
	prepared, err := preparePathRules(rules)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = applyPathRules(prepared, model.NormalisePath(path))
	}
	return out, nil
}

// preparePathRules is the half of a save that can refuse, run before anything
// is written and again for the preview.
//
// Both halves are lowercased because every path is lowercased before it is
// compared: a pattern with a capital in it could never match anything, and a
// replacement with one would name a row no page could ever produce. Refusing
// them instead would be refusing something guard knows exactly what to do with.
func preparePathRules(rules []model.PathRule) ([]model.PathRule, error) {
	out := make([]model.PathRule, 0, len(rules))
	seen := make(map[string]bool, len(rules))
	for position, rule := range rules {
		// Not NormalisePath on the pattern: `?` is a glob wildcard and
		// normalisation would read it as the start of a query string and cut
		// the pattern in half.
		rule.Pattern = strings.ToLower(strings.TrimSpace(rule.Pattern))
		rule.Replacement = strings.TrimSpace(rule.Replacement)
		if err := rule.Validate(); err != nil {
			return nil, err
		}
		rule.Replacement = model.NormalisePath(rule.Replacement)
		if _, err := glob.Match(rule.Pattern, "/"); err != nil {
			return nil, fmt.Errorf("%q is not a pattern: %w", rule.Pattern, err)
		}
		// The second rule with a pattern can never fire, because the first one
		// always wins. A row that sits on the page looking like configuration
		// and does nothing is worse than being told while it is being typed.
		if seen[rule.Pattern] {
			return nil, fmt.Errorf("there is already a rule for %q, and the first match wins", rule.Pattern)
		}
		seen[rule.Pattern] = true
		rule.Position = position
		out = append(out, rule)
	}
	return out, nil
}

// applyPathRules collapses a path onto the page it is, by the first rule that
// matches it. A path no rule matches is its own page and passes through.
//
// The replacement is a literal rather than a template with the wildcard put
// back in it: the whole point is that `/users/7` and `/users/8` become one row,
// and a replacement that could carry the id would be a way to write a rule that
// collapses nothing.
func applyPathRules(rules []model.PathRule, path string) string {
	for _, rule := range rules {
		// A pattern that will not compile is refused at the save, so a match
		// error here is a row from a guard that had not learned that yet: it
		// cannot fire, and treating it as no match is the only reading that
		// leaves the path alone.
		if ok, err := glob.Match(rule.Pattern, path); ok && err == nil {
			return rule.Replacement
		}
	}
	return path
}

// The read: the grid, and the strip above it.
//
// Both are answered from `analytics_rollup` rather than from the raw feed,
// because the raw feed is swept by the telemetry retention and "versus last
// month" is the question analytics is actually asked. A window is therefore
// whole UTC days at both ends — the rollup has no finer grain, and an hour
// somebody typed would be silently rounded into one anyway.

// analyticsDays is the span of whole UTC days a window of moments covers, both
// ends included.
//
// Inclusive on purpose: a window ending now has to contain today, or the page
// somebody is watching while they instrument it draws nothing.
func analyticsDays(from, to time.Time) (first, last int64) {
	first, last = epochDay(from), epochDay(to)
	if first > last {
		first, last = last, first
	}
	return first, last
}

// AnalyticsPaths is the grid: one row per path over the window, ordered by
// views, with a cell per action that happened on it.
//
// The rate is the only reason a column exists — the sessions that did the
// action over the sessions that saw the page — so both halves are summed the
// same way and the ratio is between two numbers of the same kind. Summed across
// days means a session that came back on two days counts twice, which is the
// unit the rollup is keyed in and the unit the strip uses: what cannot be
// recovered once `analytics_seen` is purged is exactly what would make it
// anything else.
//
// Every action is carried, not just the pinned ones. The columns are the pinned
// ones, but a path row opens onto everything that happened on it, and a second
// read for the fold would be a second answer to what happened on a page.
func (s *Store) AnalyticsPaths(from, to time.Time) ([]model.PathRow, error) {
	first, last := analyticsDays(from, to)
	rows, err := s.rdb.Query(`SELECT path, action, SUM(events), SUM(sessions)
FROM analytics_rollup WHERE day BETWEEN ? AND ? GROUP BY path, action`, first, last)
	if err != nil {
		return nil, fmt.Errorf("read analytics paths: %w", err)
	}
	defer rows.Close()

	index := make(map[string]*model.PathRow)
	var found []*model.PathRow
	for rows.Next() {
		var path, action string
		var events, sessions int64
		if err := rows.Scan(&path, &action, &events, &sessions); err != nil {
			return nil, err
		}
		row := index[path]
		if row == nil {
			row = &model.PathRow{Path: path}
			index[path] = row
			found = append(found, row)
		}
		// page_view is the Views column rather than a cell. It is counted like
		// any other action and is never offered as one, so a grid that carried
		// it would draw every page converting at 100% in a column of its own.
		if action == actionPageView {
			row.Views, row.Sessions = events, sessions
			continue
		}
		if row.Actions == nil {
			row.Actions = make(map[string]model.ActionCell)
		}
		row.Actions[action] = model.ActionCell{Events: events, Sessions: sessions}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]model.PathRow, 0, len(found))
	for _, row := range found {
		for name, cell := range row.Actions {
			// The denominator is only known once the whole path has been read —
			// a group by answers in whatever order it likes, and half the action
			// names in an alphabet sort before `page_view`.
			//
			// No denominator is no rate, and the cell keeps its counts: a path
			// with actions and no page views is a real thing (a tracker firing
			// on a route the page view never reached), and inventing 0% for it
			// would be a conversion figure guard made up. The row's own zero
			// sessions is what says so.
			if row.Sessions > 0 {
				cell.Rate = float64(cell.Sessions) / float64(row.Sessions)
				row.Actions[name] = cell
			}
		}
		out = append(out, *row)
	}
	// Ordered here rather than in SQL because the ordering key is the page_view
	// row's events, which is one row of the group the sort is over. Ties break
	// on the path so the grid does not reshuffle under somebody reading it.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Views != out[j].Views {
			return out[i].Views > out[j].Views
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// AnalyticsSummary is the strip: the window, and the one of equal length
// immediately before it.
//
// The previous window is computed here rather than asked for twice, because two
// windows that do not line up are a change figure nobody can check — and equal
// length is the only comparison that means anything.
func (s *Store) AnalyticsSummary(from, to time.Time) (model.AnalyticsSummary, error) {
	first, last := analyticsDays(from, to)
	span := last - first + 1
	window, err := s.analyticsWindow(first, last)
	if err != nil {
		return model.AnalyticsSummary{}, err
	}
	previous, err := s.analyticsWindow(first-span, first-1)
	if err != nil {
		return model.AnalyticsSummary{}, err
	}
	return model.AnalyticsSummary{Window: window, Previous: previous}, nil
}

// analyticsWindow is the four numbers over one span of days.
//
// Views and actions are sums over the rollup, which is exact for as long as the
// rollup is kept. Sessions come from `analytics_seen`, because that is the only
// table that knows a session saw three pages rather than three sessions seeing
// one — summing the rollup's per-path sessions would count a visit once per
// page and make views per session read 1.0 forever.
//
// That is the number this window is bounded by: the seen rows are purged behind
// the rollup, so the strip can only count sessions as far back as they go.
func (s *Store) analyticsWindow(first, last int64) (model.AnalyticsWindow, error) {
	var out model.AnalyticsWindow
	var actions int64
	err := s.rdb.QueryRow(`SELECT
  COALESCE(SUM(CASE WHEN action = ? THEN events END), 0),
  COALESCE(SUM(CASE WHEN action <> ? THEN events END), 0)
FROM analytics_rollup WHERE day BETWEEN ? AND ?`,
		actionPageView, actionPageView, first, last).Scan(&out.Views, &actions)
	if err != nil {
		return model.AnalyticsWindow{}, fmt.Errorf("read analytics window: %w", err)
	}
	// Distinct per day and then added, which is the unit the rollup is keyed in:
	// a session that came back tomorrow is tomorrow's session too, and counting
	// it once over a month would be a denominator that shrinks the longer the
	// window somebody picks.
	err = s.rdb.QueryRow(`SELECT COALESCE(SUM(sessions), 0) FROM
(SELECT COUNT(DISTINCT session) AS sessions FROM analytics_seen WHERE day BETWEEN ? AND ? GROUP BY day)`,
		first, last).Scan(&out.Sessions)
	if err != nil {
		return model.AnalyticsWindow{}, fmt.Errorf("count analytics sessions: %w", err)
	}
	// No sessions is no ratio. An empty window is silence rather than zero, and
	// the two numbers above are what the page draws the dash from.
	if out.Sessions > 0 {
		out.ViewsPerSession = float64(out.Views) / float64(out.Sessions)
		out.ActionsPerSession = float64(actions) / float64(out.Sessions)
	}
	return out, nil
}
