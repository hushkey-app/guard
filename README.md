# Guard

Guard is the lightweight telemetry watchtower for the Hushkies pack. It imports
[howl-go](https://github.com/mirairoad/howl-go), accepts standard OTLP/HTTP, and
presents logs, traces, metrics, and reporting instances in one compact UI.

## Run

Requirement: Go 1.25+.

```bash
make
./guard
```

The compiled stylesheet is committed and embedded in the Go binary, so running
or building Guard does not require Node.js or contact a CSS CDN.

## Interface

The dashboard is built from [shadcn-templ](https://github.com/axadrn/shadcn-templ)
v2 components — card, table, button, badge, input, field, item, alert, empty —
imported directly as Go packages and rendered by Howl on the server and in
WebAssembly. Filter controls stay native, because `guard.js` drives them.

One stylesheet, `client/public/app.css`, holds Tailwind, the shadcn base and the
`style-nova` component style. It is committed; rebuild it with `make css` after
using a Tailwind class no source used before. That target is the only thing in
Guard that needs Node, and `make`, `make dev` and `docker build` never run it.

Agents should read `docs/shadcn-templ.md` for the offline API and integration
digest before changing the UI.

Open <http://localhost:4318>. Guard writes telemetry to `guard.db` by default.
The Settings page controls time and row retention. Set `GUARD_TOKEN` to require
a bearer token on ingestion and settings mutations.

## Signing in

Guard is open until it is given OAuth credentials. Set Google's, Apple's, or
both, and the dashboard goes behind a login page with a button per configured
provider:

```bash
GUARD_GOOGLE_CLIENT_ID=…apps.googleusercontent.com
GUARD_GOOGLE_CLIENT_SECRET=…
GUARD_ADMIN_EMAIL=you@example.com          # always allowed, always an admin
GUARD_AUTH_BASE_URL=https://guard.example.com
```

The redirect URI to register is `<base URL>/auth/google/callback`, and
`<base URL>/auth/apple/callback` for Apple — whose `GUARD_APPLE_CLIENT_ID` is a
Services ID, and whose signing key goes in `GUARD_APPLE_PRIVATE_KEY` or
`GUARD_APPLE_PRIVATE_KEY_FILE` beside `GUARD_APPLE_TEAM_ID` and
`GUARD_APPLE_KEY_ID`.

Proving who you are is not the same as being allowed in: the guest list is
Settings → Members, plus whatever `GUARD_ADMIN_EMAIL` names. A member reads
everything; an admin can also change things, including the list. Sessions last
seven days (`GUARD_AUTH_SESSION_TTL`).

The OTLP endpoints stay outside all of it — an exporter holds `GUARD_TOKEN`, not
a browser session — so switching sign-in on never stops a collector. See
[docs/auth.md](docs/auth.md).

## Dashboard rendering

Home, Logs, Metrics, Traces, and Settings are howl-go `.client.templ` routes.
After the lazy WebAssembly renderer is ready, sidebar navigation renders their
data-free shells locally with no page HTML request. Each route's `Mount()` then
starts its initial JSON request; the live watcher refreshes only the mounted
page's data every three seconds. `Unmount()` invalidates requests from the page
being left.

Summary counts and instance totals are maintained transactionally during
ingestion, so `/api/summary` does not scan the events table. Filter facets are
loaded on mount and cached in the browser for 60 seconds rather than polled on
every live tick.

Configuration is available as flags or environment variables:

| Flag | Environment | Default |
| --- | --- | --- |
| `-db` | `GUARD_DB_PATH` | `guard.db` |
| `-retention-hours` | `GUARD_RETENTION_HOURS` | `24` |
| `-max-events` | `GUARD_MAX_EVENTS` | `1000000` |
| `-addr` | — | `:4318` |

## Docker and persistent storage

The included Compose file stores SQLite, its WAL, and shared-memory files on one
named volume:

```bash
docker compose up --build
```

The essential configuration is:

```yaml
environment:
  GUARD_DB_PATH: /data/guard.db
volumes:
  - guard-data:/data
```

Mount the directory, not only the database file, because SQLite creates
`guard.db-wal` and `guard.db-shm` beside it. Use a local Docker volume rather
than a network filesystem for WAL mode. The `vault` service is the same image
with `entrypoint: guard-vault`, on the same volume — it needs the key file
beside the database, and it refuses to start without it.

## Secrets

Guard also stores the environment variables your applications boot with —
key and value, per environment, encrypted at rest — on a `/secrets` page that
imports and exports whole `.env` files.

They are served by a **second binary**, `guard-vault`, on the same database and
key file. That is the point of it: a bad dashboard release, a restart or a
rollback must not stop a container from booting.

```bash
guard-vault -db /data/guard.db     # :4319
```

An application holds one key, minted on the page and shown once:

```bash
GUARD_VAULT_URL=http://vault.internal:4319
GUARD_VAULT_KEY=gsk_production_…
```

The key carries its own environment, so there is nothing else to configure and
a staging token cannot be pointed at production. Locally,
`guard-vault fetch -env local > .env` skips the server entirely.

See `docs/secrets.md`.

## Send OpenTelemetry

Point any OTLP/HTTP exporter at Guard:

```bash
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
```

Guard accepts protobuf and JSON, optionally gzip-compressed, at:

- `POST /v1/logs`
- `POST /v1/traces`
- `POST /v1/metrics`

For small clients that do not use an OpenTelemetry SDK:

```bash
curl -X POST http://localhost:4318/api/logs \
  -H 'content-type: application/json' \
  -d '{"service":"checkout","instance":"local-1","severity":"INFO","message":"order accepted"}'
```

Read APIs:

- `GET /api/logs?q=accepted&service=checkout&severity=ERROR&from=<RFC3339>`
- `GET /api/events?signal=traces&service=checkout&from=<RFC3339>&to=<RFC3339>`
- `GET /api/events/{id}`
- `GET /api/metrics/series?name=http.server.duration&group_by=service`
- `GET /api/facets`
- `GET /api/summary`
- `GET|PUT /api/settings`
- `POST /api/settings/purge`
- `GET /healthz`

## Operator interface

- pause or resume three-second live refresh without stopping ingestion
- filter logs and traces by preset/custom time, service, status, and text
- click any row to inspect identifiers, timing, resource data, and attributes
- chart metrics and group series by metric, service, or instance
- compare latest, average, minimum, maximum, and point count per series
- configure persisted time and row retention from Settings

## Performance

The ingestion path relies on `net/http`'s request goroutines and one bounded
SQLite writer goroutine. Concurrent OTLP requests are coalesced into a single
transaction, flushing after 5 ms or 2,000 events. The bounded queue applies
backpressure, and a request returns only after its batch commits. Guard does not
create a goroutine per telemetry record. SQLite uses WAL mode so readers can
continue during writes.

Reproduce the ingestion benchmarks on your storage and hardware with:

```bash
go test ./internal/ingest -run '^$' -bench 'BenchmarkOTLPLogs' -benchmem
```

`BenchmarkOTLPLogsSQLiteParallel100` exercises the persistent WAL/batch path;
the other benchmarks use isolated in-memory SQLite databases. Docker volume
latency should still be measured on the deployment host.
