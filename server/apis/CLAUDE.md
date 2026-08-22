# server/apis — the JSON API

**Scoped gate:** `make apis && go build ./... && go test ./server/...`

`make apis` regenerates `server/apis/apis_gen.go` **and** the typed client at
`client/api/api_gen.go`. Both are generated — never hand-edit either. Forgetting
the regen is a route that exists in a file and nowhere else.

- One file per endpoint. The filename **is** the route:
  `index.api.go`, `index.post.api.go`, `id.dyn.delete.api.go`, `order.put.api.go`.
- `server/apis/metrics/series.api.go` is the smallest complete example — read it
  before writing a new one.
- `api.Define(api.Spec[Query, Body, Result]{...})`. The query type goes in
  `server/apis/contract/`, so one definition serves the decode and the handler.
- The store is reached through `store.Get()`, never a package global of your own.
- **Every write declares `Roles: []string{"admin"}`.** That is what the members
  allowlist is enforced against, and it is checked per request.
- **`Name:` is the generated client's method name**, spaces stripped, so it has
  to be unique across all 126 endpoints — `"Analytics"` was taken by the grid
  before the actions list wanted it.

The client compiles for wasm, so anything an endpoint returns must live in
`internal/telemetry/model` and must not drag SQLite in.
