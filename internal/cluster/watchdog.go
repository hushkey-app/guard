package cluster

// The staleness watch: the loop that notices a job stopped working.
//
// It is a separate loop from the scheduler, and that separation is the whole
// point of it. A check that runs as part of the dump only ever fires on a day
// the dump ran — so the one failure it exists to catch, "nothing has run at
// all", is exactly the one it cannot see. This reads the database, not the
// scheduler: if the scheduler is wedged, dead, or was never started, the watch
// still finds an action whose last success is older than its budget and says
// so.
//
// It delivers through internal/notify, the same module the machine monitors
// use, for the same reason: an alert about SSH jobs failing must not travel
// over the machinery that runs SSH jobs, and one delivery path is one place to
// fix a token format.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// WatchStore is what the watch needs: the actions somebody set a threshold on,
// the machines they belong to, one flag per action so a stale job is reported
// and then repeated slowly, and the destination a given job's alerts go to.
type WatchStore interface {
	WatchedActions() ([]model.NodeAction, error)
	Node(id int64) (model.Node, error)
	MarkAlerted(actionID int64, at time.Time) error
	DestinationFor(id int64) (notify.Destination, error)
	RecordDelivery(id int64, at time.Time, failure string) error
}

// Watchdog checks the staleness budgets on its own timer.
type Watchdog struct {
	Store  WatchStore
	Sender notify.Sender
	// Interval is how often to look. Cheap — it is one query and some
	// subtraction — so it can be far finer than the budgets it enforces.
	Interval time.Duration
	// Repeat is how long a stale job stays quiet after it has been reported.
	// Without it a job that broke on Friday alerts every pass all weekend,
	// which is how a channel of alerts becomes a channel nobody reads.
	Repeat time.Duration
	Log    *slog.Logger

	once sync.Once
}

const (
	defaultWatchInterval = 5 * time.Minute
	defaultWatchRepeat   = 6 * time.Hour
)

func (w *Watchdog) prepare() {
	w.once.Do(func() {
		if w.Log == nil {
			w.Log = slog.Default()
		}
		if w.Interval <= 0 {
			w.Interval = defaultWatchInterval
		}
		if w.Repeat <= 0 {
			w.Repeat = defaultWatchRepeat
		}
		if w.Sender == nil {
			w.Sender = &notify.Webhook{}
		}
	})
}

// Run checks every Interval until the context is cancelled. The first pass is
// immediate: a guard that has just started should say what is already broken,
// not fifteen minutes from now.
func (w *Watchdog) Run(ctx context.Context) {
	w.prepare()
	w.Log.Info("staleness watch started",
		slog.Duration("interval", w.Interval), slog.Duration("repeat", w.Repeat))
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

// Round reports every action that is over its budget and has not been reported
// recently, and returns them — the return is for the tests and for a page that
// wants to ask directly.
func (w *Watchdog) Round(ctx context.Context) []notify.Event {
	w.prepare()
	actions, err := w.Store.WatchedActions()
	if err != nil {
		w.Log.Error("staleness watch could not read its actions", slog.Any("err", err))
		return nil
	}
	now := time.Now()
	var raised []notify.Event
	for _, action := range actions {
		stale, since := action.Stale(now)
		if !stale {
			continue
		}
		// Reported already, and not long enough ago to be worth repeating.
		if !action.AlertedAt.IsZero() && now.Sub(action.AlertedAt) < w.Repeat {
			continue
		}
		event := w.describe(action, since, now)
		// Logged first and always. The destination is the part that can be
		// unconfigured, misconfigured or down — the log is the record that this
		// was noticed, and it is the same log every other thing guard does at
		// 4am lands in.
		w.Log.Error("a scheduled job has not succeeded in too long",
			slog.String("subject", event.Subject),
			slog.Any("stale_for", event.Fields["stale_for"]),
			slog.Any("threshold", event.Fields["threshold"]),
			slog.Any("last_ok", event.Fields["last_ok_at"]))
		if !w.deliver(ctx, action.WebhookID, event) {
			// Not delivered: the flag stays unset, so the next pass tries
			// again rather than an outage being swallowed by a 401.
			raised = append(raised, event)
			continue
		}
		if err := w.Store.MarkAlerted(action.ID, now); err != nil {
			w.Log.Error("staleness alert not recorded", slog.Int64("action", action.ID), slog.Any("err", err))
		}
		raised = append(raised, event)
	}
	return raised
}

// deliver sends one event to the destination the action names. Reports whether
// the alert can be considered told.
//
// A command that names no destination is logged and nothing else — and that
// counts as told, because otherwise every pass would re-log it. There is no
// instance-wide fallback: destinations are named things somebody added on
// /settings/alerts, and a second, invisible one configured by an environment
// variable was a second answer to "where do alerts go".
func (w *Watchdog) deliver(ctx context.Context, webhookID int64, event notify.Event) bool {
	if webhookID <= 0 {
		return true
	}
	destination, err := w.Store.DestinationFor(webhookID)
	if err != nil {
		w.Log.Error("staleness alert has no reachable destination",
			slog.Int64("webhook", webhookID), slog.Any("err", err))
		return false
	}
	if destination.URL == "" {
		return true
	}
	err = w.Sender.Send(ctx, destination, event)
	failure := ""
	if err != nil {
		failure = err.Error()
	}
	if recordErr := w.Store.RecordDelivery(webhookID, time.Now(), failure); recordErr != nil {
		w.Log.Error("delivery not recorded", slog.Any("err", recordErr))
	}
	if err != nil {
		w.Log.Error("staleness alert not delivered",
			slog.String("destination", destination.Name), slog.Any("err", err))
		return false
	}
	return true
}

func (w *Watchdog) describe(action model.NodeAction, since, now time.Time) notify.Event {
	name := fmt.Sprintf("machine %d", action.NodeID)
	if node, err := w.Store.Node(action.NodeID); err == nil {
		name = node.Name
	}
	staleFor := now.Sub(since).Round(time.Minute)
	fields := map[string]any{
		"category":    model.CategoryJob,
		"node_id":     action.NodeID,
		"node":        name,
		"action_id":   action.ID,
		"action":      action.Name,
		"schedule":    action.Schedule,
		"stale_for":   staleFor.String(),
		"threshold":   action.StaleAfter().String(),
		"last_ok_at":  nil,
		"last_run_at": nil,
	}
	if !action.LastRunAt.IsZero() {
		fields["last_run_at"] = action.LastRunAt.UTC().Format(time.RFC3339)
	}
	message := fmt.Sprintf("%s on %s has never succeeded in the %s since it was added, past its %s threshold",
		action.Name, name, staleFor, action.StaleAfter())
	if !action.LastOKAt.IsZero() {
		last := action.LastOKAt.UTC()
		fields["last_ok_at"] = last.Format(time.RFC3339)
		message = fmt.Sprintf("%s on %s last succeeded %s ago (%s), past its %s threshold",
			action.Name, name, staleFor, last.Format(time.RFC3339), action.StaleAfter())
	}
	return notify.Event{
		At:      now.UTC(),
		Kind:    notify.KindScheduleStale,
		Subject: fmt.Sprintf("%s/%s", name, action.Name),
		State:   notify.StateFiring,
		Title:   "No successful run in " + action.StaleAfter().String(),
		Message: message,
		Fields:  fields,
	}
}
