package cluster

// The monitors: rules about the numbers the cluster page already shows.
//
// Everything on a machine's card is something guard polls anyway — whether the
// health check answered, how fast, the share of the last day it was up, and
// what the box says about its own CPU, memory and disk. A monitor is a rule
// over one of those, and firing one is a POST through internal/notify. No new
// collection, no agent, no second source of truth: if the number is on the
// card, a rule can watch it, and if it is not, the rule cannot lie about it.
//
// Two things make this a monitor rather than a noise generator:
//
//   - A condition has to *hold*. "CPU above 90% for five minutes" is a rule;
//     "CPU above 90%" is a sampling artefact, because one sample at 4am during
//     a log rotation is not an incident.
//   - A rule that fired says so again when it stops. A receiver that gets
//     "firing" and later "resolved" can close its own incident; one that only
//     ever hears about problems learns to ignore them.
//
// A machine that cannot answer a metric is *silent* on it, never zero. A box
// with no SSH login has no CPU figure at all, and a rule reading that as 0%
// would be a rule that never fires — which is the failure you find out about
// during the incident it was supposed to catch.

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// MonitorStore is what the evaluator needs: the rules, the machines they are
// about, somewhere to keep the judgement, and the destinations to deliver to.
type MonitorStore interface {
	ActiveMonitors() ([]model.Monitor, error)
	Nodes() ([]model.Node, error)
	SaveMonitorState(monitorID int64, state model.MonitorState) error
	ClearMonitorState(monitorID, nodeID int64) error
	DestinationFor(id int64) (notify.Destination, error)
	RecordDelivery(id int64, at time.Time, failure string) error
}

// Monitors evaluates the rules on its own timer.
type Monitors struct {
	Store  MonitorStore
	Sender notify.Sender
	// Interval is how often to look. Fast, because it is one read of state the
	// prober has already collected and some arithmetic — the *hold* is what
	// stops a fast loop from being a loud one.
	Interval time.Duration
	// Repeat is how long a firing rule stays quiet before saying it again.
	Repeat time.Duration
	Log    *slog.Logger

	once sync.Once
}

const (
	defaultMonitorInterval = 30 * time.Second
	defaultMonitorRepeat   = 6 * time.Hour
)

func (m *Monitors) prepare() {
	m.once.Do(func() {
		if m.Log == nil {
			m.Log = slog.Default()
		}
		if m.Interval <= 0 {
			m.Interval = defaultMonitorInterval
		}
		if m.Repeat <= 0 {
			m.Repeat = defaultMonitorRepeat
		}
		if m.Sender == nil {
			m.Sender = &notify.Webhook{}
		}
	})
}

