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
func action(t *testing.T, actions []model.Action, name string) model.Action {
	t.Helper()
	for _, a := range actions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("%s was never discovered: %#v", name, actions)
	return model.Action{}
}

// Discovery is what arrives; pinning is the one decision a person makes about
// it. The list carries both, and pin, unpin and reorder are the same request.
func TestAnalyticsActionsAreDiscoveredAndPinned(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click", "signup_click")
	visit(t, store, "b2", "/docs", "page_view", "docs_search")

	var discovered []model.Action
	if code := get(t, srv.URL+"/api/analytics/actions", &discovered); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// page_view is the Views column rather than a discovery, so it is never on
	// the list somebody pins from.
	if len(discovered) != 2 {
		t.Fatalf("discovered = %#v", discovered)
	}
	if signup := action(t, discovered, "signup_click"); signup.Events != 2 || signup.Pinned || signup.LastSeen.IsZero() {
		t.Fatalf("signup_click = %#v", signup)
	}

	// The whole set in one request, in the order the columns are drawn.
	var pinned []model.Action
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/actions",
		contract.ActionPins{Pinned: []string{"docs_search", "signup_click"}}, &pinned); code != http.StatusOK {
		t.Fatalf("pinning = %d", code)
	}
	if len(pinned) != 2 || pinned[0].Name != "docs_search" || pinned[1].Name != "signup_click" {
		t.Fatalf("pinned = %#v", pinned)
	}
	if !pinned[0].Pinned || pinned[0].Position != 0 || pinned[1].Position != 1 {
		t.Fatalf("the order was not stored: %#v", pinned)
	}

	// Unpinning is a shorter list, not a second verb — and an empty one is how
	// the last column goes.
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/actions",
		contract.ActionPins{Pinned: []string{"signup_click"}}, &pinned); code != http.StatusOK {
		t.Fatalf("unpinning = %d", code)
	}
	if action(t, pinned, "docs_search").Pinned || !action(t, pinned, "signup_click").Pinned {
		t.Fatalf("after unpinning = %#v", pinned)
	}
}

