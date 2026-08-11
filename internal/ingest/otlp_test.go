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

	"github.com/mirairoad/guard/internal/telemetry"
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

	summary := store.Snapshot()
	if summary.Logs != 1 || summary.Spans != 1 || summary.Metrics != 1 || summary.Errors != 2 || len(summary.Instances) != 1 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if got := store.Query(telemetry.Filter{Signal: "metrics", Limit: 1}); len(got) != 1 || got[0].Value == nil || *got[0].Value != 7 {
		t.Fatalf("unexpected metric: %#v", got)
	}
}

func TestSimpleLogsAndBearerToken(t *testing.T) {
	store, server := testServer(t, "secret")
	body := []byte(`{"service":"worker","severity":"INFO","message":"job complete"}`)
	response, err := http.Post(server.URL+"/api/logs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.StatusCode)
	}

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/logs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("accepted status = %d", response.StatusCode)
	}
	if got := store.Query(telemetry.Filter{Signal: "logs", Limit: 10}); len(got) != 1 || got[0].Message != "job complete" {
		t.Fatalf("logs = %#v", got)
	}
}

func TestReadAPIs(t *testing.T) {
	_, server := testServer(t, "")
	for _, path := range []string{"/healthz", "/api/summary", "/api/events", "/api/logs"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, response.StatusCode)
		}
	}
}

func testServer(t *testing.T, token string) (*telemetry.Store, *httptest.Server) {
	t.Helper()
	store := telemetry.NewStore(100)
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