// Run evaluates every Interval until the context is cancelled.
func (m *Monitors) Run(ctx context.Context) {
	m.prepare()
	m.Log.Info("cluster monitors started",
		slog.Duration("interval", m.Interval), slog.Duration("repeat", m.Repeat))
	ticker := time.NewTicker(m.Interval)
	defer ticker.Stop()
	for {
		m.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Round evaluates every rule against every machine it covers and returns the
// events it raised — the return is for the tests and for anything that wants
// to ask directly.
func (m *Monitors) Round(ctx context.Context) []notify.Event {
	m.prepare()
	monitors, err := m.Store.ActiveMonitors()
	if err != nil {
		m.Log.Error("monitors could not read their rules", slog.Any("err", err))
		return nil
	}
	if len(monitors) == 0 {
		return nil
	}
	nodes, err := m.Store.Nodes()
	if err != nil {
		m.Log.Error("monitors could not read the cluster", slog.Any("err", err))
		return nil
	}
	now := time.Now()
	var raised []notify.Event
	for _, monitor := range monitors {
		states := map[int64]model.MonitorState{}
		for _, state := range monitor.States {
			states[state.NodeID] = state
		}
		for _, node := range nodes {
			// A rule aimed at one machine ignores the rest; a rule with no
			// machine covers all of them, including the ones added after it
			// was written, which is the point of writing it that way.
			if monitor.NodeID != 0 && monitor.NodeID != node.ID {
				continue
			}
			if event := m.evaluate(ctx, monitor, node, states[node.ID], now); event != nil {
				raised = append(raised, *event)
			}
		}
	}
	return raised
}

// evaluate is one rule against one machine.
func (m *Monitors) evaluate(ctx context.Context, monitor model.Monitor, node model.Node,
	state model.MonitorState, now time.Time) *notify.Event {

	value, measurable := node.Measure(monitor.Metric)
	if !measurable {
		// Nothing to say about a machine that cannot answer. The stored
		// opinion goes with it, so a box whose login was removed while a rule
		// was firing does not sit firing forever.
		if state.Firing || !state.Since.IsZero() {
			if err := m.Store.ClearMonitorState(monitor.ID, node.ID); err != nil {
				m.Log.Error("monitor state not cleared", slog.Int64("monitor", monitor.ID), slog.Any("err", err))
			}
		}
		return nil
	}
	state.NodeID = node.ID
	state.NodeName = node.Name
	state.Value = value
	breached := monitor.Breached(value)

	if !breached {
		if !state.Firing {
			// Was fine, still fine. The value is stored anyway so the page can
			// show what the rule is currently reading.
			state.Since = time.Time{}
			state.Alerted = time.Time{}
			m.save(monitor.ID, state)
			return nil
		}
		// Came back. This is the event that lets a receiver close its own
		// incident rather than infer recovery from a channel going quiet.
		event := m.describe(monitor, node, value, notify.StateResolved, state.Since, now)
		state.Firing = false
		state.Since = time.Time{}
		state.Alerted = time.Time{}
		m.save(monitor.ID, state)
		m.deliver(ctx, monitor, event)
		return &event
	}

	if state.Since.IsZero() {
		state.Since = now
	}
	// Held long enough? A rule with no hold fires on the first sample, which is
	// what somebody who typed zero asked for.
	if now.Sub(state.Since) < monitor.For() {
		m.save(monitor.ID, state)
		return nil
	}
	if state.Firing && (state.Alerted.IsZero() || now.Sub(state.Alerted) < m.Repeat) {
		// Already told them, and not long enough ago to be worth repeating.
		m.save(monitor.ID, state)
		return nil
	}
	event := m.describe(monitor, node, value, notify.StateFiring, state.Since, now)
	state.Firing = true
	state.Alerted = now
	m.save(monitor.ID, state)
	m.deliver(ctx, monitor, event)
	return &event
}

func (m *Monitors) save(monitorID int64, state model.MonitorState) {
	if err := m.Store.SaveMonitorState(monitorID, state); err != nil {
		m.Log.Error("monitor state not recorded", slog.Int64("monitor", monitorID), slog.Any("err", err))
	}
}

// describe turns a rule and a reading into the event that goes out.
//
// Every parameter the page shows travels with it — the machine, its address,
// its status, the reading, the threshold, how long it has been like that — so
// the receiving end can route, filter or draw without asking guard a second
// question.
func (m *Monitors) describe(monitor model.Monitor, node model.Node, value float64,
	state string, since, now time.Time) notify.Event {

	metric, _ := model.Metric(monitor.Metric)
	reading := fmt.Sprintf("%s%s", trim(value), metric.Unit)
	if metric.State {
		reading = node.Status
	}
	held := now.Sub(since).Round(time.Second)
	if since.IsZero() {
		held = 0
	}
	// A state rule reads as a sentence, a threshold rule as a reading against
	// the line it crossed. "Health check failing is down (Health check
	// failing)" is what saying both the same way produces.
	var message string
	switch {
	case metric.State && state == notify.StateResolved:
		message = fmt.Sprintf("%s: health check is answering again (%s)", node.Name, node.URL)
	case metric.State:
		message = fmt.Sprintf("%s: health check is failing (%s)", node.Name, node.URL)
		if node.Error != "" {
			message += " — " + node.Error
		}
	case state == notify.StateResolved:
		message = fmt.Sprintf("%s: %s is back to %s, under %s%s", node.Name, metric.Label, reading,
			trim(monitor.Threshold), metric.Unit)
		if monitor.Op == model.MonitorBelow {
			message = fmt.Sprintf("%s: %s is back to %s, over %s%s", node.Name, metric.Label, reading,
				trim(monitor.Threshold), metric.Unit)
		}
	default:
		message = fmt.Sprintf("%s: %s is %s, %s %s%s for %s", node.Name, metric.Label, reading,
			monitor.Op, trim(monitor.Threshold), metric.Unit, held)
	}
	return notify.Event{
		At:      now.UTC(),
		Kind:    notify.KindClusterRule,
		Subject: fmt.Sprintf("%s/%s", node.Name, monitor.Metric),
		State:   state,
		Title:   monitor.Describe(),
		Message: message,
		Fields: map[string]any{
			"category":     metric.Category,
			"node_id":      node.ID,
			"node":         node.Name,
			"url":          node.URL,
			"status":       node.Status,
			"metric":       monitor.Metric,
			"value":        value,
			"unit":         metric.Unit,
			"op":           monitor.Op,
			"threshold":    monitor.Threshold,
			"for_seconds":  monitor.ForSeconds,
			"held_seconds": held.Seconds(),
			"monitor_id":   monitor.ID,
			// The rest of the card, because an alert that says "CPU 94%" and
			// makes somebody open the dashboard to learn the machine is also
			// out of disk has cost a trip it did not need to.
			"latency_ms":     node.LatencyMS,
			"uptime_percent": node.Uptime,
			"checks":         node.Checks,
			"cpu_percent":    measurement(node, "cpu_percent"),
			"mem_percent":    measurement(node, "mem_percent"),
			"disk_percent":   measurement(node, "disk_percent"),
		},
	}
}

// measurement is a reading or nil — nil rather than zero, because a machine
// with no login has no CPU figure and "0" would read as an idle box.
func measurement(node model.Node, metric string) any {
	if value, ok := node.Measure(metric); ok {
		return value
	}
	return nil
}

func trim(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%.1f", value)
}

// deliver sends one event and records how the destination answered.
//
// A failure is logged and left unrecorded as "told them", so the next pass
// tries again: an alert swallowed by a 401 would otherwise be an outage nobody
// hears about twice. The log line happens either way — it is the floor, and
// the only record on an instance with no destination reachable.
func (m *Monitors) deliver(ctx context.Context, monitor model.Monitor, event notify.Event) {
	m.Log.Warn("cluster monitor "+event.State,
		slog.String("subject", event.Subject), slog.String("rule", event.Title),
		slog.String("message", event.Message))
	destination, err := m.Store.DestinationFor(monitor.WebhookID)
	if err != nil {
		m.Log.Error("monitor has no reachable destination",
			slog.Int64("monitor", monitor.ID), slog.Any("err", err))
		return
	}
	sendErr := m.Sender.Send(ctx, destination, event)
	failure := ""
	if sendErr != nil {
		failure = sendErr.Error()
		m.Log.Error("monitor event not delivered",
			slog.String("destination", destination.Name), slog.Any("err", sendErr))
	}
	if err := m.Store.RecordDelivery(monitor.WebhookID, time.Now(), failure); err != nil {
		m.Log.Error("delivery not recorded", slog.Any("err", err))
	}
}
