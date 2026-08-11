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

Open <http://localhost:4318>. Guard retains the newest 10,000 events in memory;
change that with `-capacity`. Set `GUARD_TOKEN` to require a bearer token on
ingestion requests.

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

- `GET /api/logs?q=accepted&service=checkout&limit=100`
- `GET /api/events?signal=traces&limit=100`
- `GET /api/summary`
- `GET /healthz`

## Performance

The ingestion path relies on `net/http`'s request goroutines and adds each OTLP
batch to the bounded ring buffer under one lock. It does not create a goroutine
per telemetry record.

On an Apple M1 Pro, Go's benchmark harness reports these representative results:

| Path | Batch | Throughput | Effective time/op |
| --- | ---: | ---: | ---: |
| Handler, sequential | 1 log | ~160k events/s | ~6.2 µs |
| Handler, sequential | 100 logs | ~572k events/s | ~175 µs |
| Handler, parallel | 100 logs | ~1.37M events/s | ~73 µs |
| Full HTTP, parallel | 1 log | ~72k events/s | ~13.9 µs |
| Full HTTP, parallel | 100 logs | ~1.48M events/s | ~68 µs |

The parallel time/op values are throughput-derived benchmark timings, not
request-latency percentiles. These measure in-memory ingestion, not durable
storage or WAN latency. Reproduce them on your machine with:

```bash
go test ./internal/ingest -run '^$' -bench 'BenchmarkOTLPLogs' -benchmem
```

## Scope

The first release intentionally uses bounded memory: no database, queue, or
background services. Persistence, retention policies, trace waterfalls, and
metric aggregation belong in later releases after the ingestion contract and
operator experience are proven.
