package model

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A view is a stored question, not a stored answer.
//
// Everything guard receives lands in one events table, so "requests per route",
// "checkout value spread per hour" and "the slowest trace this morning" differ
// only in how that table is filtered, grouped and aggregated. A View records
// those choices; running it produces a Frame; a renderer turns the Frame into a
// panel. Nothing about a saved view refers to a chart library, and nothing in
// the renderer knows what SQL ran.
//
// The seam between the two halves is Shape. A panel does not consume a signal —
// it consumes a result layout. Spans feed a bar chart perfectly well once they
// have been counted per route, and the bar chart neither knows nor cares that
// they were spans. So the compiler promises a Shape, panels declare which Shape
// they read, and the pair is checked once, here, where both are visible.
type View struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Panel       string    `json:"panel"`
	Query       ViewQuery `json:"query"`
	// Position orders the dashboard; Width is how many of its twelve columns
	// the panel occupies. Layout is deliberately this small — a full drag grid
	// is a different feature and would leak into every one of these types.
	Position  int       `json:"position"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ViewQuery is the question. It compiles to exactly one SQL statement, chosen
// by the panel's shape — see (*Store).RunView.
type ViewQuery struct {
	// Signal narrows to logs, traces or metrics. Empty means all three, which
	// is rarely what you want but is a legitimate thing to ask.
	Signal  string      `json:"signal,omitempty"`
	Filters []Condition `json:"filters,omitempty"`

	// Range is a relative window ("15m", "1h", "6h", "24h", "7d", "all"). From
	// and To override it, and are what the dashboard sends for a custom range.
	Range string    `json:"range,omitempty"`
	From  time.Time `json:"from,omitempty"`
	To    time.Time `json:"to,omitempty"`

	// GroupBy splits the result into series (timeseries) or categories
	// (categorical). A field reference: a column name, or "attr:<key>".
	GroupBy string `json:"group_by,omitempty"`
	// Bucket is the time granularity for shapes with a time axis: a duration
	// like "1m" or "5m", or "auto" to pick roughly sixty buckets for the window.
	Bucket string `json:"bucket,omitempty"`
	// Agg and Value are what to measure. Agg "count" ignores Value; everything
	// else needs a numeric field, and percentiles are exact rather than
	// interpolated (see percentileSQL).
	Agg   string `json:"agg,omitempty"`
	Value string `json:"value,omitempty"`
	// X is the horizontal field for scatter, where time is not the axis.
	X string `json:"x,omitempty"`
	// Buckets is how many bars a histogram gets, or rows a heatmap gets.
	Buckets int `json:"buckets,omitempty"`

	// Max, Warn and Critical are the gauge's scale. Max 0 means "scale to what
	// arrived", which is the right default for a number whose ceiling nobody
	// has decided yet and the wrong one for a number that has a real limit —
	// so the control is offered rather than assumed.
	Max      float64 `json:"max,omitempty"`
	Warn     float64 `json:"warn,omitempty"`
	Critical float64 `json:"critical,omitempty"`

	Limit int    `json:"limit,omitempty"`
	Order string `json:"order,omitempty"`

	// TraceID pins a waterfall to one trace. Empty means "pick one" — the
	// latest, or the slowest, per Order.
	TraceID string `json:"trace_id,omitempty"`
}

// Condition is one filter. Value is always a string on the wire: it is compared
// against a JSON attribute as often as against a column, and SQLite will coerce
// it either way. Numeric operators cast explicitly.
type Condition struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
}

// Frame is a query result in the one layout every renderer reads: named fields
// and rows of values. It is generic on purpose. A typed struct per shape would
// mean a Go type, a JSON shape and a renderer branch for each one, three places
// to forget when a panel is added; this way a new panel that reads an existing
// shape is a renderer and nothing else.
//
// Row layout by shape — this table is the contract between the compiler and
// client/public/charts.js:
//
//	timeseries    time, series, value
//	categorical   category, value
//	distribution  bucket_start, bucket_end, count
//	heatmap       time, bucket_start, bucket_end, count
//	scatter       x, y, label, event_id
//	ohlc          time, open, high, low, close        (candlestick)
//	              time, min, p25, p75, max            (box — same four slots)
//	single        value, previous
type Frame struct {
	Shape  string   `json:"shape"`
	Panel  string   `json:"panel"`
	Fields []Field  `json:"fields"`
	Rows   [][]any  `json:"rows"`
	Series []string `json:"series,omitempty"`
	Unit   string   `json:"unit,omitempty"`
	// Spark is the single-value shapes' history: the same measurement, bucketed
	// across the same window. A stat panel showing 412 with a line that has
	// been climbing all morning says something 412 alone does not.
	Spark []float64 `json:"spark,omitempty"`
	// Notes carry what the query had to do to stay bounded — a truncated
	// category list, an empty window. Silently dropping rows would read as
	// "this is everything", which is the one thing a dashboard must not lie
	// about.
	Notes []string `json:"notes,omitempty"`
}

type Field struct {
	Name string `json:"name"`
	Type string `json:"type"` // time | number | string
}

// Trace is one request, flattened for a waterfall.
//
// Depth and OffsetMS are computed here rather than in the browser because the
// parent/child walk needs every span at once and the renderer only wants to
// know where to put a bar. Spans arrive depth-first, so drawing them in order
// produces the Chrome-network-panel layout without the renderer sorting
// anything.
type Trace struct {
	TraceID    string      `json:"trace_id"`
	Start      time.Time   `json:"start"`
	DurationMS float64     `json:"duration_ms"`
	Services   []string    `json:"services"`
	Errors     int         `json:"errors"`
	Spans      []TraceSpan `json:"spans"`
}

type TraceSpan struct {
	ID           uint64    `json:"id"`
	SpanID       string    `json:"span_id"`
	ParentSpanID string    `json:"parent_span_id,omitempty"`
	Name         string    `json:"name"`
	Service      string    `json:"service"`
	Kind         string    `json:"kind,omitempty"`
	Severity     string    `json:"severity,omitempty"`
	Start        time.Time `json:"start"`
	// OffsetMS is milliseconds from the start of the trace: the bar's left
	// edge. DurationMS is its width.
	OffsetMS   float64 `json:"offset_ms"`
	DurationMS float64 `json:"duration_ms"`
	Depth      int     `json:"depth"`
	// Orphan marks a span whose parent is not in this trace — a partial export,
	// or a parent still in flight. It is drawn at the root rather than hidden.
	Orphan bool `json:"orphan,omitempty"`
}

// Selection is one mark on a panel, expressed in the terms the query already
// uses: a time bucket, a series key, a value range. It is what turns a chart
// back into the events it was drawn from.
//
// Every field is optional and they compose. A bar on a categorical chart sends
// only Series; a histogram bar sends only Min and Max; a heatmap cell sends a
// bucket and a range; a point on a time series sends a bucket and a series.
type Selection struct {
	From time.Time `json:"from,omitempty"`
	To   time.Time `json:"to,omitempty"`
	// Series is compared against the same expression the panel grouped by, so
	// "(none)" here means what it means on the axis: the events the group field
	// does not cover.
	Series    string `json:"series,omitempty"`
	HasSeries bool   `json:"has_series,omitempty"`
	// Min and Max bound the value field — pointers because a histogram bucket
	// starting at exactly zero is a real bucket, and "unset" has to differ.
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

// Drill is what the drawer shows: the events behind one mark, and how many
// there were in total. The two differ whenever a bar is taller than the list is
// long, and saying so is the difference between "these are the 100 slowest" and
// "this is all of it".
type Drill struct {
	Total  int     `json:"total"`
	Events []Event `json:"events"`
}

// Fields is what the builder offers: the columns every event has, plus the
// attribute keys this instance has actually seen.
type Fields struct {
	Columns    []FieldInfo `json:"columns"`
	Attributes []FieldInfo `json:"attributes"`
}

type FieldInfo struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
	Type  string `json:"type"`
	// Indexed is true for the attributes with a generated column behind them.
	// The builder says so, because the difference between an indexed and an
	// unindexed group-by is the difference between a panel and a stalled page.
	Indexed bool `json:"indexed,omitempty"`
}

// Shapes.
const (
	ShapeTimeseries   = "timeseries"
	ShapeCategorical  = "categorical"
	ShapeDistribution = "distribution"
	ShapeHeatmap      = "heatmap"
	ShapeScatter      = "scatter"
	ShapeOHLC         = "ohlc"
	ShapeSingle       = "single"
	ShapeSpanTree     = "span_tree"
)

// PanelSpec is what a panel needs, in the terms the builder asks the user for.
type PanelSpec struct {
	Panel string `json:"panel"`
	Label string `json:"label"`
	Shape string `json:"shape"`
	Hint  string `json:"hint"`
	// Needs tells the builder which controls to show: group, bucket, value,
	// buckets, x, trace.
	Needs []string `json:"needs"`
}

// Panels is the whole catalogue. Adding one is an entry here plus a renderer;
// adding one that reads an existing shape needs no compiler change at all.
var Panels = []PanelSpec{
	{Panel: "timeseries", Label: "Time series", Shape: ShapeTimeseries, Needs: []string{"bucket", "agg", "value", "group"},
		Hint: "Rate or latency over time. The default for anything with a clock."},
	{Panel: "bar_timeseries", Label: "Bar time series", Shape: ShapeTimeseries, Needs: []string{"bucket", "agg", "value", "group"},
		Hint: "The same frame drawn as columns — better for sparse counts."},
	{Panel: "state_timeline", Label: "State timeline", Shape: ShapeTimeseries, Needs: []string{"bucket", "agg", "value", "group"},
		Hint: "One lane per series, coloured by value. For state changes, not magnitudes."},
	{Panel: "status_history", Label: "Status history", Shape: ShapeTimeseries, Needs: []string{"bucket", "agg", "value", "group"},
		Hint: "Discrete blocks per bucket — periodic health, not continuous signal."},
	{Panel: "bar", Label: "Bar chart", Shape: ShapeCategorical, Needs: []string{"group", "agg", "value", "limit"},
		Hint: "Top routes, busiest clients, errors by service."},
	{Panel: "pie", Label: "Pie chart", Shape: ShapeCategorical, Needs: []string{"group", "agg", "value", "limit"},
		Hint: "Shares of a whole. Unreadable past about eight slices."},
	{Panel: "histogram", Label: "Histogram", Shape: ShapeDistribution, Needs: []string{"value", "buckets"},
		Hint: "Where the values actually fall. The panel that finds bimodal latency."},
	{Panel: "heatmap", Label: "Heatmap", Shape: ShapeHeatmap, Needs: []string{"value", "bucket", "buckets"},
		Hint: "Distribution over time. The classic latency heatmap."},
	{Panel: "scatter", Label: "XY chart", Shape: ShapeScatter, Needs: []string{"x", "value", "group", "limit"},
		Hint: "One dot per event. Click a dot to open it."},
	{Panel: "trend", Label: "Trend", Shape: ShapeScatter, Needs: []string{"x", "value", "group", "limit"},
		Hint: "A line over a numeric x that is not time — payload size, attempt number."},
	{Panel: "candlestick", Label: "Candlestick", Shape: ShapeOHLC, Needs: []string{"value", "bucket"},
		Hint: "Open/high/low/close per bucket. Meaningful for a sampled quantity — queue depth, active carts — where opening and closing values exist."},
	{Panel: "box", Label: "Box / range", Shape: ShapeOHLC, Needs: []string{"value", "bucket"},
		Hint: "Min/p25/p75/max per bucket. The honest one for discrete events, where first and last in a bucket mean nothing."},
	{Panel: "stat", Label: "Stat", Shape: ShapeSingle, Needs: []string{"agg", "value"},
		Hint: "One big number, with the change against the previous window."},
	{Panel: "gauge", Label: "Gauge", Shape: ShapeSingle, Needs: []string{"agg", "value", "thresholds"},
		Hint: "How far one number is from its threshold."},
	{Panel: "bar_gauge", Label: "Bar gauge", Shape: ShapeSingle, Needs: []string{"agg", "value", "thresholds"},
		Hint: "The same number as a filled bar."},
	{Panel: "waterfall", Label: "Trace waterfall", Shape: ShapeSpanTree, Needs: []string{"trace"},
		Hint: "One trace as a Gantt chart, parents above children."},
}

func PanelByName(name string) (PanelSpec, bool) {
	for _, p := range Panels {
		if p.Panel == name {
			return p, true
		}
	}
	return PanelSpec{}, false
}

// ShapeOf is the panel-to-shape lookup the compiler switches on.
func ShapeOf(panel string) string {
	spec, ok := PanelByName(panel)
	if !ok {
		return ""
	}
	return spec.Shape
}

// Aggregations. Percentiles are computed exactly, from ordered rows — SQLite
// has no percentile function and an interpolated estimate would be a strange
// thing to introduce silently.
var Aggregations = []string{"count", "sum", "avg", "min", "max", "p50", "p75", "p90", "p95", "p99"}

var conditionOps = map[string]bool{
	"eq": true, "ne": true, "contains": true, "prefix": true,
	"gt": true, "gte": true, "lt": true, "lte": true,
	"exists": true, "missing": true,
}

// Columns an expression may name. The compiler never interpolates a field the
// caller supplied — it looks it up here, or treats it as an attribute key and
// binds it as a parameter — so a field reference cannot become SQL.
var Columns = map[string]FieldInfo{
	"timestamp":      {Ref: "timestamp", Label: "Time", Type: "time"},
	"signal":         {Ref: "signal", Label: "Signal", Type: "string"},
	"service":        {Ref: "service", Label: "Service", Type: "string"},
	"instance":       {Ref: "instance", Label: "Instance", Type: "string"},
	"scope":          {Ref: "scope", Label: "Scope", Type: "string"},
	"name":           {Ref: "name", Label: "Name", Type: "string"},
	"severity":       {Ref: "severity", Label: "Severity / status", Type: "string"},
	"message":        {Ref: "message", Label: "Message", Type: "string"},
	"trace_id":       {Ref: "trace_id", Label: "Trace ID", Type: "string"},
	"span_id":        {Ref: "span_id", Label: "Span ID", Type: "string"},
	"parent_span_id": {Ref: "parent_span_id", Label: "Parent span", Type: "string"},
	"kind":           {Ref: "kind", Label: "Span kind", Type: "string"},
	"duration_ms":    {Ref: "duration_ms", Label: "Duration (ms)", Type: "number"},
	"value":          {Ref: "value", Label: "Metric value", Type: "number"},
	"unit":           {Ref: "unit", Label: "Unit", Type: "string"},
	"metric_type":    {Ref: "metric_type", Label: "Metric type", Type: "string"},
}

// AttributeRef reports whether a field reference names an attribute, and which.
func AttributeRef(field string) (string, bool) {
	key, ok := strings.CutPrefix(field, "attr:")
	key = strings.TrimSpace(key)
	return key, ok && key != ""
}

// ValidField is the check both halves run: the server before compiling, and the
// builder before it lets you save.
func ValidField(field string) bool {
	if _, ok := AttributeRef(field); ok {
		return true
	}
	_, ok := Columns[field]
	return ok
}

// ParseDuration accepts the compact windows the dashboard speaks, so "15m" and
// "7d" mean the same thing in a saved view as in the filter bar.
func ParseDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty duration")
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(value, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", value)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return d, nil
}

// Window resolves Range/From/To into the two instants the query filters on. A
// zero From means "no lower bound", which is what "all" asks for.
func (q ViewQuery) Window(now time.Time) (from, to time.Time) {
	from, to = q.From, q.To
	if !from.IsZero() || !to.IsZero() {
		return from, to
	}
	if q.Range == "" || q.Range == "all" {
		return time.Time{}, time.Time{}
	}
	d, err := ParseDuration(q.Range)
	if err != nil || d <= 0 {
		return time.Time{}, time.Time{}
	}
	return now.Add(-d), now
}

// BucketSize is the time granularity. "auto" aims for sixty buckets across the
// window, rounded to a duration a human reads on an axis; with no window at all
// it falls back to a minute, because the alternative is one bucket per
// nanosecond of whatever happens to be in the table.
func (q ViewQuery) BucketSize(now time.Time) time.Duration {
	if q.Bucket != "" && q.Bucket != "auto" {
		if d, err := ParseDuration(q.Bucket); err == nil && d > 0 {
			return d
		}
	}
	from, to := q.Window(now)
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return time.Minute
	}
	target := to.Sub(from) / 60
	steps := []time.Duration{
		time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
		time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute,
		time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
	}
	for _, step := range steps {
		if target <= step {
			return step
		}
	}
	return steps[len(steps)-1]
}

// Validate is core/api's hook, and it also runs in the browser: the builder
// imports this package, so a view the server would reject is rejected before it
// costs a round trip.
//
// It checks structure, never taste. "Does this query produce the columns this
// panel reads" is answerable and worth failing on; "are four hundred pie slices
// a good idea" is the author's business, and a validator that refused it would
// be wrong as often as it was right.
func (v View) Validate() error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("name is required")
	}
	if len(v.Name) > 120 {
		return errors.New("name must be 120 characters or fewer")
	}
	if v.Width < 0 || v.Width > 12 {
		return errors.New("width must be between 1 and 12")
	}
	return v.Query.ValidateFor(v.Panel)
}

// ValidateFor is the structural check: every field the panel's shape reads has
// to be present and has to name something real.
func (q ViewQuery) ValidateFor(panel string) error {
	spec, ok := PanelByName(panel)
	if !ok {
		return fmt.Errorf("unknown panel %q", panel)
	}
	switch q.Signal {
	case "", "logs", "traces", "metrics":
	default:
		return fmt.Errorf("unknown signal %q", q.Signal)
	}
	for _, c := range q.Filters {
		if !conditionOps[c.Op] {
			return fmt.Errorf("unknown operator %q", c.Op)
		}
		if !ValidField(c.Field) {
			return fmt.Errorf("unknown field %q", c.Field)
		}
	}
	if q.GroupBy != "" && !ValidField(q.GroupBy) {
		return fmt.Errorf("unknown group field %q", q.GroupBy)
	}
	if q.Value != "" && !ValidField(q.Value) {
		return fmt.Errorf("unknown value field %q", q.Value)
	}
	if q.X != "" && !ValidField(q.X) {
		return fmt.Errorf("unknown x field %q", q.X)
	}
	if q.Agg != "" {
		valid := false
		for _, a := range Aggregations {
			valid = valid || a == q.Agg
		}
		if !valid {
			return fmt.Errorf("unknown aggregation %q", q.Agg)
		}
	}
	if q.Bucket != "" && q.Bucket != "auto" {
		if _, err := ParseDuration(q.Bucket); err != nil {
			return err
		}
	}
	if q.Range != "" && q.Range != "all" {
		if _, err := ParseDuration(q.Range); err != nil {
			return err
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return errors.New("to must not be before from")
	}
	if q.Buckets < 0 || q.Buckets > 200 {
		return errors.New("buckets must be between 1 and 200")
	}
	if q.Limit < 0 || q.Limit > 5000 {
		return errors.New("limit must be between 1 and 5000")
	}
	// A measurement that is not a count needs something to measure, and it has
	// to be numeric — averaging a service name is not a query the compiler can
	// answer, and it would return zeroes rather than an error if we let it.
	needsValue := q.Agg != "" && q.Agg != "count"
	for _, need := range spec.Needs {
		if need == "value" && shapeNeedsValue(spec.Shape) {
			needsValue = true
		}
	}
	if needsValue {
		if q.Value == "" {
			return fmt.Errorf("%s needs a value field", spec.Label)
		}
		if err := numericField(q.Value); err != nil {
			return err
		}
	}
	if spec.Shape == ShapeCategorical && q.GroupBy == "" {
		return fmt.Errorf("%s needs a field to group by", spec.Label)
	}
	if spec.Shape == ShapeScatter && q.X == "" {
		return fmt.Errorf("%s needs an x field", spec.Label)
	}
	return nil
}

// The distribution shapes have no aggregation to speak of — they bucket the
// values themselves — so their value field is required even though Agg is not.
func shapeNeedsValue(shape string) bool {
	switch shape {
	case ShapeDistribution, ShapeHeatmap, ShapeOHLC, ShapeScatter:
		return true
	}
	return false
}

func numericField(field string) error {
	if _, ok := AttributeRef(field); ok {
		return nil // cast at query time; an attribute has no declared type
	}
	if Columns[field].Type != "number" {
		return fmt.Errorf("%q is not numeric", field)
	}
	return nil
}

// Normalize fills the defaults the compiler would otherwise have to guess at
// every call site, and is what the store stores: a view saved today keeps
// behaving the same way when the defaults change.
func (q ViewQuery) Normalize(panel string) ViewQuery {
	spec, _ := PanelByName(panel)
	if q.Agg == "" {
		q.Agg = "count"
	}
	if q.Bucket == "" {
		q.Bucket = "auto"
	}
	if q.Range == "" && q.From.IsZero() && q.To.IsZero() {
		q.Range = "1h"
	}
	if q.Buckets <= 0 {
		q.Buckets = 24
	}
	if q.Limit <= 0 {
		switch spec.Shape {
		case ShapeCategorical:
			q.Limit = 12
		case ShapeScatter:
			q.Limit = 2000
		default:
			q.Limit = 500
		}
	}
	if q.Order == "" {
		switch spec.Shape {
		case ShapeCategorical:
			q.Order = "value_desc"
		case ShapeSpanTree:
			q.Order = "latest"
		default:
			q.Order = "time_asc"
		}
	}
	return q
}
