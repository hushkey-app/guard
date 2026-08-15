package apis_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func spans(t *testing.T, store *telemetry.Store) {
	t.Helper()
	now := time.Now().UTC()
	events := []telemetry.Event{
		{Signal: "traces", Service: "api", Name: "GET /checkout", TraceID: "t1", SpanID: "root", Timestamp: now.Add(-time.Minute), DurationMS: 120,
			Attributes: map[string]any{"http.route": "/checkout", "client.address": "10.0.0.1"}},
		{Signal: "traces", Service: "db", Name: "SELECT cart", TraceID: "t1", SpanID: "child", ParentSpanID: "root", Timestamp: now.Add(-time.Minute), DurationMS: 40,
			Attributes: map[string]any{"db.system": "postgres"}},
		{Signal: "traces", Service: "api", Name: "GET /search", TraceID: "t2", SpanID: "s2", Timestamp: now, DurationMS: 20,
			Attributes: map[string]any{"http.route": "/search", "client.address": "10.0.0.2"}},
	}
	if err := store.Add(events...); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if out != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, url, err)
		}
	} else {
		io.Copy(io.Discard, response.Body) //nolint:errcheck
	}
	return response.StatusCode
}

// The round trip a dashboard actually makes: create a panel, read it back, run
// it, edit it, delete it.
func TestViewLifecycle(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)

	var created model.View
	code := call(t, http.MethodPost, srv.URL+"/api/views", model.View{
		Name:  "Requests by route",
		Panel: "bar",
		Width: 6,
		Query: model.ViewQuery{Signal: "traces", Range: "1h", GroupBy: "attr:http.route", Agg: "count"},
	}, &created)
	if code != http.StatusOK || created.ID == 0 {
		t.Fatalf("create = %d, id %d", code, created.ID)
	}

	var listed []model.View
	if code := call(t, http.MethodGet, srv.URL+"/api/views", nil, &listed); code != http.StatusOK || len(listed) != 1 {
		t.Fatalf("list = %d, %d views", code, len(listed))
	}

	var frame model.Frame
	if code := call(t, http.MethodGet, srv.URL+"/api/views/data?id="+itoa(created.ID), nil, &frame); code != http.StatusOK {
		t.Fatalf("data = %d", code)
	}
	// Three, not two: the database span carries no http.route, and rows the
	// group field does not cover are labelled rather than dropped. "Events with
	// no route" is an answer, and a bar chart that quietly excluded them would
	// not add up to the count on the overview page.
	if frame.Shape != model.ShapeCategorical || len(frame.Rows) != 3 {
		t.Fatalf("frame = %s with %d rows, want categorical with 3", frame.Shape, len(frame.Rows))
	}
	var labelled bool
	for _, row := range frame.Rows {
		labelled = labelled || row[0] == "(none)"
	}
	if !labelled {
		t.Error("the span without an http.route is missing — it should be grouped as (none)")
	}

	created.Name = "Requests by client"
	created.Query.GroupBy = "attr:client.address"
	var updated model.View
	if code := call(t, http.MethodPut, srv.URL+"/api/views/"+itoa(created.ID), created, &updated); code != http.StatusOK {
		t.Fatalf("update = %d", code)
	}
	if updated.Query.GroupBy != "attr:client.address" || updated.Name != "Requests by client" {
		t.Fatalf("update did not stick: %#v", updated)
	}

	if code := call(t, http.MethodDelete, srv.URL+"/api/views/"+itoa(created.ID), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := call(t, http.MethodDelete, srv.URL+"/api/views/"+itoa(created.ID), nil, nil); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
}

