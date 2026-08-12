# Guard

An OTLP/HTTP telemetry receiver with a server-rendered dashboard. Logs, traces and metrics
land over OTLP, get stored in SQLite, and are explored from a howl-go UI.

```
main.go                       wiring: store, howl-go app, ingest routes
internal/ingest/otlp.go       OTLP/HTTP + the JSON API the dashboard reads
internal/telemetry/model.go   storage, retention, snapshots
internal/telemetry/views.go   saved views: the query compiler and its CRUD
client/pages/                 the dashboard — howl-go filesystem routes
client/ui/ui.templ            shared page furniture (nav, stats, filter bar, pagination)
client/ui/components/         panel chrome, panel bodies, the view builder
client/public/core.js         helpers shared by the three client modules
client/public/charts.js       the panel renderers, hand-drawn SVG
client/public/views.js        the views page: panels, builder, live preview
client/public/guard.js        tables, filters, detail panel, the live tick
client/styles/app.css         stylesheet source — compiles to client/public/app.css
```

## The framework

Guard is built on **howl-go**. Its conventions are not guessable — Go's toolchain rejects
`_layout.templ` and `[id].templ`, so the routing, page and shell rules look different from
any JS framework.

**Read `llms.txt` from the howl-go module before touching anything under `client/pages/`.**
It is at `../howl-go/llms.txt` in this working copy (see the `replace` directive in
`go.mod`), and served at `/llms.txt` by the howl-go docs site.

`.mcp.json` in this repo wires up the framework's MCP server: `howl_conventions`,
`howl_check`, `howl_routes`, `howl_endpoints`, `howl_scaffold`. Call `howl_check` after
editing; run it from the shell with
`go run github.com/mirairoad/howl-go/core/cmd/howl check`.

The short version:

- `client/pages/**/index.templ` → routes; `*.client.templ` also renders through
  `client/public/views.wasm`; `app.templ` is the document shell; `layout.templ`
  wraps its directory. All reserved names.
- `fsroutes_gen.go`, `*_templ.go`, `views.wasm`, and `wasm_exec.js` are
  **generated** — `make` runs routes, templ, WASM, then the server build.
- Pages import `core/router` (never `core/app`), take no arguments, and read everything
  from `ctx`.
- Middleware is plain `func(http.Handler) http.Handler`.
- The JSON API lives in `server/apis/`, one file per endpoint, generated into
  `apis_gen.go` and a typed client at `client/api/`. OTLP stays raw in `internal/ingest`.

## The UI

The dashboard is **shadcn-templ v2** (`github.com/axadrn/shadcn-templ/v2`), imported
directly as Go packages. Read `docs/shadcn-templ.md` before changing it — it is the
offline digest of the exact beta pinned here; do not guess React shadcn APIs or fetch
the website first.

Two things are load-bearing and easy to break:

- **`<html class="dark style-nova">`.** `style-nova.css` nests every `cn-*` rule under
  `.style-nova`, so dropping that class silently unstyles every component.
- **`client/public/app.css` is a build artifact but is committed.** Tailwind only emits
  the classes it finds in the `@source` globs (pages, `client/ui`, and each JavaScript
  file by name — see the `css` target). Use a class nothing used before and you must run
  `make css`, which is the one target that needs Node. **A new file under
  `client/public/*.js` needs its own `@source` line**, and a class assembled from a
  variable (`` `col-span-${n}` ``) is never emitted at all — those go in inline styles.

Interactive shadcn components (select, dialog, popover) need the upstream JS bundle,
which the direct-import workflow does not serve. Guard uses static components only;
filter controls are native elements wearing `cn-native-select` / the Input component,
because `guard.js` drives them through `.value`.

## Views

A view is a stored query — filters, a grouping, an aggregation, a window — and running one
produces a `model.Frame`, which is a *result layout* rather than a chart. Panels declare
which layout they read (`model.Panels`), the compiler promises one (`model.ShapeOf`), and
the pair is checked in `ViewQuery.ValidateFor`. That seam is the whole design: adding a
panel that reads an existing shape is a renderer in `charts.js` and an entry in
`model.Panels` — no SQL, no endpoint, no migration.

- `internal/telemetry/views.go` compiles a query per shape. **Nothing there interpolates
  caller text into SQL** — a field is looked up in `model.Columns` or bound as a JSON path,
  and values are always parameters. Keep it that way; it is the only reason a dashboard
  user may compose queries at all.
- Grouping by an attribute is a `json_extract` over every candidate row. Five attributes
  (`http.route`, method, status, `client.address`, `db.system`) have generated columns and
  indexes, and both OpenTelemetry spellings of each resolve to the same column on purpose.
  Adding a sixth means an entry in `indexedAttributes` and nothing else.
- Percentiles are exact nearest-rank, computed with window functions, because SQLite has
  no percentile function and a silently interpolated p95 is a number people act on.
- The panels are cloned from `<template>` elements declared in `client/ui/components`, so
  panel chrome stays real shadcn markup and every class stays where Tailwind looks. The
  package doc explains why.

## Build and run

```bash
make            # generate + build WASM and server
make dev        # watch, rebuild, restart, reload the browser — use this, not `go run .`
make css        # rebuild client/public/app.css (needs Node; only after new classes)
./guard         # OTLP/HTTP + dashboard on :4318
go test ./...
```

`make dev` proxies :4318 in front of the restarting binary, so exporters posting
telemetry keep their connection target and a failed build keeps the last good binary
serving with the compiler error shown in the dashboard.

`GUARD_DB_PATH`, `GUARD_RETENTION_HOURS`, `GUARD_MAX_EVENTS`, `GUARD_TOKEN` configure it;
flags of the same names override.

## Local dependency

`go.mod` has `replace github.com/mirairoad/howl-go => ../howl-go` while the framework
changes are unreleased. **`docker build` will fail with this in place** — the Dockerfile
only copies guard's own source. Remove the replace and pin a published version before
building an image.

## Logging

`console.Setup` installs the tinted slog handler; the request logger tags `ip`/`ua` for
callers that are not one of guard's own pages — i.e. the exporters posting telemetry, which
is usually what you want to see. Log through `slog`, not `fmt.Printf`.
