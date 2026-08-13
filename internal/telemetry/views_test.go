package telemetry

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

// seed writes a small but realistic trace workload: three routes, two clients,
// durations that differ enough per route for a percentile to be checkable by
// hand, and one error.
func seed(t *testing.T) (*Store, time.Time) {
	t.Helper()
	store := NewStore(10_000)
	t.Cleanup(func() { store.Close() })

	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	routes := []struct {
		route    string
		client   string
		duration float64
	}{
		{"/checkout", "10.0.0.1", 120},
		{"/checkout", "10.0.0.1", 240},
		{"/checkout", "10.0.0.2", 60},
		{"/search", "10.0.0.2", 15},
		{"/search", "10.0.0.1", 25},
		{"/health", "10.0.0.3", 2},
	}
	events := make([]Event, 0, len(routes))
	for i, r := range routes {
		severity := "OK"
		if r.route == "/health" {
			severity = "ERROR"
		}
		events = append(events, Event{
			Signal: "traces", Service: "api", Name: "GET " + r.route, Kind: "server", Severity: severity,
			Timestamp:  base.Add(time.Duration(i) * time.Minute),
			DurationMS: r.duration,
			TraceID:    "trace-" + strings.TrimPrefix(r.route, "/"),
			SpanID:     "span-" + string(rune('a'+i)),
			Attributes: map[string]any{"http.route": r.route, "client.address": r.client},
		})
	}
	if err := store.Add(events...); err != nil {
		t.Fatal(err)
	}
	return store, base
}

func run(t *testing.T, store *Store, panel string, q ViewQuery) Frame {
	t.Helper()
	frame, err := store.RunView(panel, q)
	if err != nil {
		t.Fatalf("%s: %v", panel, err)
	}
	return frame
}

// Every panel has to compile and run against a real database. This is the test
// that catches a shape whose SQL only looked right.
func TestEveryPanelCompiles(t *testing.T) {
	store, _ := seed(t)
	for _, spec := range model.Panels {
		query := ViewQuery{Signal: "traces", Range: "24h", Value: "duration_ms", Agg: "avg", Bucket: "5m"}
		switch spec.Shape {
		case model.ShapeCategorical:
			query.GroupBy = "attr:http.route"
		case model.ShapeScatter:
			query.X = "timestamp"
		}
		frame, err := store.RunView(spec.Panel, query)
		if err != nil {
			t.Errorf("%s: %v", spec.Panel, err)
			continue
		}
		if frame.Shape != spec.Shape {
			t.Errorf("%s: shape = %q, want %q", spec.Panel, frame.Shape, spec.Shape)
		}
		if frame.Rows == nil {
			t.Errorf("%s: rows must never be nil — the renderer branches on length", spec.Panel)
		}
	}
}

func TestCategoricalCountsByIndexedAttribute(t *testing.T) {
	store, _ := seed(t)
	frame := run(t, store, "bar", ViewQuery{Signal: "traces", Range: "24h", GroupBy: "attr:http.route", Agg: "count"})

	if len(frame.Rows) != 3 {
		t.Fatalf("rows = %d, want 3: %v", len(frame.Rows), frame.Rows)
	}
	// value_desc is the default order, so the busiest route leads.
	if frame.Rows[0][0] != "/checkout" || frame.Rows[0][1].(float64) != 3 {
		t.Fatalf("top row = %v, want /checkout 3", frame.Rows[0])
	}
}

// The two semconv spellings are deliberately one field. A view written against
// http.method has to see the rows an exporter tagged http.request.method.
func TestIndexedAttributeMergesSemconvSpellings(t *testing.T) {
	store, _ := seed(t)
	if err := store.Add(
		Event{Signal: "traces", Service: "api", Timestamp: time.Now().UTC(), Attributes: map[string]any{"http.request.method": "GET"}},
		Event{Signal: "traces", Service: "api", Timestamp: time.Now().UTC(), Attributes: map[string]any{"http.method": "GET"}},
	); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"attr:http.method", "attr:http.request.method"} {
		frame := run(t, store, "bar", ViewQuery{Signal: "traces", Range: "24h", GroupBy: ref, Agg: "count"})
		var got float64
		for _, row := range frame.Rows {
			if row[0] == "GET" {
				got = row[1].(float64)
			}
		}
		if got != 2 {
			t.Errorf("%s: GET = %v, want 2 (both spellings)", ref, got)
		}
	}
}

