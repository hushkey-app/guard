package ingest

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestOTLPProtobufAndJSON(t *testing.T) {
	store, server := testServer(t, "")
	now := uint64(time.Now().UnixNano())
	resource := &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
		{Key: "service.name", Value: stringValue("checkout")},
		{Key: "service.instance.id", Value: stringValue("checkout-1")},
	}}

	logs := &collectorlogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: resource,
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					TimeUnixNano: now,
					SeverityText: "ERROR",
					Body:         stringValue("payment failed"),
					TraceId:      bytes.Repeat([]byte{1}, 16),
				}},
			}},
		}},
	}
	postProto(t, server.URL+"/v1/logs", logs, true)

	traces := &collectortracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: resource,
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Name:              "POST /pay",
					StartTimeUnixNano: now,
					EndTimeUnixNano:   now + uint64(12*time.Millisecond),
					Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR},
				}},
			}},
		}},
	}
	postJSON(t, server.URL+"/v1/traces", traces)

	metrics := &collectormetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: resource,
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Metrics: []*metricspb.Metric{{
					Name: "queue.depth",
					Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
						DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: now, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 7}}},
					}},
				}},
			}},
		}},
	}
	postProto(t, server.URL+"/v1/metrics", metrics, false)

	summary, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Logs != 1 || summary.Spans != 1 || summary.Metrics != 1 || summary.Errors != 2 || len(summary.Instances) != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got, err := store.Query(telemetry.Filter{Signal: "metrics", Limit: 1}); err != nil || len(got) != 1 || got[0].Value == nil || *got[0].Value != 7 {
		t.Fatalf("unexpected metric: %#v", got)
	}
}

// TestIngestCredentials pins the two credentials the OTLP door takes. The rows
// that matter are the last two: GUARD_OTEL_SECRET has to open these routes on
// its own, or a deployment that issues only the collector's credential has no
// working exporters — and GUARD_TOKEN has to keep opening them, or upgrading
// silently drops the telemetry from every exporter configured before the
// secret existed.
func TestIngestCredentials(t *testing.T) {
	for _, tc := range []struct {
		name          string
		token, secret string
		presented     string
		want          int
	}{
		{name: "neither set is an open door", want: http.StatusOK},
		{name: "token set, none presented", token: "operator", want: http.StatusUnauthorized},
		{name: "token set, wrong one presented", token: "operator", presented: "Bearer nope", want: http.StatusUnauthorized},
		{name: "token set and presented", token: "operator", presented: "Bearer operator", want: http.StatusOK},
		{name: "secret alone closes the door", secret: "collector", want: http.StatusUnauthorized},
		{name: "secret alone opens it", secret: "collector", presented: "Bearer collector", want: http.StatusOK},
		{name: "both set, secret presented", token: "operator", secret: "collector", presented: "Bearer collector", want: http.StatusOK},
		{name: "both set, token still presented", token: "operator", secret: "collector", presented: "Bearer operator", want: http.StatusOK},
		{name: "both set, neither presented", token: "operator", secret: "collector", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := telemetry.NewStore(100)
			t.Cleanup(func() { store.Close() })
			mux := http.NewServeMux()
			Handler{Store: store, Token: tc.token, Secret: tc.secret}.Register(mux)
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)

			body, err := proto.Marshal(&collectorlogspb.ExportLogsServiceRequest{})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/logs", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/x-protobuf")
			if tc.presented != "" {
				request.Header.Set("Authorization", tc.presented)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", response.StatusCode, tc.want)
			}
		})
	}
}

func testServer(t *testing.T, token string) (*telemetry.Store, *httptest.Server) {
	t.Helper()
	store := telemetry.NewStore(100)
	t.Cleanup(func() { store.Close() })
	mux := http.NewServeMux()
	Handler{Store: store, Token: token}.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return store, server
}

func postProto(t *testing.T, url string, message proto.Message, compressed bool) {
	t.Helper()
	body, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var reader io.Reader = bytes.NewReader(body)
	var zipped bytes.Buffer
	if compressed {
		zw := gzip.NewWriter(&zipped)
		if _, err := zw.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		reader = &zipped
	}
	request, _ := http.NewRequest(http.MethodPost, url, reader)
	request.Header.Set("Content-Type", "application/x-protobuf")
	if compressed {
		request.Header.Set("Content-Encoding", "gzip")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s = %d: %s", url, response.StatusCode, payload)
	}
}

func postJSON(t *testing.T, url string, message proto.Message) {
	t.Helper()
	body, err := protojson.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s = %d: %s", url, response.StatusCode, payload)
	}
	var value map[string]any
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
}

func stringValue(value string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}
}

func floatPtr(value float64) *float64 { return &value }
