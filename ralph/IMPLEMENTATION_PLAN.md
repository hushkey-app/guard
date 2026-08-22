# Implementation plan — Analytics

Spec: `ralph/specs/analytics.md`. Loop rules: `ralph/PROMPT.md`.

**One task per iteration. First unchecked task whose dependencies are all
checked.** Tick the box only on green gates.

Legend: `[ ]` todo · `[x]` done · `[!]` blocked (reason on the line)

Every task below is sized to be one commit and ends with the repository
building, generating and testing clean. If a task turns out to be bigger than
that, split it *in this file* — add the halves as new lettered sub-tasks
directly beneath it, tick nothing, and exit. Re-planning is a legitimate
iteration.

---

## Phase A — storage

The spine. Nothing in phases B–D can be built or tested without it, and all of
it is testable with no browser and no HTTP.

- [x] **A1 — the types**
  - Depends: —
  - Files: `internal/telemetry/model/analytics.go`, `internal/telemetry/model/analytics_test.go`
  - Do: the JSON contract and the pure functions over it. `Beacon` (`s`, `p`,
    `u`, `r`, `e`), `TrackEvent` (`n`, `t`, `d`), `PathRow`, `ActionCell`,
    `Action`, `PathRule`, `AnalyticsHealth`. Plus `NormalisePath(string) string`
    and `ValidActionName(string) bool` and `Beacon.Validate() error` enforcing
    the edge limits from spec §6 (≤50 events, name ≤64 chars matching
    `[a-z0-9_.-]`, ≤8 props, prop value ≤200 chars, path ≤200 chars).
  - Note: this package must compile for `js/wasm` — it may not import
    `internal/telemetry` or anything that opens SQLite. See the package doc.
  - Done when: table-driven tests cover path normalisation (query dropped,
    hash dropped, trailing slash, lowercase, truncation) and every rejection
    `Validate` can return.
  - Verify: `go test ./internal/telemetry/model/ && GOOS=js GOARCH=wasm go build ./internal/telemetry/model/`

- [x] **A2 — the schema**
  - Depends: A1
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/model.go` (one call)
  - Do: `migrateAnalytics(db *sql.DB) error` creating `analytics_events`,
    `analytics_rollup`, `analytics_seen`, `analytics_sources`,
    `analytics_actions`, `analytics_path_rules` per spec §5, with their
    indexes and primary keys. Call it from `Store.migrate` beside the other
    `migrate*` calls, with a comment in the house style saying what it is for.
  - Done when: opening a fresh store creates the tables, and opening an
    existing one twice is a no-op.
  - Verify: `go test ./internal/telemetry/ -run Analytics`

- [x] **A3 — the rollup writer**
  - Depends: A2
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/analytics_test.go`
  - Do: `Store.AddAnalytics(b model.Beacon) error`. One transaction per
    beacon, through `Store.db` (the writer). For each event: insert the raw
    row, `INSERT OR IGNORE` into `analytics_seen`, then the rollup UPSERT
    incrementing `sessions` only when `changes() == 1` — spec §5.
  - Done when: a test proves the distinct-session count is **exact** — the
    same session firing the same action on the same path twice increments
    `events` by two and `sessions` by one; two sessions increment `sessions`
    by two; the same session on the next day increments it again.
  - Verify: `go test ./internal/telemetry/ -run Analytics`

- [x] **A4 — the caps**
  - Depends: A3
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/analytics_test.go`
  - Do: the two cardinality ceilings from spec §4. Distinct paths per day over
    1,000 roll into the literal path `(other)`. Distinct action names over 200
    are **refused** — not renamed, not truncated — and counted. Both counters
    readable for the health endpoint.
  - Done when: a test drives past each ceiling and asserts the overflow
    behaviour and the counter, including that a refused action name never
    appears in `analytics_actions`.
  - Verify: `go test ./internal/telemetry/ -run Analytics`

- [x] **A5 — path rules**
  - Depends: A4
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/analytics_test.go`
  - Do: `analytics_path_rules` CRUD, ordered, first match wins, applied at
    ingest inside `AddAnalytics` so they shape what is stored. A rule is a glob
    pattern and a replacement (`/users/*` → `/users/:id`). Plus
    `Store.PreviewPathRules([]string) []string` for the prove-before-store
    dialog.
  - Done when: tests cover order precedence, no-match passthrough, and that
    changing a rule does not rewrite history.
  - Verify: `go test ./internal/telemetry/ -run Analytics`

