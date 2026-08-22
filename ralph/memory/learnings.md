# Learnings

Traps, seeded from the existing code and from `CLAUDE.md`. Append a bullet when
something costs you more than a few minutes. Keep each to one or two lines —
this file is loaded every iteration and an essay here is a tax on all of them.

Much of what used to live in this file now lives in the per-folder `CLAUDE.md`
files (`internal/telemetry/`, `internal/telemetry/model/`, `internal/ingest/`,
`server/apis/`, `client/pages/`, `client/public/`, `client/ui/`). Those load
automatically when you touch a file in the directory, so put a folder-specific
trap **there**, and put a cross-cutting one here.

## Seeded

- `make test` runs `generate` first. The regenerated `fsroutes_gen.go`,
  `*_templ.go` and `client/api/api_gen.go` diffs are expected — commit them.
- `go.mod` has `replace github.com/mirairoad/howl-go => ../howl-go`. That
  directory must exist or nothing builds. It is present in this checkout.
- `make css` is the only target needing Node, and it `npm install`s on every
  run. No network means no `make css` — mark the task `[!]` rather than
  hand-editing `client/public/app.css`.
- `go test ./...` was green before the first iteration. If it is red and you did
  not make it red, say so in progress.md and stop rather than "fixing" someone
  else's in-progress work.
- Twenty-two `GUARD_*` variables, and that is the whole list. A number that
  looks like a knob is a row in `settings` or a constant beside its reader.
- Guard's prose rule, and it applies to code comments too: say **why a rule
  exists**, not what a line does.
- A per-store counter needs a field on `Store` in `internal/telemetry/model.go`,
  which is outside most tasks' `Files` list. One line there is unavoidable —
  say so in progress.md rather than inventing a package-level variable that two
  stores in one test would share.
- A glob pattern is validated by probing it — `path.Match(pattern, "/")` returns
  `ErrBadPattern` for a malformed one — which is how a rule that could never
  fire is refused while somebody is still typing it.
- `analytics_rollup` cannot answer a site-wide session count. It is keyed
  (day, path, action), so summing sessions across paths counts one visit once
  per page it read; only `analytics_seen` knows the difference.
- Adding a column to an existing table: `tableColumns(db, name)` in `backup.go`
  is the shared reader — no need to hand-roll the `pragma_table_xinfo` loop that
  `cluster.go` and `views.go` predate it with. And `Store.Settings` selects its
  columns by name, so a new one is a line there and a line in `UpdateSettings`.
