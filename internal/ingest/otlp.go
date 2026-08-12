package ingest

import (
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mirairoad/guard/internal/telemetry"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxBody = 16 << 20

type Handler struct {
	Store *telemetry.Store
	Token string
}

// Register mounts the OTLP receiver. Only the protobuf half lives here: the
// JSON API the dashboard reads is declared in server/apis and registered from
// main.go, because a typed envelope around an OTLP ExportRequest would buy
// nothing and these three routes must stay byte-compatible with every exporter.
func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/logs", h.logs)
	mux.HandleFunc("POST /v1/traces", h.traces)
	mux.HandleFunc("POST /v1/metrics", h.metrics)
}

func (h Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.Token == "" || r.Header.Get("Authorization") == "Bearer "+h.Token {
		return true
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (h Handler) logs(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req collectorlogspb.ExportLogsServiceRequest
	jsonMode, err := decode(r, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Store.Add(logEvents(&req)...); err != nil {
		http.Error(w, "store logs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeProto(w, jsonMode, &collectorlogspb.ExportLogsServiceResponse{})
}

func (h Handler) traces(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req collectortracepb.ExportTraceServiceRequest
	jsonMode, err := decode(r, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Store.Add(traceEvents(&req)...); err != nil {
		http.Error(w, "store traces: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeProto(w, jsonMode, &collectortracepb.ExportTraceServiceResponse{})
}

func (h Handler) metrics(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var req collectormetricspb.ExportMetricsServiceRequest
	jsonMode, err := decode(r, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Store.Add(metricEvents(&req)...); err != nil {
		http.Error(w, "store metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeProto(w, jsonMode, &collectormetricspb.ExportMetricsServiceResponse{})
}

func decode(r *http.Request, dst proto.Message) (bool, error) {
	var reader io.Reader = r.Body
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return false, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		reader = gz
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBody+1))
	if err != nil {
		return false, fmt.Errorf("read payload: %w", err)
	}
	if len(body) > maxBody {
		return false, fmt.Errorf("payload exceeds %d bytes", maxBody)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	jsonMode := contentType == "application/json"
	if jsonMode {
		return true, protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, dst)
	}
	if contentType != "" && contentType != "application/x-protobuf" && contentType != "application/protobuf" && contentType != "application/octet-stream" {
		return false, fmt.Errorf("unsupported content type %q", contentType)
	}
	return false, proto.Unmarshal(body, dst)
}

func writeProto(w http.ResponseWriter, jsonMode bool, message proto.Message) {
	var body []byte
	var err error
	if jsonMode {
		w.Header().Set("Content-Type", "application/json")
		body, err = protojson.Marshal(message)
	} else {
		w.Header().Set("Content-Type", "application/x-protobuf")
		body, err = proto.Marshal(message)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(body)
}

func logEvents(req *collectorlogspb.ExportLogsServiceRequest) []telemetry.Event {
	var out []telemetry.Event
	for _, rl := range req.ResourceLogs {
		base, service, instance := resource(rl.Resource)
		for _, sl := range rl.ScopeLogs {
			scope := ""
			if sl.Scope != nil {
				scope = sl.Scope.Name
			}
			for _, record := range sl.LogRecords {
				attrs := clone(base)
				merge(attrs, attributes(record.Attributes))
				out = append(out, telemetry.Event{Signal: "logs", Timestamp: unixNano(record.TimeUnixNano), Service: service,
					Instance: instance, Scope: scope, Severity: record.SeverityText, Message: text(record.Body),
					TraceID: hex.EncodeToString(record.TraceId), SpanID: hex.EncodeToString(record.SpanId), Attributes: attrs})
			}
		}
	}
	return out
}

func traceEvents(req *collectortracepb.ExportTraceServiceRequest) []telemetry.Event {
	var out []telemetry.Event
	for _, rs := range req.ResourceSpans {
		base, service, instance := resource(rs.Resource)
		for _, ss := range rs.ScopeSpans {
			scope := ""
			if ss.Scope != nil {
				scope = ss.Scope.Name
			}
			for _, span := range ss.Spans {
				attrs := clone(base)
				merge(attrs, attributes(span.Attributes))
				severity := "OK"
				statusMessage := ""
				if span.Status != nil && span.Status.Code == tracepb.Status_STATUS_CODE_ERROR {
					severity = "ERROR"
				}
				if span.Status != nil {
					statusMessage = span.Status.Message
				}
				kind := strings.TrimPrefix(span.Kind.String(), "SPAN_KIND_")
				out = append(out, telemetry.Event{Signal: "traces", Timestamp: unixNano(span.StartTimeUnixNano), Service: service,
					Instance: instance, Scope: scope, Name: span.Name, Severity: severity, Message: statusMessage, Kind: kind,
					TraceID: hex.EncodeToString(span.TraceId), SpanID: hex.EncodeToString(span.SpanId), ParentSpanID: hex.EncodeToString(span.ParentSpanId),
					DurationMS: float64(span.EndTimeUnixNano-span.StartTimeUnixNano) / float64(time.Millisecond), Attributes: attrs})
			}
		}
	}
	return out
}

func metricEvents(req *collectormetricspb.ExportMetricsServiceRequest) []telemetry.Event {
	var out []telemetry.Event
	for _, rm := range req.ResourceMetrics {
		base, service, instance := resource(rm.Resource)
		for _, sm := range rm.ScopeMetrics {
			scope := ""
			if sm.Scope != nil {
				scope = sm.Scope.Name
			}
			for _, metric := range sm.Metrics {
				appendPoint := func(ts uint64, value float64, attrs []*commonpb.KeyValue, metricType string, details map[string]any) {
					all := clone(base)
					merge(all, attributes(attrs))
					merge(all, details)
					v := value
					out = append(out, telemetry.Event{Signal: "metrics", Timestamp: unixNano(ts), Service: service,
						Instance: instance, Scope: scope, Name: metric.Name, Value: &v, Unit: metric.Unit, MetricType: metricType, Attributes: all})
				}
				switch data := metric.Data.(type) {
				case *metricspb.Metric_Gauge:
					for _, p := range data.Gauge.DataPoints {
						appendPoint(p.TimeUnixNano, number(p), p.Attributes, "gauge", nil)
					}
				case *metricspb.Metric_Sum:
					for _, p := range data.Sum.DataPoints {
						appendPoint(p.TimeUnixNano, number(p), p.Attributes, "sum", map[string]any{"guard.aggregation_temporality": data.Sum.AggregationTemporality.String(), "guard.monotonic": data.Sum.IsMonotonic})
					}
				case *metricspb.Metric_Histogram:
					for _, p := range data.Histogram.DataPoints {
						appendPoint(p.TimeUnixNano, p.GetSum(), p.Attributes, "histogram", map[string]any{"guard.count": p.Count, "guard.min": p.GetMin(), "guard.max": p.GetMax(), "guard.bucket_counts": p.BucketCounts, "guard.explicit_bounds": p.ExplicitBounds})
					}
				case *metricspb.Metric_ExponentialHistogram:
					for _, p := range data.ExponentialHistogram.DataPoints {
						appendPoint(p.TimeUnixNano, p.GetSum(), p.Attributes, "exponential histogram", map[string]any{"guard.count": p.Count, "guard.min": p.GetMin(), "guard.max": p.GetMax(), "guard.scale": p.Scale, "guard.zero_count": p.ZeroCount})
					}
				case *metricspb.Metric_Summary:
					for _, p := range data.Summary.DataPoints {
						quantiles := make(map[string]float64, len(p.QuantileValues))
						for _, q := range p.QuantileValues {
							quantiles[strconv.FormatFloat(q.Quantile, 'g', -1, 64)] = q.Value
						}
						appendPoint(p.TimeUnixNano, p.Sum, p.Attributes, "summary", map[string]any{"guard.count": p.Count, "guard.quantiles": quantiles})
					}
				}
			}
		}
	}
	return out
}

func number(p *metricspb.NumberDataPoint) float64 {
	switch v := p.Value.(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

func resource(r *resourcepb.Resource) (map[string]any, string, string) {
	attrs := map[string]any{}
	if r != nil {
		attrs = attributes(r.Attributes)
	}
	service, _ := attrs["service.name"].(string)
	instance, _ := attrs["service.instance.id"].(string)
	return attrs, service, instance
}

func attributes(values []*commonpb.KeyValue) map[string]any {
	out := make(map[string]any, len(values))
	for _, kv := range values {
		out[kv.Key] = anyValue(kv.Value)
	}
	return out
}

func anyValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch value := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return value.StringValue
	case *commonpb.AnyValue_BoolValue:
		return value.BoolValue
	case *commonpb.AnyValue_IntValue:
		return value.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return value.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(value.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(value.ArrayValue.Values))
		for _, item := range value.ArrayValue.Values {
			out = append(out, anyValue(item))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		return attributes(value.KvlistValue.Values)
	}
	return nil
}

func text(v *commonpb.AnyValue) string {
	value := anyValue(v)
	if s, ok := value.(string); ok {
		return s
	}
	body, _ := json.Marshal(value)
	return string(body)
}
func unixNano(value uint64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(value)).UTC()
}
func clone(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	merge(dst, src)
	return dst
}
func merge(dst, src map[string]any) {
	for key, value := range src {
		dst[key] = value
	}
}