// The window on the request overrides the one the view was saved with — one
// time picker, sixteen panels, no stored queries rewritten.
func TestViewDataWindowOverride(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)
	if err := store.Add(telemetry.Event{
		Signal: "traces", Service: "api", Name: "old", TraceID: "t0", SpanID: "old",
		Timestamp: time.Now().UTC().Add(-48 * time.Hour), DurationMS: 5,
		Attributes: map[string]any{"http.route": "/ancient"},
	}); err != nil {
		t.Fatal(err)
	}

	var created model.View
	call(t, http.MethodPost, srv.URL+"/api/views", model.View{
		Name: "Routes", Panel: "bar",
		Query: model.ViewQuery{Signal: "traces", Range: "1h", GroupBy: "attr:http.route", Agg: "count"},
	}, &created)

	var scoped, wide model.Frame
	call(t, http.MethodGet, srv.URL+"/api/views/data?id="+itoa(created.ID), nil, &scoped)
	call(t, http.MethodGet, srv.URL+"/api/views/data?id="+itoa(created.ID)+"&range=7d", nil, &wide)
	// /checkout, /search and the routeless database span within the hour; the
	// two-day-old /ancient only once the window is widened.
	if len(scoped.Rows) != 3 {
		t.Fatalf("saved window returned %d rows, want 3", len(scoped.Rows))
	}
	if len(wide.Rows) != 4 {
		t.Fatalf("overridden window returned %d rows, want 4", len(wide.Rows))
	}
}

// Preview needs no token: finding out what a panel draws is reading, and only
// keeping it is writing.
func TestPreviewIsOpenButSavingIsNot(t *testing.T) {
	store, srv := server(t, "secret")
	spans(t, store)

	var frame model.Frame
	code := call(t, http.MethodPost, srv.URL+"/api/views/preview", map[string]any{
		"panel": "timeseries",
		"query": model.ViewQuery{Signal: "traces", Range: "1h", Agg: "p95", Value: "duration_ms"},
	}, &frame)
	if code != http.StatusOK {
		t.Fatalf("preview without a token = %d, want 200", code)
	}

	if code := call(t, http.MethodPost, srv.URL+"/api/views", model.View{
		Name: "x", Panel: "bar", Query: model.ViewQuery{GroupBy: "service", Agg: "count"},
	}, nil); code != http.StatusUnauthorized {
		t.Fatalf("save without a token = %d, want 401", code)
	}
}

// A query the compiler cannot answer is the caller's mistake, and it is told
// which part — not handed an empty chart to puzzle over.
func TestInvalidPanelsAreRejectedWithAReason(t *testing.T) {
	_, srv := server(t, "")
	for _, tc := range []struct {
		name  string
		panel string
		query model.ViewQuery
	}{
		{"no grouping", "bar", model.ViewQuery{Agg: "count"}},
		{"average of a string", "timeseries", model.ViewQuery{Agg: "avg", Value: "service"}},
		{"unknown panel", "sankey", model.ViewQuery{}},
		{"injected field", "bar", model.ViewQuery{GroupBy: "service); DROP TABLE events; --", Agg: "count"}},
	} {
		code := call(t, http.MethodPost, srv.URL+"/api/views/preview", map[string]any{"panel": tc.panel, "query": tc.query}, nil)
		if code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.name, code)
		}
	}
}

func TestCatalogueDescribesWhatThisBinaryCanDo(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)

	var catalogue struct {
		Panels       []model.PanelSpec `json:"panels"`
		Aggregations []string          `json:"aggregations"`
		Fields       model.Fields      `json:"fields"`
	}
	if code := call(t, http.MethodGet, srv.URL+"/api/views/catalogue", nil, &catalogue); code != http.StatusOK {
		t.Fatalf("catalogue = %d", code)
	}
	if len(catalogue.Panels) != len(model.Panels) {
		t.Fatalf("panels = %d, want %d", len(catalogue.Panels), len(model.Panels))
	}
	// Every panel offered has to be one the compiler can actually run, or the
	// builder offers a choice that fails on save.
	for _, spec := range catalogue.Panels {
		if model.ShapeOf(spec.Panel) == "" {
			t.Errorf("%s has no shape", spec.Panel)
		}
	}
	var indexed bool
	for _, field := range catalogue.Fields.Attributes {
		indexed = indexed || field.Indexed
	}
	if !indexed {
		t.Error("no indexed attributes offered")
	}
}

