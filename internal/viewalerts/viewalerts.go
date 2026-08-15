// Package viewalerts runs the saved views that carry a rule, and says so when
// their number crosses the line.
//
// It is a third watcher beside the machine monitors and the staleness watch,
// with the same bargain: the thing being watched is something guard already
// draws, the delivery goes through internal/notify to a named destination, and
// a rule that fired says so again when it stops.
//
// What makes this one different is where the number comes from. A machine rule
// reads a figure the prober already collected; this runs somebody's stored
// query, which is real work against SQLite. So it is a slow loop by default and
// the query it runs is the *same* one the panel draws — never a variant, never
// a rewritten window — because an alert that fires on a number nobody can see
// on the dashboard is an alert nobody trusts.
package viewalerts

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Store is what the loop needs: the views carrying a rule, a way to run one,
// somewhere to keep the judgement, and the destinations.
type Store interface {
	WatchedViews() ([]model.View, error)
	RunView(panel string, query model.ViewQuery) (model.Frame, error)
	SaveViewAlertState(viewID int64, alert model.ViewAlert) error
	DestinationFor(id int64) (notify.Destination, error)
	RecordDelivery(id int64, at time.Time, failure string) error
}

// Watcher evaluates the view rules on its own timer.
type Watcher struct {
	Store  Store
	Sender notify.Sender
	// Interval is how often to run them. A minute by default rather than the
	// thirty seconds the machine rules get: each pass is one compiled query per
	// watched view against the events table, which is the same table the
	// dashboard is reading.
	Interval time.Duration
	Repeat   time.Duration
	Log      *slog.Logger

	once sync.Once
}

const (
	defaultInterval = time.Minute
	defaultRepeat   = 6 * time.Hour
)

func (w *Watcher) prepare() {
	w.once.Do(func() {
		if w.Log == nil {
			w.Log = slog.Default()
		}
		if w.Interval <= 0 {
			w.Interval = defaultInterval
		}
		if w.Repeat <= 0 {
			w.Repeat = defaultRepeat
		}
		if w.Sender == nil {
			w.Sender = &notify.Webhook{}
		}
	})
}

func (w *Watcher) Run(ctx context.Context) {
	w.prepare()
	w.Log.Info("view alerts started", slog.Duration("interval", w.Interval))
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		w.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Round runs every watched view once and returns the events it raised.
func (w *Watcher) Round(ctx context.Context) []notify.Event {
	w.prepare()
	views, err := w.Store.WatchedViews()
	if err != nil {
		w.Log.Error("view alerts could not read their views", slog.Any("err", err))
		return nil
	}
	now := time.Now()
	var raised []notify.Event
	for _, view := range views {
		if event := w.evaluate(ctx, view, now); event != nil {
			raised = append(raised, *event)
		}
	}
	return raised
}

func (w *Watcher) evaluate(ctx context.Context, view model.View, now time.Time) *notify.Event {
	alert := *view.Alert
	frame, err := w.Store.RunView(view.Panel, view.Query)
	if err != nil {
		// A query that no longer compiles — a field renamed, a panel changed —
		// is worth a line and no alert. Firing on it would page somebody about
		// guard rather than about their system.
		w.Log.Error("a watched view would not run",
			slog.String("view", view.Name), slog.Any("err", err))
		return nil
	}
	value, series, ok := frame.Reading(alert.Op)
	if !ok {
		// An empty window is not a zero. A rule that fired every time telemetry
		// paused is a rule somebody turns off, and a turned-off rule catches
		// nothing.
		return nil
	}
	alert.Value = value
	alert.Series = series

	if !alert.Breached(value) {
		if !alert.Firing {
			alert.Since = time.Time{}
			alert.Alerted = time.Time{}
			w.save(view.ID, alert)
			return nil
		}
		event := describe(view, alert, notify.StateResolved, now)
		alert.Firing = false
		alert.Since = time.Time{}
		alert.Alerted = time.Time{}
		w.save(view.ID, alert)
		w.deliver(ctx, alert.WebhookID, event)
		return &event
	}

	if alert.Since.IsZero() {
		alert.Since = now
	}
	if now.Sub(alert.Since) < alert.For() {
		w.save(view.ID, alert)
		return nil
	}
	if alert.Firing && now.Sub(alert.Alerted) < w.Repeat {
		w.save(view.ID, alert)
		return nil
	}
	event := describe(view, alert, notify.StateFiring, now)
	alert.Firing = true
	alert.Alerted = now
	w.save(view.ID, alert)
	w.deliver(ctx, alert.WebhookID, event)
	return &event
}

func (w *Watcher) save(viewID int64, alert model.ViewAlert) {
	if err := w.Store.SaveViewAlertState(viewID, alert); err != nil {
		w.Log.Error("view alert state not recorded", slog.Int64("view", viewID), slog.Any("err", err))
	}
}

// describe turns a view and its reading into the event that goes out.
//
// The query travels with it — signal, filters, window, aggregation — because
// the first question anybody asks about a chart alert is "which chart, measuring
// what", and the answer should not require opening the dashboard.
func describe(view model.View, alert model.ViewAlert, state string, now time.Time) notify.Event {
	subject := view.Name
	if alert.Series != "" {
		subject = view.Name + "/" + alert.Series
	}
	where := ""
	if alert.Series != "" {
		where = " (" + alert.Series + ")"
	}
	line := fmt.Sprintf("%s %s", alert.Op, trim(alert.Threshold))
	message := fmt.Sprintf("%s%s is %s, %s", view.Name, where, trim(alert.Value), line)
	if state == notify.StateResolved {
		message = fmt.Sprintf("%s%s is back to %s", view.Name, where, trim(alert.Value))
	}
	return notify.Event{
		At:      now.UTC(),
		Kind:    notify.KindViewRule,
		Subject: subject,
		State:   state,
		Title:   view.Name + " " + line,
		Message: message,
		Fields: map[string]any{
			"category":    model.CategoryView,
			"view_id":     view.ID,
			"view":        view.Name,
			"panel":       view.Panel,
			"series":      alert.Series,
			"value":       alert.Value,
			"op":          alert.Op,
			"threshold":   alert.Threshold,
			"for_seconds": alert.ForSeconds,
			"signal":      view.Query.Signal,
			"agg":         view.Query.Agg,
			"of":          view.Query.Value,
			"group_by":    view.Query.GroupBy,
			"range":       view.Query.Range,
		},
	}
}

func trim(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%.2f", value)
}

func (w *Watcher) deliver(ctx context.Context, webhookID int64, event notify.Event) {
	w.Log.Warn("view alert "+event.State,
		slog.String("subject", event.Subject), slog.String("message", event.Message))
	destination, err := w.Store.DestinationFor(webhookID)
	if err != nil {
		w.Log.Error("view alert has no reachable destination",
			slog.Int64("webhook", webhookID), slog.Any("err", err))
		return
	}
	sendErr := w.Sender.Send(ctx, destination, event)
	failure := ""
	if sendErr != nil {
		failure = sendErr.Error()
		w.Log.Error("view alert not delivered",
			slog.String("destination", destination.Name), slog.Any("err", sendErr))
	}
	if err := w.Store.RecordDelivery(webhookID, time.Now(), failure); err != nil {
		w.Log.Error("delivery not recorded", slog.Any("err", err))
	}
}
