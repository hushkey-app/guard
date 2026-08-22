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
	"fmt"
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