// The sample dashboard is one panel per visualisation, and every one of them
// has to actually run — a sample that draws an error is worse than no sample.
func TestSamplePanelsAllRender(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)

	var created []model.View
	if code := call(t, http.MethodPost, srv.URL+"/api/views/samples", nil, &created); code != http.StatusOK {
		t.Fatalf("samples = %d", code)
	}
	if len(created) != len(model.Panels) {
		t.Fatalf("created %d panels, want one per visualisation (%d)", len(created), len(model.Panels))
	}
	drawn := map[string]bool{}
	for _, view := range created {
		drawn[view.Panel] = true
		var frame model.Frame
		if code := call(t, http.MethodGet, srv.URL+"/api/views/data?id="+itoa(view.ID), nil, &frame); code != http.StatusOK {
			t.Errorf("%s (%s) = %d", view.Name, view.Panel, code)
			continue
		}
		if frame.Shape != model.ShapeOf(view.Panel) {
			t.Errorf("%s: shape %q, want %q", view.Panel, frame.Shape, model.ShapeOf(view.Panel))
		}
	}
	for _, spec := range model.Panels {
		if !drawn[spec.Panel] {
			t.Errorf("no sample for %s", spec.Panel)
		}
	}

	// Running it twice must not double the dashboard.
	var again []model.View
	call(t, http.MethodPost, srv.URL+"/api/views/samples", nil, &again)
	if len(again) != 0 {
		t.Errorf("second run created %d more panels", len(again))
	}
}

// Dragging a card rewrites the whole layout in one request, and the answer is
// what was stored — so the browser can settle on that rather than trusting its
// own optimistic shuffle.
func TestReorderEndpoint(t *testing.T) {
	store, srv := server(t, "secret")
	spans(t, store)

	var ids []int64
	for _, name := range []string{"one", "two", "three"} {
		var created model.View
		call(t, http.MethodPost, srv.URL+"/api/views", model.View{
			Name: name, Panel: "bar", Query: model.ViewQuery{GroupBy: "service", Agg: "count"},
		}, &created)
		ids = append(ids, created.ID)
	}
	// Creating needed the token; the helper does not send one, so this
	// dashboard is empty and the reorder below has nothing to do — which is the
	// point of the next assertion.
	if code := call(t, http.MethodPut, srv.URL+"/api/views/order", contractOrder{IDs: []int64{1}}, nil); code != http.StatusUnauthorized {
		t.Fatalf("reorder without a token = %d, want 401", code)
	}

	_, open := server(t, "")
	ids = ids[:0]
	for _, name := range []string{"one", "two", "three"} {
		var created model.View
		call(t, http.MethodPost, open.URL+"/api/views", model.View{
			Name: name, Panel: "bar", Query: model.ViewQuery{GroupBy: "service", Agg: "count"},
		}, &created)
		ids = append(ids, created.ID)
	}
	var reordered []model.View
	if code := call(t, http.MethodPut, open.URL+"/api/views/order", contractOrder{IDs: []int64{ids[2], ids[1], ids[0]}}, &reordered); code != http.StatusOK {
		t.Fatalf("reorder = %d", code)
	}
	if len(reordered) != 3 || reordered[0].Name != "three" || reordered[2].Name != "one" {
		t.Fatalf("order = %v", []string{reordered[0].Name, reordered[1].Name, reordered[2].Name})
	}
	// An empty list is a client bug, not an instruction to flatten the layout.
	if code := call(t, http.MethodPut, open.URL+"/api/views/order", contractOrder{}, nil); code != http.StatusBadRequest {
		t.Errorf("empty order = %d, want 400", code)
	}
}

type contractOrder struct {
	IDs []int64 `json:"ids"`
}