- [x] **A6 — the read**
  - Depends: A5
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/analytics_test.go`
  - Do: `Store.AnalyticsPaths(from, to time.Time) ([]model.PathRow, error)` —
    the grid. One row per path with views, sessions, and a cell per action
    carrying count and the rate (sessions-that-did-it ÷ sessions-that-saw-the-page).
    A path where an action was never seen carries **no cell**, so the renderer
    can draw a dash rather than a zero. Ordered by views descending.
    Plus `Store.AnalyticsSummary` for the strip (sessions, views, per-session
    ratios, and the same four for the preceding window of equal length).
  - Done when: tests cover the rate arithmetic, the ordering, and that a
    never-seen action is absent rather than zero.
  - Verify: `go test ./internal/telemetry/ -run Analytics`

- [x] **A7 — retention**
  - Depends: A6
  - Files: `internal/telemetry/analytics.go`, `internal/telemetry/model.go`,
    `internal/telemetry/model/model.go`, `internal/telemetry/analytics_test.go`
  - Do: two new columns on the `settings` row — analytics rollup days
    (default 90) and analytics seen days (default 7) — with the same
    validation shape the existing two have, and a purge that runs beside the
    existing sweep. Raw `analytics_events` follows the existing
    `retention_hours`. **No environment variable.**
  - Done when: a test proves rollup rows survive an events purge, that seen
    rows behind the window are deleted, and that the rollup counts stand after
    the seen rows have gone.
  - Verify: `go test ./internal/telemetry/`

## Phase B — the door and the script

- [x] **B1 — the intake**
  - Depends: A7
  - Files: `internal/ingest/analytics.go`, `internal/ingest/rum.go` (mount only),
    `internal/ingest/analytics_test.go`
  - Do: `POST /v1/rum/events`, mounted from the existing `RegisterBrowser` so
    it shares `GUARD_RUM_ORIGINS`, the rate limiter, the 256 KB
    `MaxBytesReader` and the CORS preflight. Decode `model.Beacon`, call
    `Validate`, call `Store.AddAnalytics`. **Add no new configuration.**
  - Done when: tests cover a disallowed origin (403), a missing origin
    (allowed — the relay case), an over-budget body, the rate limit, a beacon
    failing each edge limit, and the happy path landing rows in the rollup.
  - Verify: `go test ./internal/ingest/`

- [x] **B2 — the tracker**
  - Depends: B1
  - Files: `internal/ingest/track.js`, `internal/ingest/analytics.go`,
    `internal/ingest/analytics_test.go`
  - Do: the script from spec §6, `go:embed`ed and served at
    `GET /v1/rum/track.js` with a long cache header. Under 2 KB minified, zero
    dependencies, no build step. `page_view` on load and on
    `pushState`/`replaceState`/`popstate`; `guard.track(name, props)`; one
    delegated click listener reading `[data-guard-track]`; batching with a
    5-second timer and a `visibilitychange:hidden` flush through
    `navigator.sendBeacon`; session id as 16 random bytes in `sessionStorage`
    with a 30-minute idle expiry; nothing at all under
    `navigator.doNotTrack === "1"`; a no-op after two consecutive 4xx.
  - Note: this file is deliberately **not** under `client/public/` — that
    directory is the dashboard's own JavaScript and every file in it needs a
    Tailwind `@source` line. See spec §6.
  - Done when: `node --check internal/ingest/track.js` passes, the endpoint
    serves it, and a Go test asserts the byte budget so a future edit that
    doubles it fails loudly.
  - Verify: `node --check internal/ingest/track.js && go test ./internal/ingest/`

- [x] **B3 — health**
  - Depends: B2
  - Files: `internal/telemetry/analytics.go`, `internal/ingest/analytics.go`,
    `internal/telemetry/analytics_test.go`
  - Do: the rejection counters — malformed beacons, over-cap action names,
    over-cap paths, last event received at — surfaced as
    `model.AnalyticsHealth`. A tracker being silently dropped is the failure
    mode people take weeks to notice, so it gets a number on the page.
  - Verify: `go test ./internal/telemetry/ ./internal/ingest/`

## Phase C — the API

Read an existing endpoint first (`server/apis/metrics/series.api.go` is the
smallest) — the file layout, the naming and the generated route table are a
convention, not a choice.

- [x] **C1 — the grid endpoint**
  - Depends: B3
  - Files: `server/apis/analytics/index.api.go`, `server/apis/contract/` (one query type)
  - Do: `GET /api/analytics` returning the strip and the grid for a window.
  - Done when: `make apis` regenerates `server/apis/apis_gen.go` and
    `client/api/api_gen.go` with the route in it, and both compile — including
    for wasm.
  - Verify: `make apis && go build ./... && GOOS=js GOARCH=wasm go build ./wasm`

- [x] **C2 — the actions endpoints**
  - Depends: C1
  - Files: `server/apis/analytics/actions.api.go`,
    `server/apis/analytics/actions.post.api.go`,
    `server/apis/analytics/id.dyn.delete.api.go`
  - Do: list discovered actions; pin / unpin / reorder; delete an action and
    its rollup rows. Writes declare `Roles: []string{"admin"}` like every other
    write endpoint.
  - Verify: `make apis && go build ./... && go test ./server/...`

- [x] **C3 — the rules endpoints**
  - Depends: C2
  - Files: `server/apis/analytics/rules.api.go`, `server/apis/analytics/rules.post.api.go`,
    `server/apis/analytics/preview.post.api.go`
  - Do: path-rule CRUD plus the preview that proves a rule against the last
    100 distinct paths before it is stored.
  - Verify: `make apis && go build ./... && go test ./server/...`

- [x] **C4 — the health endpoint**
  - Depends: C3
  - Files: `server/apis/analytics/health.api.go`
  - Verify: `make apis && go build ./...`

## Phase D — the page

**Before the first task in this phase**, read `.claude/skills/guard-ui/SKILL.md`
and `docs/shadcn-templ.md` in full. They exist because this is the part that
fails silently.

- [ ] **D1 — the glyph**
  - Depends: C4
  - Files: `client/ui/icons.go`
  - Do: one row in the `Icons` registry named `analytics`. Inner paths only;
    only the attributes that differ from the default.
  - Verify: `go build ./...`

- [ ] **D2 — the sidebar**
  - Depends: D1
  - Files: `client/ui/nav.go`
  - Do: `/analytics` appended to the `Signals` group in `navOrder`, and
    `"/analytics": "analytics"` in `navIcons`. Two lines. The row label comes
    from the route table, so nothing else is edited.
  - Verify: `make test`

- [ ] **D3 — the page skeleton**
  - Depends: D2
  - Files: `client/pages/analytics/index.client.templ`
  - Do: the route and its three states — analytics off (`GUARD_RUM_ORIGINS`
    unset: say so, name the variable, link to Settings → Configuration),
    configured but empty (the Install band alone, with this instance's script
    tag and a copy button), and the live layout's empty containers. Page takes
    no arguments and reads everything from `ctx`; imports `core/router`, never
    `core/app`.
  - Verify: `go run github.com/mirairoad/howl-go/core/cmd/howl check && make test`

- [ ] **D4 — the renderer's wiring**
  - Depends: D3
  - Files: `client/public/analytics.js`, `Makefile` (one `@source` line),
    `client/public/modules_test.mjs` if it enumerates modules
  - Do: the module, going through `client/public/store.js` for its data like
    every other page — `ensure` on mount, `set` on a save. It draws nothing
    yet beyond the strip's containers; this task is the wiring and the build
    plumbing, which is the part that silently does nothing when skipped.
    **The `@source "../public/analytics.js";` line in the `css` target is not
    optional** — without it Tailwind emits none of the classes this file names.
  - Verify: `make css && make test`
  - Note: `make css` needs Node and network. If it cannot run, mark this `[!]`
    and say so — do not guess at `client/public/app.css`.

- [ ] **D5 — the strip**
  - Depends: D4
  - Files: `client/public/analytics.js`, `client/ui/components/` (a template if the chrome is shared)
  - Do: sessions, page views, views per session, actions per session, each
    with the change against the previous window of equal length. Panels are
    cloned from `<template>` elements in `client/ui/components` so every class
    stays where Tailwind looks.
  - Verify: `make css && make test`

- [ ] **D6 — the grid**
  - Depends: D5
  - Files: `client/public/analytics.js`, `client/ui/components/`
  - Do: **the feature.** One row per path in fixed columns: path, views,
    sessions, then one column per pinned action carrying count and rate.
    Sortable by any column. A **dash, never a zero**, where an action was never
    seen on that path. A class assembled from a variable is never emitted by
    Tailwind, so anything computed goes in an inline style.
  - Verify: `make css && make test`

- [ ] **D7 — the fold**
  - Depends: D6
  - Files: `client/public/analytics.js`, `client/ui/components/`
  - Do: a path row opens onto its sparkline (an existing renderer in
    `charts.js`), every action on that path pinned or not with a pin button,
    and the top referrer hosts and campaigns into it. Shut is the default;
    open is what is remembered — the same rule `/cluster` keeps.
  - Verify: `make css && make test`

- [ ] **D8 — the controls**
  - Depends: D7
  - Files: `client/public/analytics.js`, `client/pages/analytics/index.client.templ`
  - Do: the Actions list (pin, reorder, delete with a confirm that says the
    rollup rows go too) and the Paths rules editor with its live preview.
  - Verify: `make css && make test`

## Phase E — the joins

- [ ] **E1 — sources**
  - Depends: D8
  - Files: `internal/telemetry/analytics.go`, `internal/ingest/analytics.go`,
    `server/apis/analytics/`, `client/public/analytics.js`
  - Do: the `analytics_sources` rollup written at ingest — `utm_source`,
    `utm_medium`, `utm_campaign` and the referrer **host only** — and its list
    on the page. Four strings, extracted client-side before the query string is
    dropped. Not a props system.
  - Verify: `make css && make test`

- [ ] **E2 — the session id becomes a join key**
  - Depends: E1
  - Files: `internal/telemetry/views.go`, `internal/ingest/analytics.go`,
    `internal/telemetry/views_test.go`
  - Do: one entry in `indexedAttributes` for `rum.session_id`, and the tracker
    emitting it on browser spans, so a path with a bad rate can be walked into
    `/traces`. Adding a sixth indexed attribute is an entry in that list and
    nothing else — if it turns out to need more, stop and record why.
  - Verify: `make test`

- [ ] **E3 — the drill**
  - Depends: E2
  - Files: `client/public/analytics.js`
  - Do: the link from an opened path row into `/traces` filtered to that
    path's sessions.
  - Verify: `make css && make test`

- [ ] **E4 — backup**
  - Depends: E3
  - Files: `internal/telemetry/backup.go`, `internal/telemetry/backup_test.go`
  - Do: `analytics_actions` and `analytics_path_rules` into `backupTables` —
    they say how guard is configured. `analytics_events`, `analytics_rollup`,
    `analytics_seen` and `analytics_sources` into `backupExcluded`, written out
    beside them, for the same reason logs are excluded.
  - Verify: `go test ./internal/telemetry/`

- [ ] **E5 — retention on the storage page**
  - Depends: E4
  - Files: `client/public/config.js` or the storage settings page, `server/apis/settings/`
  - Do: the two analytics retention numbers from A7 typed on
    Settings → Data storage, beside the two that are already there. Applied
    when saved, not at the next start.
  - Verify: `make css && make test`

- [ ] **E6 — the document of record**
  - Depends: E5
  - Files: `docs/analytics.md`, `CLAUDE.md`, `docs/browser-telemetry.md`
  - Do: write `docs/analytics.md` in the voice of the other docs — what the
    rules are and *why*, not a tour of the code. Add the `## Analytics`
    section to `CLAUDE.md` and the one-line file map entries. Add
    `/v1/rum/events` and `/v1/rum/track.js` to the table and the relay example
    in `docs/browser-telemetry.md`.
  - Say plainly, because each is a thing somebody will otherwise learn the hard
    way: guard counts **sessions**, not people; once `analytics_seen` is purged
    the counts stand and cannot be recomputed under a new path rule; and the
    relay deployment is recommended first because ad blockers block the direct
    path.
  - Verify: `make test`

- [ ] **E7 — the benchmark**
  - Depends: E6
  - Files: `internal/ingest/analytics_bench_test.go`
  - Do: a benchmark beside `internal/ingest/benchmark_test.go` measuring
    beacon ingest against the single-writer store — three writes per event
    against one SQLite writer is the risk the spec names, and an unmeasured
    risk is a rumour.
  - Verify: `go test ./internal/ingest/ -bench . -benchtime 10x`

---

## When everything is ticked

Append `ALL TASKS COMPLETE` on its own line at the end of
`ralph/memory/progress.md`. The loop script greps for it and stops.

Then write a final block in `ralph/memory/progress.md` naming: the branch, the
commit range, anything left in `ralph/memory/questions.md`, and any task marked
`[!]`. That block is the handover, and a person reads it before reading a single
diff.
