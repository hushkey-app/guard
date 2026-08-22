# Progress

Append-only. One block per iteration, newest at the bottom. Eight lines maximum
per block — this file is read in full by every iteration, so a verbose entry
costs every future iteration context it could have spent working.

The last line of this file, when the plan is finished, is `ALL TASKS COMPLETE`
on its own. `ralph.sh` greps for it.

---

## seed — the harness
- commit: (uncommitted at time of writing)
- gates: build ok / howl not run / `go test ./...` ok
- notes: baseline is green on branch fix/valkey-telemetry-collector. The working
  tree has substantial unrelated in-progress changes — `git add` explicit paths
  only, never `-A`. Work happens on `feat/analytics`, which ralph.sh creates.

## A2 — the analytics schema, and its call from Store.migrate
- commit: feat(analytics): the tables, and which of them is the truth
- gates: build ok / howl ok / make test ok
- notes: `day` is an INTEGER of whole UTC days via the existing `epochDay`
  (uptime.go), not the TEXT the spec's example SQL implies — A3/A6/A7 must use
  it too. `analytics_seen` is WITHOUT ROWID (the row is its key). Raw rows carry
  both `received_at_ns` and `timestamp_ns`; A7 sweeps on guard's clock, never
  the browser's. Path rule patterns are UNIQUE, so A5's CRUD must answer a
  duplicate with words. A2's Files list omitted a test file — `analytics_test.go`
  exists now and A3 extends it. A1 left its own plan tick uncommitted; it is in
  this commit.

## A4 — the two cardinality ceilings, answered differently on purpose
- commit: b1bd30e
- gates: build ok / howl ok / make test ok
- notes: the ceilings are `maxAnalyticsPaths` (1000) and `maxAnalyticsActions`
  (200) in analytics.go. A path past the ceiling counts under the literal row
  `(other)` — but the **raw** row keeps the URL it arrived on, because that is
  what somebody reads to write A5's path rule; A6 reads the rollup, so it never
  sees the difference. A name past the ceiling is refused per event (the beacon
  around it still counts) — unlike Validate, which refuses the whole beacon.
  Counters are in-memory atomics on Store (`analytics analyticsCaps`, one field
  added to model.go), read by `Store.AnalyticsCaps()` for B3's health. A5 must
  apply its rules **before** `analyticsCountedPath`, which is the point of them.
  A3 left its own plan tick uncommitted; it is in this commit.

## A5 — the path rules, applied at ingest and proved before they are stored
- commit: (this one)
- gates: build ok / howl ok / make test ok
- notes: `PreviewPathRules` takes the **candidate rules** as well as the paths —
  the plan's `([]string) []string` could only preview the stored ones, and C3
  asks it to prove a rule *before* it is stored (see questions.md). Save is
  replace-the-whole-list (`SavePathRules`), which is the shape C3's single
  `rules.post` endpoint wants; a rule has no history to keep, unlike a command.
  Matching is `path.Match`, so `*` stops at `/`. Both halves are lowercased,
  because a pattern with a capital could never match a normalised path.

## A6 — the grid and the strip, read from the rollup
- commit: (this one)
- gates: build ok / howl ok / make test ok
- notes: `AnalyticsPaths` carries **every** action per path, not just the pinned
  ones — D7's fold needs them and a second read would be a second answer. Rate
  is computed after the whole path is read (a GROUP BY answers in any order, and
  most names sort before `page_view`); no page_view row means no denominator, so
  the cell keeps its counts at rate 0 and the row's own zero sessions is what
  says so. **The strip's Sessions can only come from `analytics_seen`** — the
  rollup is per path, so summing it counts one visit once per page — which means
  A7's seen window bounds how far back the strip can count. See questions.md.
  `model.AnalyticsWindow`/`AnalyticsSummary` are new in `model/analytics.go`,
  outside A6's Files list, because a type the API returns must live there.

## A7 — the two retention windows, and the sweep that applies them
- commit: (this one)
- gates: build ok / howl ok / make test ok
- notes: the two columns are on `settings` (ALTER in `migrateAnalytics`, defaults
  from `model.DefaultAnalytics*Days`), and `purgeAnalytics` runs inside the
  existing `Store.Purge` transaction — so the settings page's "removed" number
  still counts only the events table. **A save that omits the two leaves them
  alone**: the current storage form posts `{retention_hours, max_events}` only,
  and E5 is what types them, so UpdateSettings fills a zero from the stored row.
  `analytics_sources` sweeps on the rollup window too — the plan named three
  tables, and leaving the fourth growing would be a bug. See questions.md for
  the spec/plan disagreement about which two numbers these are.

## B1 — the intake, a third path on the door that was already there
- commit: (this one)
- gates: build ok / howl ok / make test ok
- notes: `POST /v1/rum/events` answers **204**, not 200 — a sendBeacon flush is
  usually a tab that is already gone, so there is no body worth writing. B2's
  "no-op after two consecutive 4xx" must read 204 as success. No Content-Type
  check at the door on purpose: sendBeacon posts `text/plain`, which is what
  keeps the closing-tab flush free of a preflight it would not live to finish.
  The existing `OPTIONS /v1/rum/{signal}` already covers the new path, so the
  mount is one line. Validate is called at the door *and* in the store: the
  door's answer to a bad beacon is 400, the store's is a 500, and B3's rejection
  counter belongs on the door's side of that line.
