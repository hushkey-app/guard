// Command seed posts a plausible hour of telemetry to a running guard.
//
// It exists for the state every new instance starts in and every dashboard
// hits eventually: an empty store, and no obvious way to tell whether the
// panels are broken or the data simply is not there. `make seed` fills it with
// something worth drawing — a slow tail, a few errors, two services, a
// wandering gauge — over the real OTLP/HTTP endpoint, in protobuf, exactly as
// an exporter would.
//
// Not part of the server build: its own main package, run with `go run`.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

type route struct {
	path     string
	base     float64
	spread   float64
	weight   int
	database bool
}

// Weights and durations that make the panels say something: /health is
// frequent and fast, /checkout is rare and slow, and one request in twelve is
// slow enough to put a second hump in the histogram.
var routes = []route{
	{path: "/checkout", base: 180, spread: 260, weight: 5, database: true},
	{path: "/search", base: 40, spread: 60, weight: 9, database: true},
	{path: "/cart", base: 60, spread: 40, weight: 6, database: true},
	{path: "/health", base: 2, spread: 2, weight: 12},
	{path: "/login", base: 90, spread: 120, weight: 3, database: true},
}

var clients = []string{"10.0.0.11", "10.0.0.12", "10.0.0.13", "203.0.113.7", "198.51.100.4"}
var methods = []string{"GET", "GET", "GET", "POST"}

func main() {
	endpoint := flag.String("endpoint", "http://localhost:4318", "the guard instance to post to")
	requests := flag.Int("requests", 900, "how many requests to simulate")
	window := flag.Duration("window", time.Hour, "how far back to spread them")
	token := flag.String("token", "", "bearer token, if the instance has GUARD_TOKEN set")
	flag.Parse()

	var pool []route
	for _, r := range routes {
		for range r.weight {
			pool = append(pool, r)
		}
	}

	now := time.Now()
	var spans []*tracepb.Span
	var dbSpans []*tracepb.Span
	var records []*logspb.LogRecord

	for range *requests {
		r := pool[rand.IntN(len(pool))]
		start := now.Add(-time.Duration(rand.Int64N(int64(*window))))
		// A long tail rather than a clean normal: the shape worth looking at in
		// a histogram or a heatmap is the second hump, and a tidy distribution
		// has none.
		duration := r.base + rand.Float64()*r.spread
		if rand.Float64() < 0.08 {
			duration += 400 + rand.Float64()*900
		}
		failed := rand.Float64() < 0.04
		traceID := randomID(16)
		spanID := randomID(8)

		status := &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}
		if failed {
			status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "internal error"}
		}
		spans = append(spans, &tracepb.Span{
			TraceId: traceID, SpanId: spanID,
			Name:              methods[rand.IntN(len(methods))] + " " + r.path,
			Kind:              tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(start.UnixNano()),
			EndTimeUnixNano:   uint64(start.Add(millis(duration)).UnixNano()),
			Status:            status,
			Attributes: attributes(map[string]any{
				"http.route":                r.path,
				"http.request.method":       methods[rand.IntN(len(methods))],
				"http.response.status_code": statusCode(failed),
				"client.address":            clients[rand.IntN(len(clients))],
			}),
		})

		// A child span, so a waterfall has something to nest and db.system has
		// values to group by.
		if r.database {
			childStart := start.Add(millis(rand.Float64() * duration * 0.3))
			childDuration := duration * (0.2 + rand.Float64()*0.5)
			dbSpans = append(dbSpans, &tracepb.Span{
				TraceId: traceID, SpanId: randomID(8), ParentSpanId: spanID,
				Name:              "SELECT " + r.path[1:],
				Kind:              tracepb.Span_SPAN_KIND_CLIENT,
				StartTimeUnixNano: uint64(childStart.UnixNano()),
				EndTimeUnixNano:   uint64(childStart.Add(millis(childDuration)).UnixNano()),
				Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				Attributes:        attributes(map[string]any{"db.system.name": "postgres", "db.operation.name": "SELECT"}),
			})
		}

		if failed || rand.Float64() < 0.15 {
			severity, number, body := "INFO", logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "served "+r.path
			if failed {
				severity, number, body = "ERROR", logspb.SeverityNumber_SEVERITY_NUMBER_ERROR, "request to "+r.path+" failed"
			}
			records = append(records, &logspb.LogRecord{
				TimeUnixNano:   uint64(start.UnixNano()),
				SeverityText:   severity,
				SeverityNumber: number,
				Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
				TraceId:        traceID, SpanId: spanID,
				Attributes: attributes(map[string]any{"http.route": r.path}),
			})
		}
	}

	// Two gauges sampled every minute. A quantity that exists continuously and
	// wanders is what a candlestick is actually for — open and close mean
	// something for a queue depth and nothing for a checkout.
	depth, carts := 40.0, 120.0
	var queuePoints, cartPoints []*metricspb.NumberDataPoint
	for minute := int(window.Minutes()); minute >= 0; minute-- {
		at := uint64(now.Add(-time.Duration(minute) * time.Minute).UnixNano())
		depth = max(0, depth+(rand.Float64()-0.45)*25)
		carts = max(0, carts+(rand.Float64()-0.5)*40)
		queuePoints = append(queuePoints, &metricspb.NumberDataPoint{TimeUnixNano: at, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: depth}})
		cartPoints = append(cartPoints, &metricspb.NumberDataPoint{TimeUnixNano: at, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: carts}})
	}

	post := poster(*endpoint, *token)
	for _, batch := range chunk(spans, 400) {
		post("/v1/traces", &collectortracepb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{Resource: resourceFor("api"), ScopeSpans: []*tracepb.ScopeSpans{{Spans: batch}}}},
		})
	}
	for _, batch := range chunk(dbSpans, 400) {
		post("/v1/traces", &collectortracepb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{Resource: resourceFor("db"), ScopeSpans: []*tracepb.ScopeSpans{{Spans: batch}}}},
		})
	}
	for _, batch := range chunk(records, 400) {
		post("/v1/logs", &collectorlogspb.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{Resource: resourceFor("api"), ScopeLogs: []*logspb.ScopeLogs{{LogRecords: batch}}}},
		})
	}
	post("/v1/metrics", &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{Resource: resourceFor("api"), ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "queue.depth", Unit: "{items}", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: queuePoints}}},
			{Name: "cart.active", Unit: "{carts}", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: cartPoints}}},
		}}}}},
	})

	fmt.Printf("seeded %d spans, %d logs and %d metric points over the last %s to %s\n",
		len(spans)+len(dbSpans), len(records), len(queuePoints)+len(cartPoints), *window, *endpoint)
}

