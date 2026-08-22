package apis_test

import (
	"net/http"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
)

func visit(t *testing.T, store *telemetry.Store, session, path string, actions ...string) {
	t.Helper()
	beacon := model.Beacon{Session: session, Path: path}
	for _, name := range actions {
		beacon.Events = append(beacon.Events, model.TrackEvent{Name: name})
	}
	if err := store.AddAnalytics(beacon); err != nil {
		t.Fatal(err)
	}
}

func row(t *testing.T, grid []model.PathRow, path string) model.PathRow {
	t.Helper()
	for _, r := range grid {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("%s is not in the grid: %#v", path, grid)
	return model.PathRow{}
}

// The strip and the grid over one window, in one answer — and the two of them
// agreeing, which is the reason it is one request rather than two.
func TestAnalyticsGrid(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click")
	visit(t, store, "b2", "/pricing", "page_view")
	visit(t, store, "a1", "/docs", "page_view")

	var body contract.Analytics
	if code := get(t, srv.URL+"/api/analytics?range=24h", &body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	// Sessions come from analytics_seen, so a1 reading two pages is one
	// session: summing the grid's rows would say three and make views per
	// session read 1.0 forever.
	if body.Summary.Window.Sessions != 2 || body.Summary.Window.Views != 3 {
		t.Fatalf("the strip is %#v", body.Summary.Window)
	}
	if body.Summary.Window.ViewsPerSession != 1.5 || body.Summary.Window.ActionsPerSession != 0.5 {
		t.Fatalf("the ratios are %#v", body.Summary.Window)
	}
	// Nothing happened the day before, and an empty window is silence rather
	// than zero — the change figure beside the strip is drawn from this.
	if body.Summary.Previous.Sessions != 0 || body.Summary.Previous.ViewsPerSession != 0 {
		t.Fatalf("the previous window is %#v", body.Summary.Previous)
	}

	// Ordered by views, so the grid is read from the top.
	if len(body.Paths) != 2 || body.Paths[0].Path != "/pricing" {
		t.Fatalf("the grid is %#v", body.Paths)
	}
	pricing := row(t, body.Paths, "/pricing")
	cell := pricing.Actions["signup_click"]
	if pricing.Views != 2 || cell.Sessions != 1 || cell.Rate != 0.5 {
		t.Fatalf("/pricing is %#v", pricing)
	}
	// A dash, never a zero: a page with no signup button carries no cell at
	// all, so the renderer has nothing to draw 0.0% from.
	if _, drawn := row(t, body.Paths, "/docs").Actions["signup_click"]; drawn {
		t.Fatal("a page nobody could press it on carries a signup_click cell")
	}
}

// "All retained" is an answer the dashboard's own window control offers, and
// the one window analytics cannot have: the strip is a comparison against the
// span of equal length before it, and an unbounded window has no previous. It
// is refused in words rather than answered against the epoch.
func TestAnalyticsRefusesAnUnboundedWindow(t *testing.T) {
	_, srv := server(t, "")
	if code := get(t, srv.URL+"/api/analytics?range=all", nil); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	// Naming no window at all is not the same refusal: it is the default, so
	// the page's first paint does not have to know what to ask for.
	var body contract.Analytics
	if code := get(t, srv.URL+"/api/analytics", &body); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}
