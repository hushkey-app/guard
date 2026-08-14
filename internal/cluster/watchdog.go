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
// It reaches out through its own HTTP client for the same reason. The alert
// about SSH jobs failing should not travel over the thing that runs SSH jobs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// WatchStore is what the watch needs: the actions somebody set a threshold on,
// the machines they belong to, and one flag per action so a stale job is
// reported and then repeated slowly rather than every pass.
type WatchStore interface {
	WatchedActions() ([]model.NodeAction, error)
	Node(id int64) (model.Node, error)
	MarkAlerted(actionID int64, at time.Time) error
}

// An Alert is one job that has not succeeded for too long.
//
// It carries when it last worked rather than only that it is late, because
// "no successful dump since 02:00 yesterday" is a sentence somebody can act on
// and "backup stale" is one they learn to skim.
type Alert struct {
	At        time.Time `json:"at"`
	NodeID    int64     `json:"node_id"`
	Node      string    `json:"node"`
	ActionID  int64     `json:"action_id"`
	Action    string    `json:"action"`
	Schedule  string    `json:"schedule,omitempty"`
	LastOKAt  time.Time `json:"last_ok_at,omitempty"`
	StaleFor  string    `json:"stale_for"`
	Threshold string    `json:"threshold"`
	Message   string    `json:"message"`
}

// Notifier is where an alert goes. One method, so the delivery path is a thing
// that can be swapped for a test, a webhook, or nothing at all — an instance
// with no notifier still logs, and the log is the floor rather than the
// feature.
type Notifier interface {
	Notify(ctx context.Context, alert Alert) error
}

// Watchdog checks the staleness budgets on its own timer.
type Watchdog struct {
	Store    WatchStore
	Notifier Notifier
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
func (w *Watchdog) Round(ctx context.Context) []Alert {
	w.prepare()
	actions, err := w.Store.WatchedActions()
	if err != nil {
		w.Log.Error("staleness watch could not read its actions", slog.Any("err", err))
		return nil
	}
	now := time.Now()
	var raised []Alert
	for _, action := range actions {
		stale, since := action.Stale(now)
		if !stale {
			continue
		}
		// Reported already, and not long enough ago to be worth repeating.
		if !action.AlertedAt.IsZero() && now.Sub(action.AlertedAt) < w.Repeat {
			continue
		}
		alert := w.describe(action, since, now)
		// Logged first and always. The notifier is the part that can be
		// unconfigured, misconfigured, or down — the log is the record that
		// this was noticed, and it is the same log every other thing guard
		// does at 4am lands in.
		w.Log.Error("a scheduled job has not succeeded in too long",
			slog.String("machine", alert.Node), slog.String("action", alert.Action),
			slog.String("stale_for", alert.StaleFor), slog.String("threshold", alert.Threshold),
			slog.Time("last_ok", alert.LastOKAt))
		if w.Notifier != nil {
			if err := w.Notifier.Notify(ctx, alert); err != nil {
				// The alert is not lost by a delivery that failed: the flag is
				// only set below, so the next pass tries again.
				w.Log.Error("staleness alert not delivered",
					slog.Int64("action", action.ID), slog.Any("err", err))
				raised = append(raised, alert)
				continue
			}
		}
		if err := w.Store.MarkAlerted(action.ID, now); err != nil {
			w.Log.Error("staleness alert not recorded", slog.Int64("action", action.ID), slog.Any("err", err))
		}
		raised = append(raised, alert)
	}
	return raised
}

func (w *Watchdog) describe(action model.NodeAction, since, now time.Time) Alert {
	name := fmt.Sprintf("machine %d", action.NodeID)
	if node, err := w.Store.Node(action.NodeID); err == nil {
		name = node.Name
	}
	staleFor := now.Sub(since).Round(time.Minute)
	alert := Alert{
		At:        now.UTC(),
		NodeID:    action.NodeID,
		Node:      name,
		ActionID:  action.ID,
		Action:    action.Name,
		Schedule:  action.Schedule,
		StaleFor:  staleFor.String(),
		Threshold: action.StaleAfter().String(),
	}
	if !action.LastOKAt.IsZero() {
		alert.LastOKAt = action.LastOKAt
		alert.Message = fmt.Sprintf("%s on %s last succeeded %s ago (%s), past its %s threshold",
			action.Name, name, staleFor, action.LastOKAt.Format(time.RFC3339), alert.Threshold)
		return alert
	}
	// Never succeeded, which is a different and worse sentence than "late".
	alert.Message = fmt.Sprintf("%s on %s has never succeeded, %s past its %s threshold",
		action.Name, name, staleFor-action.StaleAfter(), alert.Threshold)
	return alert
}

// Webhook posts an alert as JSON to one URL — Slack's incoming webhooks, an
// internal endpoint, whatever is already wired to wake somebody up.
//
// Its own client, deliberately not guard's prober or its SSH runner: an alert
// that a machine's jobs are failing should not be delivered by the machinery
// that runs the jobs.
type Webhook struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration
}

func (h *Webhook) Notify(ctx context.Context, alert Alert) error {
	if h.URL == "" {
		return nil
	}
	client := h.Client
	if client == nil {
		timeout := h.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	body, err := json.Marshal(struct {
		Alert
		// text is what a Slack incoming webhook renders. Included beside the
		// fields so one URL works for both a chat hook and something that
		// parses the payload properly.
		Text string `json:"text"`
	}{Alert: alert, Text: alert.Message})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("the alert webhook answered %s", response.Status)
	}
	return nil
}
