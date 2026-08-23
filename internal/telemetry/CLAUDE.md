# internal/telemetry — the store

**Scoped gate:** `go test ./internal/telemetry/ ./internal/telemetry/model/`
Narrow it while iterating: `go test ./internal/telemetry/ -run Analytics`. That
`-run` is a name match, so a new analytics test whose name does not carry the
word is a test the gate silently never runs.

Full `make test` before the commit, always — this package is imported by the
API tree and a change here can only break compilation over there.

## The three rules that break silently

- **One writer.** Every `Exec` and every `Begin` goes through `s.db`. Reads go
  through `s.rdb`, which is a pool. A write on `rdb` is a lock error under
  load and passes every test.
- **Migrations are `migrate*(db)` functions**, each called from `Store.migrate`
  with a comment saying what it is for. SQLite has no `ADD COLUMN IF NOT
  EXISTS`, so an ALTER reads `pragma_table_xinfo` first — `table_info` hides
  VIRTUAL generated columns and will make you add the same column twice.
- **Nothing interpolates caller text into SQL.** A field is looked up in
  `model.Columns` or bound as a JSON path; values are always parameters. This
  is the only reason a dashboard user may compose a query at all.

Types live in `model/`, storage lives here — see that package's doc for why.

One `Store` serves the whole product, so a method name is taken sooner than it
looks: `Store.Actions` is a machine's stored commands, which is why the
analytics half is `AnalyticsActions`, `PinAnalyticsActions` and
`DeleteAnalyticsAction`.

Analytics work: `ralph/specs/analytics.md` §5, plan tasks A1–A7.