func poster(endpoint, token string) func(string, proto.Message) {
	client := &http.Client{Timeout: 30 * time.Second}
	return func(path string, message proto.Message) {
		body, err := proto.Marshal(message)
		if err != nil {
			log.Fatalf("encode %s: %v", path, err)
		}
		request, err := http.NewRequest(http.MethodPost, endpoint+path, bytes.NewReader(body))
		if err != nil {
			log.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/x-protobuf")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err != nil {
			log.Fatalf("post %s: %v — is guard running at %s?", path, err, endpoint)
		}
		defer response.Body.Close()
		if response.StatusCode >= 300 {
			log.Fatalf("post %s: %s", path, response.Status)
		}
	}
}

func resourceFor(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: attributes(map[string]any{
		"service.name":        service,
		"service.instance.id": service + "-1",
	})}
}

func attributes(values map[string]any) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(values))
	for key, value := range values {
		attribute := &commonpb.KeyValue{Key: key}
		switch typed := value.(type) {
		case string:
			attribute.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: typed}}
		case int:
			attribute.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(typed)}}
		}
		out = append(out, attribute)
	}
	return out
}

func statusCode(failed bool) int {
	if failed {
		return 500
	}
	return 200
}

func millis(value float64) time.Duration { return time.Duration(value * float64(time.Millisecond)) }

func randomID(length int) []byte {
	id := make([]byte, length)
	for i := range id {
		id[i] = byte(rand.IntN(256))
	}
	return id
}

func chunk[T any](items []T, size int) [][]T {
	var out [][]T
	for start := 0; start < len(items); start += size {
		out = append(out, items[start:min(start+size, len(items))])
	}
	return out
}
