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
	"fmt"
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

	for _, event := range b.Events {
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
		if _, err := raw.Exec(receivedNS, timestampNS, b.Session, path, event.Name, props); err != nil {
			return fmt.Errorf("store analytics event: %w", err)
		}
		result, err := seen.Exec(day, path, event.Name, b.Session)
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
		if _, err := rollup.Exec(day, path, event.Name, firstToday, firstToday); err != nil {
			return fmt.Errorf("roll up analytics event: %w", err)
		}
		if event.Name == actionPageView {
			continue
		}
		if _, err := discovered.Exec(event.Name, receivedNS, receivedNS); err != nil {
			return fmt.Errorf("record analytics action: %w", err)
		}
	}
	return tx.Commit()
}
