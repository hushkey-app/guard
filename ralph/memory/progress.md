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