// A column with no cells reads as a page where nothing ever happened, so a name
// nothing has ever sent cannot be pinned — and page_view, which is counted but
// never discovered, is refused by the same rule rather than by a case of its own.
func TestPinningRefusesANameNothingHasSent(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click")

	for _, name := range []string{"never_fired", "page_view"} {
		if code := call(t, http.MethodPost, srv.URL+"/api/analytics/actions",
			contract.ActionPins{Pinned: []string{name}}, nil); code != http.StatusBadRequest {
			t.Fatalf("pinning %s = %d, want 400", name, code)
		}
	}
	// The refusal is the whole request: signup_click is not left pinned by a
	// half-applied list.
	var actions []model.Action
	get(t, srv.URL+"/api/analytics/actions", &actions)
	if action(t, actions, "signup_click").Pinned {
		t.Fatalf("a refused pin was applied anyway: %#v", actions)
	}
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/actions",
		contract.ActionPins{Pinned: []string{"signup_click", "signup_click"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("pinning one name twice = %d, want 400", code)
	}
}

// Deleting an action drops the history counted under it, which is what the
// dialog above it has to say. The page views on the same paths are a different
// action and stand.
func TestDeletingAnActionTakesWhatWasCountedUnderIt(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click")
	visit(t, store, "b2", "/pricing", "page_view", "signup_click")

	if code := call(t, http.MethodDelete, srv.URL+"/api/analytics/signup_click", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete = %d", code)
	}
	var body contract.Analytics
	if code := get(t, srv.URL+"/api/analytics?range=24h", &body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	pricing := row(t, body.Paths, "/pricing")
	if _, drawn := pricing.Actions["signup_click"]; drawn {
		t.Fatalf("the rollup rows outlived the action: %#v", pricing)
	}
	if pricing.Views != 2 || pricing.Sessions != 2 {
		t.Fatalf("deleting an action took the page views with it: %#v", pricing)
	}
	var actions []model.Action
	get(t, srv.URL+"/api/analytics/actions", &actions)
	if len(actions) != 0 {
		t.Fatalf("actions = %#v", actions)
	}
	// Answering 204 for a name that is not there would be guard agreeing to a
	// deletion it did not make.
	if code := call(t, http.MethodDelete, srv.URL+"/api/analytics/signup_click", nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleting it twice = %d, want 404", code)
	}
}

// Reading what the tracker found is open; deciding the columns and dropping the
// history are not.
func TestAnalyticsActionWritesNeedTheToken(t *testing.T) {
	store, srv := server(t, "secret")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click")

	if code := getWith(t, srv.URL+"/api/analytics/actions", "secret", nil); code != http.StatusOK {
		t.Fatalf("reading with the token = %d", code)
	}
	if code := post(t, srv.URL+"/api/analytics/actions", []byte(`{"pinned":["signup_click"]}`), ""); code != http.StatusUnauthorized {
		t.Fatalf("pinning without a token = %d, want 401", code)
	}
	if code := send(t, http.MethodDelete, srv.URL+"/api/analytics/signup_click", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("deleting without a token = %d, want 401", code)
	}
	if code := post(t, srv.URL+"/api/analytics/actions", []byte(`{"pinned":["signup_click"]}`), "secret"); code != http.StatusOK {
		t.Fatalf("pinning with the token = %d", code)
	}
}

func rule(pattern, replacement string) model.PathRule {
	return model.PathRule{Pattern: pattern, Replacement: replacement}
}

// A rule is applied at ingest, so it shapes what is counted rather than what is
// drawn — and the raw feed keeps the URL that arrived, because that is the path
// somebody reads to write the next rule.
func TestAnalyticsPathRulesShapeWhatIsCounted(t *testing.T) {
	store, srv := server(t, "")

	var stored []model.PathRule
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/rules",
		contract.PathRuleSet{Rules: []model.PathRule{rule("/users/*", "/users/:id")}}, &stored); code != http.StatusOK {
		t.Fatalf("saving = %d", code)
	}
	if len(stored) != 1 || stored[0].Replacement != "/users/:id" || stored[0].ID == 0 {
		t.Fatalf("stored = %#v", stored)
	}

	visit(t, store, "a1", "/users/7", "page_view")
	visit(t, store, "b2", "/users/9", "page_view")
	visit(t, store, "c3", "/pricing", "page_view")

	var body contract.Analytics
	if code := get(t, srv.URL+"/api/analytics?range=24h", &body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if collapsed := row(t, body.Paths, "/users/:id"); collapsed.Views != 2 || collapsed.Sessions != 2 {
		t.Fatalf("/users/:id = %#v", collapsed)
	}
	if len(body.Paths) != 2 {
		t.Fatalf("a rule that collapses two pages left %#v", body.Paths)
	}

	// The list is read back in the order it is applied, and the preview beside
	// it still sees the URLs as they arrived rather than the row they landed in.
	var read []model.PathRule
	if code := get(t, srv.URL+"/api/analytics/rules", &read); code != http.StatusOK || len(read) != 1 {
		t.Fatalf("reading the rules = %d, %#v", code, read)
	}
	var preview []contract.PathPreview
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/preview",
		contract.PathRuleSet{Rules: read}, &preview); code != http.StatusOK {
		t.Fatalf("preview = %d", code)
	}
	if len(preview) != 3 {
		t.Fatalf("the raw feed was collapsed too: %#v", preview)
	}

	// The first match wins, so a second rule for the same pattern could never
	// fire — it is refused while somebody is typing it rather than stored and
	// left looking like configuration.
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/rules",
		contract.PathRuleSet{Rules: []model.PathRule{rule("/users/*", "/users/:id"), rule("/users/*", "/u")}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a duplicate pattern = %d, want 400", code)
	}
	if code := get(t, srv.URL+"/api/analytics/rules", &read); code != http.StatusOK || len(read) != 1 {
		t.Fatalf("a refused save was applied anyway: %#v", read)
	}
}

// The preview is the same call the save runs, against the paths that actually
// arrived — so a rule is proved before it is stored, which is the only chance
// there is: applying one cannot rewrite the days already rolled up.
func TestAnalyticsPathRulePreviewProvesARuleBeforeItIsStored(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/users/7", "page_view")
	visit(t, store, "b2", "/pricing", "page_view")
	visit(t, store, "c3", "/pricing", "page_view")

	var preview []contract.PathPreview
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/preview",
		contract.PathRuleSet{Rules: []model.PathRule{rule("/users/*", "/users/:id")}}, &preview); code != http.StatusOK {
		t.Fatalf("preview = %d", code)
	}
	// Distinct paths, most recently seen first: /pricing twice is one line of a
	// dialog somebody has to read.
	if len(preview) != 2 || preview[0].Path != "/pricing" {
		t.Fatalf("preview = %#v", preview)
	}
	if preview[0].Result != "/pricing" {
		t.Fatalf("a path no rule matches is its own page: %#v", preview[0])
	}
	if preview[1].Path != "/users/7" || preview[1].Result != "/users/:id" {
		t.Fatalf("the rule was not applied: %#v", preview[1])
	}

	// Proving one stores nothing. A preview that saved would be the press.
	var rules []model.PathRule
	if code := get(t, srv.URL+"/api/analytics/rules", &rules); code != http.StatusOK || len(rules) != 0 {
		t.Fatalf("the preview stored the rule: %d, %#v", code, rules)
	}

	// A pattern that will not compile can never fire, and the refusal lands in
	// the dialog rather than on the save.
	if code := call(t, http.MethodPost, srv.URL+"/api/analytics/preview",
		contract.PathRuleSet{Rules: []model.PathRule{rule("/users/[", "/users/:id")}}, nil); code != http.StatusBadRequest {
		t.Fatalf("a pattern that will not compile = %d, want 400", code)
	}
}

