package ingest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/mirairoad/guard/internal/telemetry"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func BenchmarkOTLPLogsHandler(b *testing.B) {
	for _, records := range []int{1, 100} {
		b.Run(strconv.Itoa(records)+"_records", func(b *testing.B) {
			payload := benchmarkLogsPayload(b, records)
			handler := Handler{Store: telemetry.NewStore(10_000)}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for range b.N {
				request := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/x-protobuf")
				handler.logs(httptest.NewRecorder(), request)
			}
			b.ReportMetric(float64(b.N*records)/b.Elapsed().Seconds(), "events/s")
		})
	}
}

func BenchmarkOTLPLogsHandlerParallel100(b *testing.B) {
	const records = 100
	payload := benchmarkLogsPayload(b, records)
	handler := Handler{Store: telemetry.NewStore(10_000)}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			request := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/x-protobuf")
			handler.logs(httptest.NewRecorder(), request)
		}
	})
	b.ReportMetric(float64(b.N*records)/b.Elapsed().Seconds(), "events/s")
}

func BenchmarkOTLPLogsHTTPParallel(b *testing.B) {
	for _, records := range []int{1, 100} {
		b.Run(strconv.Itoa(records)+"_records", func(b *testing.B) {
			payload := benchmarkLogsPayload(b, records)
			mux := http.NewServeMux()
			Handler{Store: telemetry.NewStore(10_000)}.Register(mux)
			server := httptest.NewServer(mux)
			b.Cleanup(server.Close)
			transport := &http.Transport{
				MaxIdleConns:        256,
				MaxIdleConnsPerHost: 256,
				MaxConnsPerHost:     256,
			}
			client := &http.Client{Transport: transport}
			b.Cleanup(transport.CloseIdleConnections)
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/logs", bytes.NewReader(payload))
					request.Header.Set("Content-Type", "application/x-protobuf")
					response, err := client.Do(request)
					if err != nil {
						b.Error(err)
						continue
					}
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusOK {
						b.Errorf("status = %d", response.StatusCode)
					}
				}
			})
			b.ReportMetric(float64(b.N*records)/b.Elapsed().Seconds(), "events/s")
		})
	}
}

func benchmarkLogsPayload(b *testing.B, count int) []byte {
	b.Helper()
	records := make([]*logspb.LogRecord, count)
	for i := range records {
		records[i] = &logspb.LogRecord{
			TimeUnixNano: uint64(time.Now().UnixNano()),
			SeverityText: "INFO",
			Body:         stringValue("request completed"),
			Attributes: []*commonpb.KeyValue{
				{Key: "http.request.method", Value: stringValue("GET")},
				{Key: "url.path", Value: stringValue("/orders")},
			},
		}
	}
	request := &collectorlogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			{Key: "service.name", Value: stringValue("benchmark-api")},
			{Key: "service.instance.id", Value: stringValue("benchmark-1")},
		}},
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
	}}}
	payload, err := proto.Marshal(request)
	if err != nil {
		b.Fatal(err)
	}
	return payload
}