// Percentiles are exact nearest-rank, not interpolated. /checkout is 60/120/240,
// so p50 is 120 and p99 is 240 — numbers that are wrong under interpolation.
func TestPercentileIsExact(t *testing.T) {
	store, _ := seed(t)
	for _, tc := range []struct{ agg string; want float64 }{{"p50", 120}, {"p99", 240}, {"min", 60}, {"max", 240}} {
		frame := run(t, store, "bar", ViewQuery{
			Signal: "traces", Range: "24h", GroupBy: "attr:http.route", Agg: tc.agg, Value: "duration_ms",
		})
		var got float64
		for _, row := range frame.Rows {
			if row[0] == "/checkout" {
				got = row[1].(float64)
			}
		}
		if got != tc.want {
			t.Errorf("%s(/checkout) = %v, want %v", tc.agg, got, tc.want)
		}
	}
}

func TestHistogramCoversEveryBucket(t *testing.T) {
	store, _ := seed(t)
	frame := run(t, store, "histogram", ViewQuery{Signal: "traces", Range: "24h", Value: "duration_ms", Buckets: 8})

	if len(frame.Rows) != 8 {
		t.Fatalf("rows = %d, want 8 — empty buckets are information", len(frame.Rows))
	}
	total := 0
	for _, row := range frame.Rows {
		total += int(row[2].(int))
	}
	if total != 6 {
		t.Fatalf("counted %d values, want 6 — the maximum must land in the last bucket, not past it", total)
	}
}

// Candlestick and box read the same four slots but mean different things. Both
// are checked against a hand-computed bucket.
func TestOHLCPanels(t *testing.T) {
	store, base := seed(t)
	window := ViewQuery{Signal: "traces", From: base.Add(-time.Minute), To: base.Add(10 * time.Minute), Value: "duration_ms", Bucket: "1h"}

	// Buckets are aligned to the epoch, so a run at 09:58 splits the same six
	// events across two hourly buckets and a run at 09:02 does not. The facts
	// that hold either way are the ones across all the buckets returned.
	candles := run(t, store, "candlestick", window)
	if len(candles.Rows) == 0 {
		t.Fatal("no candles")
	}
	first, last := candles.Rows[0], candles.Rows[len(candles.Rows)-1]
	high, low := math.Inf(-1), math.Inf(1)
	for _, row := range candles.Rows {
		high = math.Max(high, row[2].(float64))
		low = math.Min(low, row[3].(float64))
	}
	// In time order the durations are 120, 240, 60, 15, 25, 2.
	if first[1].(float64) != 120 || last[4].(float64) != 2 || high != 240 || low != 2 {
		t.Fatalf("ohlc opened %v, closed %v, high %v, low %v; want 120, 2, 240, 2", first[1], last[4], high, low)
	}

	boxes := run(t, store, "box", window)
	if len(boxes.Rows) == 0 {
		t.Fatal("no boxes")
	}
	high, low = math.Inf(-1), math.Inf(1)
	for _, row := range boxes.Rows {
		low = math.Min(low, row[1].(float64))
		high = math.Max(high, row[4].(float64))
	}
	if low != 2 || high != 240 {
		t.Fatalf("box spanned %v to %v, want 2 to 240", low, high)
	}
}

func TestStatComparesWithPreviousWindow(t *testing.T) {
	store, _ := seed(t)
	frame := run(t, store, "stat", ViewQuery{Signal: "traces", Range: "1h", Agg: "count"})
	if len(frame.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(frame.Rows))
	}
	if frame.Rows[0][0].(float64) != 6 {
		t.Fatalf("value = %v, want 6", frame.Rows[0][0])
	}
	if frame.Rows[0][1] == nil {
		t.Fatal("previous must be present for a bounded window — the comparison is the point of the panel")
	}
}

