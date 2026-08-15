package viewalerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type fakeStore struct {
	views  []model.View
	frame  model.Frame
	runErr error
	saved  map[int64]model.ViewAlert
}

func (f *fakeStore) WatchedViews() ([]model.View, error) {
	out := make([]model.View, len(f.views))
	copy(out, f.views)
	for i := range out {
		if alert, ok := f.saved[out[i].ID]; ok {
			copied := alert
			out[i].Alert = &copied
		}
	}
	return out, nil
}

func (f *fakeStore) RunView(string, model.ViewQuery) (model.Frame, error) {
	return f.frame, f.runErr
}

func (f *fakeStore) SaveViewAlertState(viewID int64, alert model.ViewAlert) error {
	if f.saved == nil {
		f.saved = map[int64]model.ViewAlert{}
	}
	stored := f.saved[viewID]
	stored.Enabled, stored.Op, stored.Threshold = true, alert.Op, alert.Threshold
	stored.ForSeconds, stored.WebhookID = alert.ForSeconds, alert.WebhookID
	stored.Firing, stored.Since, stored.Alerted = alert.Firing, alert.Since, alert.Alerted
	stored.Value, stored.Series = alert.Value, alert.Series
	f.saved[viewID] = stored
	return nil
}

func (f *fakeStore) DestinationFor(id int64) (notify.Destination, error) {
	return notify.Destination{ID: id, Name: "ops", URL: "https://hooks.example.com"}, nil
}

func (f *fakeStore) RecordDelivery(int64, time.Time, string) error { return nil }

type sender struct{ sent []notify.Event }

func (s *sender) Send(_ context.Context, _ notify.Destination, event notify.Event) error {
	s.sent = append(s.sent, event)
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func timeseries(rows ...[]any) model.Frame {
	return model.Frame{
		Shape: model.ShapeTimeseries,
		Fields: []model.Field{
			{Name: "time", Type: "time"}, {Name: "series", Type: "string"}, {Name: "value", Type: "number"},
		},
		Rows: rows,
	}
}

func watched(alert model.ViewAlert) []model.View {
	alert.Enabled = true
	alert.WebhookID = 1
	return []model.View{{ID: 1, Name: "Errors per minute", Panel: "timeseries", Alert: &alert}}
}

func TestFiresOnTheLatestValueAndNamesTheSeries(t *testing.T) {
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorAbove, Threshold: 10}),
		// Two series, two buckets each. The rule reads the last value of each
		// and the worst one decides — averaging them would hide exactly the
		// outage worth alerting on.
		frame: timeseries(
			[]any{"t0", "checkout", 2.0}, []any{"t0", "search", 1.0},
			[]any{"t1", "checkout", 42.0}, []any{"t1", "search", 3.0},
		),
	}
	out := &sender{}
	w := &Watcher{Store: store, Sender: out, Log: quiet()}

	raised := w.Round(context.Background())
	if len(raised) != 1 {
		t.Fatalf("raised %d events", len(raised))
	}
	event := raised[0]
	if event.Kind != notify.KindViewRule || event.State != notify.StateFiring {
		t.Fatalf("event = %+v", event)
	}
	if event.Fields["series"] != "checkout" || event.Fields["value"] != 42.0 {
		t.Fatalf("fields = %v, want the worst series named", event.Fields)
	}
	if event.Subject != "Errors per minute/checkout" {
		t.Fatalf("subject = %q", event.Subject)
	}
	// And not twice.
	if len(w.Round(context.Background())) != 0 {
		t.Fatal("the second pass said it again")
	}
}

func TestRecoveryIsItsOwnEvent(t *testing.T) {
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorAbove, Threshold: 10}),
		frame: timeseries([]any{"t1", "checkout", 42.0}),
	}
	out := &sender{}
	w := &Watcher{Store: store, Sender: out, Log: quiet()}
	w.Round(context.Background())

	store.frame = timeseries([]any{"t2", "checkout", 1.0})
	raised := w.Round(context.Background())
	if len(raised) != 1 || raised[0].State != notify.StateResolved {
		t.Fatalf("raised %+v, want one resolved event", raised)
	}
}

func TestAnEmptyWindowIsNotAZero(t *testing.T) {
	// A "below" rule over a frame with no rows is the trap: telemetry pausing
	// for a minute would page somebody every time, and a rule people turn off
	// catches nothing.
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorBelow, Threshold: 10}),
		frame: timeseries(),
	}
	w := &Watcher{Store: store, Sender: &sender{}, Log: quiet()}
	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d events about an empty window", len(raised))
	}
}

