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