// A view is only safe to expose because a field reference can never become SQL.
func TestFieldReferencesAreNotInterpolated(t *testing.T) {
	store, _ := seed(t)
	for _, field := range []string{"duration_ms); DROP TABLE events; --", "attr:x\"); DROP TABLE events; --"} {
		if _, err := store.RunView("bar", ViewQuery{Signal: "traces", GroupBy: field, Agg: "count"}); err == nil {
			t.Errorf("%q was accepted as a field", field)
		}
	}
	// The table is still there.
	if _, err := store.Query(Filter{Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

// A filter value is a parameter, so it may contain anything at all.
func TestFilterValuesAreParameters(t *testing.T) {
	store, _ := seed(t)
	frame := run(t, store, "bar", ViewQuery{
		Signal: "traces", Range: "24h", GroupBy: "service", Agg: "count",
		Filters: []model.Condition{{Field: "attr:http.route", Op: "eq", Value: "'; DROP TABLE events; --"}},
	})
	if len(frame.Rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(frame.Rows))
	}
	if _, err := store.Query(Filter{Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestConditionOperators(t *testing.T) {
	store, _ := seed(t)
	count := func(condition model.Condition) float64 {
		frame := run(t, store, "stat", ViewQuery{Signal: "traces", Range: "24h", Agg: "count", Filters: []model.Condition{condition}})
		return frame.Rows[0][0].(float64)
	}
	for _, tc := range []struct {
		condition model.Condition
		want      float64
	}{
		{model.Condition{Field: "attr:http.route", Op: "eq", Value: "/search"}, 2},
		{model.Condition{Field: "attr:http.route", Op: "ne", Value: "/checkout"}, 3},
		{model.Condition{Field: "duration_ms", Op: "gt", Value: "100"}, 2},
		{model.Condition{Field: "severity", Op: "contains", Value: "err"}, 1},
		{model.Condition{Field: "attr:client.address", Op: "exists"}, 6},
		{model.Condition{Field: "attr:nothing.here", Op: "missing"}, 6},
	} {
		if got := count(tc.condition); got != tc.want {
			t.Errorf("%s %s %q = %v, want %v", tc.condition.Field, tc.condition.Op, tc.condition.Value, got, tc.want)
		}
	}
}

// A timeseries grouped by something high-cardinality has to stay bounded, and
// has to say that it did.
func TestTimeseriesBoundsSeriesAndSaysSo(t *testing.T) {
	store := NewStore(10_000)
	t.Cleanup(func() { store.Close() })
	now := time.Now().UTC()
	events := make([]Event, 0, 40)
	for i := range 40 {
		events = append(events, Event{
			Signal: "traces", Service: "api", Timestamp: now.Add(-time.Duration(i) * time.Second),
			DurationMS: float64(i), Attributes: map[string]any{"http.route": "/r" + string(rune('a'+i%40))},
		})
	}
	if err := store.Add(events...); err != nil {
		t.Fatal(err)
	}
	frame, err := store.RunView("timeseries", ViewQuery{Signal: "traces", Range: "1h", GroupBy: "attr:http.route", Agg: "count"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Series) > 12 {
		t.Fatalf("series = %d, want at most 12", len(frame.Series))
	}
	if len(frame.Notes) == 0 {
		t.Fatal("truncation must be reported — a silent cap reads as 'this is everything'")
	}
}

func TestValidationRejectsMismatchedPanels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		panel string
		query ViewQuery
	}{
		{"bar with nothing to group by", "bar", ViewQuery{Agg: "count"}},
		{"average of a string", "timeseries", ViewQuery{Agg: "avg", Value: "service"}},
		{"scatter with no x", "scatter", ViewQuery{Value: "duration_ms"}},
		{"unknown panel", "sankey", ViewQuery{}},
		{"unknown aggregation", "bar", ViewQuery{GroupBy: "service", Agg: "median"}},
		{"histogram with no value", "histogram", ViewQuery{}},
	} {
		if err := tc.query.ValidateFor(tc.panel); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// Structural checks only. Four hundred pie slices is a bad chart, not an
// invalid query, and the validator is not the taste police.
func TestValidationAllowsQuestionableButValidPanels(t *testing.T) {
	query := ViewQuery{Signal: "logs", GroupBy: "message", Agg: "count", Limit: 400}
	if err := query.ValidateFor("pie"); err != nil {
		t.Fatalf("rejected a legal query: %v", err)
	}
}

func TestTraceWaterfallIsDepthFirst(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	start := time.Now().UTC().Add(-time.Minute)
	if err := store.Add(
		Event{Signal: "traces", Service: "api", Name: "GET /checkout", TraceID: "t1", SpanID: "root", Timestamp: start, DurationMS: 100},
		Event{Signal: "traces", Service: "api", Name: "auth", TraceID: "t1", SpanID: "a", ParentSpanID: "root", Timestamp: start.Add(10 * time.Millisecond), DurationMS: 20},
		Event{Signal: "traces", Service: "db", Name: "SELECT cart", TraceID: "t1", SpanID: "b", ParentSpanID: "a", Timestamp: start.Add(15 * time.Millisecond), DurationMS: 5},
		Event{Signal: "traces", Service: "api", Name: "charge", TraceID: "t1", SpanID: "c", ParentSpanID: "root", Timestamp: start.Add(40 * time.Millisecond), DurationMS: 50},
		Event{Signal: "traces", Service: "api", Name: "detached", TraceID: "t1", SpanID: "d", ParentSpanID: "missing", Timestamp: start.Add(90 * time.Millisecond), DurationMS: 1},
	); err != nil {
		t.Fatal(err)
	}
	trace, err := store.Trace("t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Spans) != 5 {
		t.Fatalf("spans = %d, want 5 — an orphan must be drawn, not dropped", len(trace.Spans))
	}
	want := []struct {
		name  string
		depth int
	}{{"GET /checkout", 0}, {"auth", 1}, {"SELECT cart", 2}, {"charge", 1}, {"detached", 0}}
	for i, w := range want {
		if trace.Spans[i].Name != w.name || trace.Spans[i].Depth != w.depth {
			t.Errorf("span %d = %q depth %d, want %q depth %d", i, trace.Spans[i].Name, trace.Spans[i].Depth, w.name, w.depth)
		}
	}
	if !trace.Spans[4].Orphan {
		t.Error("a span whose parent was never exported must be marked, not silently re-rooted")
	}
	if trace.Spans[3].OffsetMS != 40 {
		t.Errorf("charge offset = %v, want 40", trace.Spans[3].OffsetMS)
	}
	if trace.DurationMS != 100 {
		t.Errorf("trace duration = %v, want 100", trace.DurationMS)
	}
}

// A parent pointer cycle is malformed input, not a reason to hang.
func TestTraceSurvivesCyclicParents(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	now := time.Now().UTC()
	if err := store.Add(
		Event{Signal: "traces", Service: "api", Name: "a", TraceID: "t2", SpanID: "a", ParentSpanID: "b", Timestamp: now},
		Event{Signal: "traces", Service: "api", Name: "b", TraceID: "t2", SpanID: "b", ParentSpanID: "a", Timestamp: now},
	); err != nil {
		t.Fatal(err)
	}
	trace, err := store.Trace("t2")
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(trace.Spans))
	}
}

// A mark is an aggregate, and drilling into it has to return exactly the events
// that were counted into it — no more, or the list disagrees with the bar.
func TestDrillNarrowsToTheMark(t *testing.T) {
	store, base := seed(t)
	query := ViewQuery{Signal: "traces", Range: "24h", GroupBy: "attr:http.route", Agg: "count"}

	for _, tc := range []struct {
		name      string
		selection model.Selection
		want      int
	}{
		{"a bar", model.Selection{Series: "/checkout", HasSeries: true}, 3},
		{"another bar", model.Selection{Series: "/search", HasSeries: true}, 2},
		{"the whole panel", model.Selection{}, 6},
		{"a time bucket", model.Selection{From: base, To: base.Add(2 * time.Minute)}, 2},
		{"a bar in a bucket", model.Selection{Series: "/checkout", HasSeries: true, From: base, To: base.Add(2 * time.Minute)}, 2},
	} {
		drill, err := store.DrillView("bar", query, tc.selection)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if drill.Total != tc.want || len(drill.Events) != tc.want {
			t.Errorf("%s: total %d, listed %d, want %d", tc.name, drill.Total, len(drill.Events), tc.want)
		}
	}
}

// The bucket boundary belongs to one bucket, not to both — otherwise adjacent
// bars each claim the same event and the drill-downs sum to more than the data.
func TestDrillTreatsTheBucketEndAsExclusive(t *testing.T) {
	store, base := seed(t)
	query := ViewQuery{Signal: "traces", Range: "24h", Agg: "count"}
	first, err := store.DrillView("timeseries", query, model.Selection{From: base, To: base.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.DrillView("timeseries", query, model.Selection{From: base.Add(time.Minute), To: base.Add(2 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 1 || second.Total != 1 {
		t.Fatalf("adjacent buckets returned %d and %d, want 1 each", first.Total, second.Total)
	}
}

// A histogram bar is a value range, not a time range.
func TestDrillNarrowsToAValueRange(t *testing.T) {
	store, _ := seed(t)
	low, high := 100.0, 300.0
	drill, err := store.DrillView("histogram", ViewQuery{Signal: "traces", Range: "24h", Value: "duration_ms", Buckets: 8},
		model.Selection{Min: &low, Max: &high})
	if err != nil {
		t.Fatal(err)
	}
	// 120 and 240 fall inside; 60, 25, 15 and 2 do not.
	if drill.Total != 2 {
		t.Fatalf("total = %d, want 2", drill.Total)
	}
	// Ordered by the measured value, so the slowest example leads.
	if drill.Events[0].DurationMS != 240 {
		t.Errorf("first event = %v ms, want the slowest (240)", drill.Events[0].DurationMS)
	}
}

// "(none)" is a label on the axis for the events the group field does not
// cover, so it has to mean the same thing when clicked.
func TestDrillIntoTheUngroupedBar(t *testing.T) {
	store, _ := seed(t)
	if err := store.Add(Event{Signal: "traces", Service: "db", Name: "SELECT", Timestamp: time.Now().UTC(), DurationMS: 4}); err != nil {
		t.Fatal(err)
	}
	drill, err := store.DrillView("bar", ViewQuery{Signal: "traces", Range: "24h", GroupBy: "attr:http.route", Agg: "count"},
		model.Selection{Series: "(none)", HasSeries: true})
	if err != nil {
		t.Fatal(err)
	}
	if drill.Total != 1 || drill.Events[0].Name != "SELECT" {
		t.Fatalf("drill = %d events, first %q; want the one span with no route", drill.Total, drill.Events[0].Name)
	}
}

// The list is capped; the count is not. A bar of 5,000 must not claim to be a
// bar of 100 once you open it.
func TestDrillReportsTheTotalItDidNotList(t *testing.T) {
	store := NewStore(10_000)
	t.Cleanup(func() { store.Close() })
	now := time.Now().UTC()
	events := make([]Event, 0, 250)
	for i := range 250 {
		events = append(events, Event{Signal: "traces", Service: "api", Timestamp: now.Add(-time.Duration(i) * time.Second), DurationMS: 1})
	}
	if err := store.Add(events...); err != nil {
		t.Fatal(err)
	}
	drill, err := store.DrillView("bar", ViewQuery{Signal: "traces", Range: "1h", GroupBy: "service", Agg: "count"},
		model.Selection{Series: "api", HasSeries: true})
	if err != nil {
		t.Fatal(err)
	}
	if drill.Total != 250 {
		t.Errorf("total = %d, want 250", drill.Total)
	}
	if len(drill.Events) != 100 {
		t.Errorf("listed %d, want the 100-event cap", len(drill.Events))
	}
}

func TestDrillRejectsAnInvalidQuery(t *testing.T) {
	store, _ := seed(t)
	if _, err := store.DrillView("bar", ViewQuery{GroupBy: "service); DROP TABLE events; --", Agg: "count"}, model.Selection{}); err == nil {
		t.Error("an injected group field was accepted")
	}
	if _, err := store.Query(Filter{Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

// An empty panel is the one result that cannot explain itself, and the three
// reasons it can be empty have three different fixes.
func TestEmptyPanelsSayWhy(t *testing.T) {
	empty := NewStore(100)
	t.Cleanup(func() { empty.Close() })
	frame, err := empty.RunView("bar", ViewQuery{GroupBy: "service", Agg: "count", Range: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Notes) == 0 || !strings.Contains(frame.Notes[0], "No telemetry has arrived") {
		t.Errorf("an empty store said %q", frame.Notes)
	}

	store, _ := seed(t)
	frame, err = store.RunView("bar", ViewQuery{GroupBy: "service", Agg: "count", Range: "24h",
		Filters: []model.Condition{{Field: "service", Op: "eq", Value: "not-a-service"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Notes) == 0 || !strings.Contains(frame.Notes[0], "any window") {
		t.Errorf("a filter matching nothing said %q", frame.Notes)
	}

	// seed() writes its events half an hour ago, so a fifteen-minute window
	// misses them — the most common way a panel looks broken when it is not.
	frame, err = store.RunView("bar", ViewQuery{GroupBy: "service", Agg: "count", Range: "15m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(frame.Notes) == 0 || !strings.Contains(frame.Notes[0], "widen the time range") {
		t.Errorf("data older than the window said %q", frame.Notes)
	}
}

// A panel with results says nothing extra — the hint costs two queries, and
// they only run where the answer was empty anyway.
func TestPanelsWithResultsCarryNoHint(t *testing.T) {
	store, _ := seed(t)
	frame, err := store.RunView("bar", ViewQuery{GroupBy: "service", Agg: "count", Range: "24h"})
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range frame.Notes {
		if strings.Contains(note, "widen") || strings.Contains(note, "No telemetry") {
			t.Errorf("a panel with %d rows still explained itself: %q", len(frame.Rows), note)
		}
	}
}

func TestSaveAndDeleteView(t *testing.T) {
	store, _ := seed(t)
	saved, err := store.SaveView(View{Name: "Checkout p95", Panel: "timeseries",
		Query: ViewQuery{Signal: "traces", Agg: "p95", Value: "duration_ms", GroupBy: "attr:http.route"}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 || saved.Query.Bucket != "auto" || saved.Query.Range != "1h" {
		t.Fatalf("defaults were not stored: %#v", saved.Query)
	}
	views, err := store.Views()
	if err != nil || len(views) != 1 {
		t.Fatalf("views = %d, %v", len(views), err)
	}
	if err := store.DeleteView(saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteView(saved.ID); err == nil {
		t.Fatal("deleting a missing view must not report success")
	}
}

func TestSaveViewRejectsInvalidQuery(t *testing.T) {
	store, _ := seed(t)
	if _, err := store.SaveView(View{Name: "", Panel: "bar", Query: ViewQuery{GroupBy: "service"}}); err == nil {
		t.Error("a view with no name was saved")
	}
	if _, err := store.SaveView(View{Name: "x", Panel: "bar", Query: ViewQuery{}}); err == nil {
		t.Error("a bar chart with nothing to group by was saved")
	}
}

// An event with no attributes at all used to be stored as the JSON scalar
// `null`, and one of those anywhere in the catalogue's sample failed the whole
// request — which the dashboard shows as a builder that will not open.
func TestFieldCatalogSurvivesEventsWithoutAttributes(t *testing.T) {
	store, _ := seed(t)
	if err := store.Add(
		Event{Signal: "logs", Service: "api", Message: "no attributes here", Timestamp: time.Now().UTC()},
		Event{Signal: "logs", Service: "api", Message: "empty map", Attributes: map[string]any{}, Timestamp: time.Now().UTC()},
	); err != nil {
		t.Fatal(err)
	}
	fields, err := store.FieldCatalog()
	if err != nil {
		t.Fatalf("catalogue failed: %v", err)
	}
	for _, field := range fields.Attributes {
		if field.Ref == "attr:" || field.Label == "" {
			t.Errorf("empty attribute offered: %#v", field)
		}
	}
	// The attributes that do exist still have to be there.
	var found bool
	for _, field := range fields.Attributes {
		found = found || field.Ref == "attr:http.route"
	}
	if !found {
		t.Error("http.route went missing")
	}
}

func TestFieldCatalogOffersIndexedAttributes(t *testing.T) {
	store, _ := seed(t)
	fields, err := store.FieldCatalog()
	if err != nil {
		t.Fatal(err)
	}
	indexed := map[string]bool{}
	for _, field := range fields.Attributes {
		if field.Indexed {
			indexed[field.Ref] = true
		}
		// Both spellings of one concept would let two panels disagree.
		if field.Ref == "attr:http.status_code" {
			t.Error("a spelling with an indexed column behind it was offered separately")
		}
	}
	if !indexed["attr:http.route"] {
		t.Error("http.route must be offered as indexed")
	}
	if len(fields.Columns) == 0 {
		t.Error("no columns offered")
	}
}