// Reading the rules and proving one are reads; storing them is not.
func TestAnalyticsPathRuleWritesNeedTheToken(t *testing.T) {
	_, srv := server(t, "secret")
	body := []byte(`{"rules":[{"pattern":"/users/*","replacement":"/users/:id"}]}`)

	if code := post(t, srv.URL+"/api/analytics/rules", body, ""); code != http.StatusUnauthorized {
		t.Fatalf("saving without a token = %d, want 401", code)
	}
	if code := post(t, srv.URL+"/api/analytics/rules", body, "secret"); code != http.StatusOK {
		t.Fatalf("saving with the token = %d", code)
	}
	if code := getWith(t, srv.URL+"/api/analytics/rules", "secret", nil); code != http.StatusOK {
		t.Fatalf("reading with the token = %d", code)
	}
}

// The counters reach the wire, and "off" is a value rather than an absence of
// rows: the page has to be able to say "analytics is not configured" without
// inferring it from an empty grid, which is also what a working tracker that
// nobody has visited yet looks like.
func TestAnalyticsHealthCarriesWhatWasThrownAway(t *testing.T) {
	store, srv := server(t, "")
	visit(t, store, "a1", "/pricing", "page_view", "signup_click")
	store.AnalyticsRejected()

	var body model.AnalyticsHealth
	if code := get(t, srv.URL+"/api/analytics/health", &body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Nothing mounted the browser door on this server, so the counters are
	// real and analytics is still off.
	if body.Enabled {
		t.Error("a server with no browser door says analytics is on")
	}
	if body.Rejected != 1 || body.Actions != 1 || body.SeenRows != 2 {
		t.Fatalf("health = %#v", body)
	}
	if body.LastEvent.IsZero() {
		t.Error("a beacon landed and the health says nothing has ever arrived")
	}
}