// Clicking a bar asks for the events behind it, and gets exactly those.
func TestDrillEndpoint(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)

	var drill model.Drill
	code := call(t, http.MethodPost, srv.URL+"/api/views/drill", map[string]any{
		"panel":     "bar",
		"query":     model.ViewQuery{Signal: "traces", Range: "1h", GroupBy: "attr:http.route", Agg: "count"},
		"selection": model.Selection{Series: "/checkout", HasSeries: true},
	}, &drill)
	if code != http.StatusOK {
		t.Fatalf("drill = %d", code)
	}
	if drill.Total != 1 || len(drill.Events) != 1 || drill.Events[0].Name != "GET /checkout" {
		t.Fatalf("drill = %#v", drill)
	}

	// Reading, so it needs no token even where writing does.
	_, secured := server(t, "secret")
	if code := call(t, http.MethodPost, secured.URL+"/api/views/drill", map[string]any{
		"panel": "bar", "query": model.ViewQuery{GroupBy: "service", Agg: "count"}, "selection": model.Selection{},
	}, nil); code != http.StatusOK {
		t.Errorf("drill without a token = %d, want 200", code)
	}
}

// Adding a node decides which URLs guard will fetch, on a timer, from inside
// whatever network it runs in — so it is admin even though it looks like a
// bookmark.
func TestClusterEndpoints(t *testing.T) {
	_, secured := server(t, "secret")
	if code := call(t, http.MethodPost, secured.URL+"/api/cluster", model.Node{Name: "VPS-1", URL: "https://example.com/health"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("adding a node without a token = %d, want 401", code)
	}

	_, srv := server(t, "")
	var node model.Node
	if code := call(t, http.MethodPost, srv.URL+"/api/cluster", model.Node{Name: "VPS-1", URL: "https://example.com/health"}, &node); code != http.StatusOK {
		t.Fatalf("add = %d", code)
	}
	if !node.Enabled || node.Status != model.StatusUnknown {
		t.Fatalf("new node = %#v; it should be watched and not yet checked", node)
	}

	for _, bad := range []string{"", "not-a-url", "file:///etc/passwd"} {
		if code := call(t, http.MethodPost, srv.URL+"/api/cluster", model.Node{Name: "x", URL: bad}, nil); code != http.StatusBadRequest {
			t.Errorf("url %q = %d, want 400", bad, code)
		}
	}

	node.Enabled = false
	var paused model.Node
	if code := call(t, http.MethodPut, srv.URL+"/api/cluster/"+itoa(node.ID), node, &paused); code != http.StatusOK || paused.Enabled {
		t.Fatalf("pause = %d, enabled %v", code, paused.Enabled)
	}

	var listed []model.Node
	call(t, http.MethodGet, srv.URL+"/api/cluster", nil, &listed)
	if len(listed) != 1 {
		t.Fatalf("listed %d nodes", len(listed))
	}

	if code := call(t, http.MethodDelete, srv.URL+"/api/cluster/"+itoa(node.ID), nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := call(t, http.MethodDelete, srv.URL+"/api/cluster/"+itoa(node.ID), nil, nil); code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", code)
	}
}

// The test server wires no prober, which is the case a running binary should
// never be in — but an endpoint that panicked over it would take the whole API
// down rather than the one request.
func TestCheckNowWithoutAProber(t *testing.T) {
	_, srv := server(t, "")
	if code := call(t, http.MethodPost, srv.URL+"/api/cluster/check", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("check with no prober = %d, want 503", code)
	}
}

func TestTraceEndpoint(t *testing.T) {
	store, srv := server(t, "")
	spans(t, store)

	var trace model.Trace
	if code := call(t, http.MethodGet, srv.URL+"/api/traces/t1", nil, &trace); code != http.StatusOK {
		t.Fatalf("trace = %d", code)
	}
	if len(trace.Spans) != 2 || trace.Spans[0].Depth != 0 || trace.Spans[1].Depth != 1 {
		t.Fatalf("spans = %#v", trace.Spans)
	}
	if code := call(t, http.MethodGet, srv.URL+"/api/traces/nope", nil, nil); code != http.StatusNotFound {
		t.Fatalf("missing trace = %d, want 404", code)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
