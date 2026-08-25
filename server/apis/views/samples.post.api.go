package views

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Samples fills an empty dashboard with one panel per visualisation.
//
// It exists because the catalogue of panels is not a useful thing to read: the
// difference between a state timeline and a status history is obvious once you
// have seen both drawn from your own telemetry, and unclear in any number of
// words. So: one of each, against whatever this instance has actually received.
//
// Panels that already exist by name are skipped, so it is safe to run twice —
// and it answers with what it made, not with a count, because "17 created" and
// "0 created, they were already there" are the same sentence to a dashboard
// that has not refreshed yet.
var Samples = api.Define(api.Spec[api.None, api.None, []model.View]{
	Name:  "Create Sample Panels",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.View, error) {
		existing, err := store.Get().Views()
		if err != nil {
			return nil, err
		}
		taken := make(map[string]bool, len(existing))
		for _, view := range existing {
			taken[view.Name] = true
		}

		fields, err := store.Get().FieldCatalog()
		if err != nil {
			return nil, err
		}
		created := make([]model.View, 0, len(model.Panels))
		for _, sample := range sampleViews(fields) {
			if taken[sample.Name] {
				continue
			}
			saved, err := store.Get().SaveView(sample)
			if err != nil {
				// One bad sample must not cost the other fifteen — a panel that
				// cannot be built from this instance's fields is a panel to skip,
				// not a failed request.
				continue
			}
			created = append(created, saved)
		}
		return created, nil
	},
})

// sampleViews is one view per entry in model.Panels, in the same order, built
// from fields this instance is known to have.
//
// The window is 24 hours rather than the usual hour: a dashboard someone is
// seeing for the first time should have something in it, and telemetry that
// stopped arriving over lunch is the normal state of a development instance.
func sampleViews(fields model.Fields) []model.View {
	// Group by route when the telemetry carries one, and fall back to service,
	// which every event has.
	group := "service"
	for _, field := range fields.Attributes {
		if field.Ref == "attr:http.route" {
			group = field.Ref
		}
	}
	const window = "24h"
	traces := func(q model.ViewQuery) model.ViewQuery { q.Signal, q.Range = "traces", window; return q }

	return []model.View{
		{Name: "Requests over time", Panel: "timeseries", Width: 8, Description: "Sample · time series",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: group, Bucket: "auto"})},
		{Name: "Requests per bucket", Panel: "bar_timeseries", Width: 4, Description: "Sample · bar time series",
			Query: traces(model.ViewQuery{Agg: "count", Bucket: "auto"})},
		{Name: "Service activity", Panel: "state_timeline", Width: 6, Description: "Sample · state timeline",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: "service", Bucket: "auto"})},
		{Name: "Reporting history", Panel: "status_history", Width: 6, Description: "Sample · status history",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: "service", Bucket: "auto"})},
		{Name: "Busiest routes", Panel: "bar", Width: 4, Description: "Sample · bar chart",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: group, Limit: 8})},
		{Name: "Share of traffic", Panel: "pie", Width: 4, Description: "Sample · pie chart",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: "service", Limit: 6})},
		{Name: "Requests around the world", Panel: "map", Width: 8, Description: "Sample · public client IP map",
			Query: traces(model.ViewQuery{Agg: "count", GroupBy: "attr:client.address", Limit: 100})},
		{Name: "Latency distribution", Panel: "histogram", Width: 4, Description: "Sample · histogram",
			Query: traces(model.ViewQuery{Value: "duration_ms", Buckets: 20})},
		{Name: "Latency over time", Panel: "heatmap", Width: 8, Description: "Sample · heatmap",
			Query: traces(model.ViewQuery{Value: "duration_ms", Bucket: "auto", Buckets: 16})},
		{Name: "Every span, plotted", Panel: "scatter", Width: 4, Description: "Sample · XY chart — click a dot",
			Query: traces(model.ViewQuery{X: "timestamp", Value: "duration_ms", GroupBy: "service", Limit: 1500})},
		{Name: "Duration trend", Panel: "trend", Width: 6, Description: "Sample · trend",
			Query: traces(model.ViewQuery{X: "timestamp", Value: "duration_ms", GroupBy: "service", Limit: 400})},
		{Name: "Duration open to close", Panel: "candlestick", Width: 6, Description: "Sample · candlestick",
			Query: traces(model.ViewQuery{Value: "duration_ms", Bucket: "auto"})},
		{Name: "Duration spread", Panel: "box", Width: 6, Description: "Sample · box plot",
			Query: traces(model.ViewQuery{Value: "duration_ms", Bucket: "auto"})},
		{Name: "Spans received", Panel: "stat", Width: 3, Description: "Sample · big number",
			Query: traces(model.ViewQuery{Agg: "count"})},
		{Name: "Slowest span", Panel: "gauge", Width: 3, Description: "Sample · gauge",
			Query: traces(model.ViewQuery{Agg: "max", Value: "duration_ms"})},
		{Name: "Errors logged", Panel: "bar_gauge", Width: 3, Description: "Sample · bar gauge",
			Query: model.ViewQuery{Signal: "logs", Range: window, Agg: "count",
				Filters: []model.Condition{{Field: "severity", Op: "contains", Value: "error"}}}},
		{Name: "Most recent trace", Panel: "waterfall", Width: 12, Description: "Sample · trace waterfall",
			Query: traces(model.ViewQuery{Order: "latest"})},
	}
}
