# Guard

Guard is the lightweight telemetry watchtower for the Hushkies pack. It imports
[howl-go](https://github.com/mirairoad/howl-go), accepts standard OTLP/HTTP, and
presents logs, traces, metrics, and reporting instances in one compact UI.

## Run

Requirements: Go 1.25+, Node 20+ for the CSS build.

```bash
npm install
make
./guard
```

Open <http://localhost:4318>. Guard writes telemetry to `guard.db` by default.
The Settings page controls time and row retention. Set `GUARD_TOKEN` to require
a bearer token on ingestion and settings mutations.

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
than a network filesystem for WAL mode.

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
