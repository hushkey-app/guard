# Guard

An OTLP/HTTP telemetry receiver with a server-rendered dashboard. Logs, traces and metrics
land over OTLP, get stored in SQLite, and are explored from a howl-go UI.

```
main.go                     wiring: store, howl-go app, ingest routes
internal/ingest/otlp.go     OTLP/HTTP + the JSON API the dashboard reads
internal/telemetry/model.go storage, retention, snapshots
client/pages/               the dashboard — howl-go filesystem routes
client/ui/ui.templ          shared page furniture (nav, stats, filter bar, pagination)
client/public/guard.js      the dashboard's own client code (tables, filters, detail panel)
client/styles/app.css       stylesheet source — compiles to client/public/app.css
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
  the classes it finds in the `@source` globs (pages, `client/ui`, and `guard.js` — see
  the `css` target). Use a class nothing used before and you must run `make css`, which
  is the one target that needs Node.

Interactive shadcn components (select, dialog, popover) need the upstream JS bundle,
which the direct-import workflow does not serve. Guard uses static components only;
filter controls are native elements wearing `cn-native-select` / the Input component,
because `guard.js` drives them through `.value`.

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
