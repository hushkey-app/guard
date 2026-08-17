# Browser telemetry

Guard can take OpenTelemetry from a browser, and the point of doing it *here*
rather than in a separate analytics tool is trace context: a browser span and
the API span it caused share a trace id, so the waterfall on `/traces` shows
click → fetch → handler → SQL as one picture. Splitting them across two systems
loses exactly the join that makes the data worth having.

The door is separate; the house is not.

## Why not just point the browser at `/v1/traces`

That endpoint is authenticated with `GUARD_OTEL_SECRET` (or `GUARD_TOKEN`),
accepts 16MB bodies, and
files events under whatever `service.name` the payload claims. A browser can
hold no secret — anything you ship to it is public — so pointing a browser at
it means either disabling the token or publishing it. Either way, anyone who
views source can post spans claiming to be your `api` service, and every panel,
saved view and uptime number on the dashboard is now something a stranger can
write to.

`/v1/rum/traces` and `/v1/rum/logs` are the browser's door:

| | `/v1/traces` | `/v1/rum/traces` |
|---|---|---|
| auth | `GUARD_OTEL_SECRET` or `GUARD_TOKEN` | none — it is public by nature |
| identity | from the payload | **assigned by guard**, payload ignored |
| body | 16 MB | 256 KB |
| rate | unlimited | 120 a minute per address |
| enabled | always | only when `GUARD_RUM_ORIGINS` is set |
| signals | logs, traces, metrics | traces, logs |

## Deployment: relay, don't expose

Guard runs inside the VPC. The browser is outside it. There are two ways to
bridge that and only one of them is good.

**Relay through the application you already expose.** The browser posts to your
own origin; that handler forwards to guard inside the VPC.

```
browser ──► https://app.example.com/api/telemetry ──► http://guard.internal:4318/v1/rum/traces
```

Nothing new is exposed, the relay is somewhere you already have rate limiting
and a session, and guard needs no CORS at all — a server-to-server post sends no
`Origin`, which the intake allows precisely so this shape works.

```go
// In your app, next to your other handlers.
http.HandleFunc("POST /api/telemetry", func(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	proxy, _ := http.NewRequest(http.MethodPost, "http://guard.internal:4318/v1/rum/traces", r.Body)
	proxy.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	// So guard's rate limit counts visitors, not your relay.
	proxy.Header.Set("X-Forwarded-For", clientIP(r))
	response, err := http.DefaultClient.Do(proxy)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.WriteHeader(response.StatusCode)
})
```

**Or expose only the RUM paths** through an ingress, if you would rather not
have a relay. Publish `/v1/rum/*` and nothing else — not `/v1/traces`, not the
dashboard, not `/api` — and set `GUARD_RUM_ORIGINS` to the exact origins you
serve from.

`X-Forwarded-For` is honoured for rate limiting, which means whatever sets it
must be trusted: a client can send that header itself. With a relay you control
it; with a bare ingress, make sure the proxy overwrites rather than appends.

## Configuration

```bash
GUARD_RUM_ORIGINS=https://app.example.com,https://www.example.com  # required; enables the intake
```

`GUARD_RUM_ORIGINS=*` is accepted and means what it says. It is right behind a
relay that has already decided who may reach guard, and wrong on anything
publicly reachable.

## The browser side

```js
import { WebTracerProvider, BatchSpanProcessor } from "@opentelemetry/sdk-trace-web";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { DocumentLoadInstrumentation } from "@opentelemetry/instrumentation-document-load";
import { FetchInstrumentation } from "@opentelemetry/instrumentation-fetch";
import { resourceFromAttributes } from "@opentelemetry/resources";

const provider = new WebTracerProvider({
  // Guard overrides service.name regardless — set it anyway so the same code
  // reports honestly if it is ever pointed at a collector that does not.
  resource: resourceFromAttributes({ "service.name": "browser" }),
  spanProcessors: [new BatchSpanProcessor(new OTLPTraceExporter({
    url: "/api/telemetry",   // your relay, or https://guard.example.com/v1/rum/traces
  }))],
});
provider.register();

registerInstrumentations({
  instrumentations: [
    new DocumentLoadInstrumentation(),
    new FetchInstrumentation({
      // THE line that makes this worth doing: without it the browser span and
      // the server span are two unrelated traces.
      propagateTraceHeaderCorsUrls: [/^https:\/\/app\.example\.com/],
    }),
  ],
});
```

Your API must accept and expose the header for that to survive CORS:

```
Access-Control-Allow-Headers: traceparent, tracestate
Access-Control-Expose-Headers: traceparent
```

Then a trace opened from `/traces` starts at the click and ends at the query.

## What to watch for

**Cardinality.** Every visitor is not an instance. Guard files all browser
events under one service and one instance on purpose — put the session, the
route and the release in *attributes*, where grouping is a choice, rather than
in `service.instance.id`, where it would grow the instances table without limit.

**Volume.** A busy site sends far more spans than a backend does. `Every span,
plotted` and the heatmap are the panels that will feel it first; sample in the
SDK (`TraceIdRatioBasedSampler`) rather than letting retention do it for you.

**Privacy.** Guard does not store visitor IP addresses — the address is used
for rate limiting and discarded. Whatever you put in span attributes *is*
stored, so keep tokens, emails and full query strings out of them. `url.full`
on a browser span often carries more than you think.

**The unassigned group.** Browser telemetry names no host guard is watching, so
it appears under "Not placed" on the overview. That is correct: a browser does
not run on one of your machines.