func TestAQueryThatWillNotRunIsNotAnAlert(t *testing.T) {
	// A field renamed out from under a saved view is guard's problem, not the
	// user's system going wrong — paging somebody about it would train them to
	// ignore the channel.
	store := &fakeStore{
		views:  watched(model.ViewAlert{Op: model.MonitorAbove, Threshold: 1}),
		runErr: errors.New("unknown field"),
	}
	w := &Watcher{Store: store, Sender: &sender{}, Log: quiet()}
	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d events about a broken query", len(raised))
	}
}

func TestTheHoldIsRespectedAndStored(t *testing.T) {
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorAbove, Threshold: 10, ForSeconds: 300}),
		frame: timeseries([]any{"t1", "", 42.0}),
	}
	w := &Watcher{Store: store, Sender: &sender{}, Log: quiet()}
	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatal("one bucket over the line is not five minutes over it")
	}
	if store.saved[1].Since.IsZero() {
		t.Fatal("the breach was not timed, so the hold could never be reached")
	}
	stored := store.saved[1]
	stored.Since = time.Now().Add(-6 * time.Minute)
	store.saved[1] = stored
	if raised := w.Round(context.Background()); len(raised) != 1 {
		t.Fatal("six minutes over a five minute hold is an alert")
	}
}

func TestSingleValuePanels(t *testing.T) {
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorBelow, Threshold: 100}),
		frame: model.Frame{
			Shape:  model.ShapeSingle,
			Fields: []model.Field{{Name: "value", Type: "number"}, {Name: "previous", Type: "number"}},
			Rows:   [][]any{{42.0, 90.0}},
		},
	}
	w := &Watcher{Store: store, Sender: &sender{}, Log: quiet()}
	raised := w.Round(context.Background())
	if len(raised) != 1 || raised[0].Fields["value"] != 42.0 {
		t.Fatalf("raised %+v", raised)
	}
}

func TestShapesWithNoReadingAreNotAlertable(t *testing.T) {
	for _, panel := range []string{"trace_waterfall", "heatmap", "histogram"} {
		if model.Alertable(panel) {
			t.Errorf("%s has no single number to draw a line across", panel)
		}
	}
	// A scatter does have one: the worst dot in the window. "The slowest
	// request in the last fifteen minutes" is the whole reason to put a
	// duration scatter on a dashboard.
	for _, panel := range []string{"timeseries", "stat", "bar", "scatter"} {
		if !model.Alertable(panel) {
			t.Errorf("%s does have one", panel)
		}
	}
}

func TestAScatterAlertsOnItsWorstPoint(t *testing.T) {
	store := &fakeStore{
		views: watched(model.ViewAlert{Op: model.MonitorAbove, Threshold: 5000}),
		frame: model.Frame{
			Shape: model.ShapeScatter,
			Unit:  "ms",
			Fields: []model.Field{
				{Name: "x", Type: "time"}, {Name: "y", Type: "number"},
				{Name: "label", Type: "string"}, {Name: "event_id", Type: "string"},
			},
			Rows: [][]any{
				{"t0", 120.0, "GET /health", "a"},
				{"t1", 9400.0, "POST /checkout", "b"},
				{"t2", 240.0, "GET /health", "c"},
			},
		},
	}
	out := &sender{}
	w := &Watcher{Store: store, Sender: out, Log: quiet()}
	raised := w.Round(context.Background())
	if len(raised) != 1 {
		t.Fatalf("raised %d events", len(raised))
	}
	if raised[0].Fields["value"] != 9400.0 || raised[0].Fields["series"] != "POST /checkout" {
		t.Fatalf("fields = %v, want the slowest request named", raised[0].Fields)
	}
}

func TestDurationsAreTypedInTheUnitTheChartIsDrawnIn(t *testing.T) {
	// A latency query is drawn in ms, so its threshold is typed in ms — a rule
	// in different units from the chart is one nobody can check by looking.
	if got := model.AlertUnit(model.ViewQuery{Value: "duration_ms", Agg: "p95"}); got != "ms" {
		t.Fatalf("unit = %q, want ms", got)
	}
	// Counting events is not measuring a duration, whatever is being counted.
	if got := model.AlertUnit(model.ViewQuery{Value: "duration_ms", Agg: "count"}); got != "" {
		t.Fatalf("unit = %q, want none", got)
	}
}
