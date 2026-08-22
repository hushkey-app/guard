// Package contract holds the request and response shapes that are not already
// domain types.
//
// They live here rather than beside their handlers for the same reason the data
// types live in internal/telemetry/model: the generated client imports the
// package a type is declared in, and it has to compile for GOOS=js/wasm so a
// page can call the API. An endpoint package cannot — it calls api.Define,
// which only exists in the server build.
//
// The rule in one line: handlers in *.api.go, shapes in a package with no
// api.Define in it.
package contract

import (
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/api"
)

// Health is what a load balancer reads.
type Health struct {
	Status string `json:"status"`
	Events int    `json:"events"`
}

// LogInput is a log line from something that does not speak OTLP — a shell
// script, a cron job, a language with no exporter.
type LogInput struct {
	Timestamp  time.Time      `json:"timestamp"`
	Service    string         `json:"service"`
	Instance   string         `json:"instance"`
	Severity   string         `json:"severity"`
	Message    string         `json:"message"`
	TraceID    string         `json:"trace_id"`
	SpanID     string         `json:"span_id"`
	Attributes map[string]any `json:"attributes"`
}

// Validate runs before the handler — and, because it lives on the shared side,
// in the browser too: the client rejects a log line with no message without
// spending a round-trip on an answer it could already predict.
func (i LogInput) Validate() error {
	if strings.TrimSpace(i.Message) == "" {
		return api.Invalid("message", "is required")
	}
	return nil
}

// Accepted answers 202: the event is queued for the writer, not yet on disk.
type Accepted struct{}

func (Accepted) Status() int { return 202 }

// SeriesQuery is the chart's request. It is not a model.Filter because a series
// asks two things a list does not: which metric, and what to break it down by.
type SeriesQuery struct {
	Name    string    `query:"name"`
	GroupBy string    `query:"group_by"`
	Service string    `query:"service"`
	From    time.Time `query:"from"`
	To      time.Time `query:"to"`
	Limit   int       `query:"limit"`
}

// Purged reports what the retention sweep removed.
type Purged struct {
	Removed int64 `json:"removed"`
}

// PreviewRequest runs a query that has not been saved — what the view builder
// posts on every change, so the panel on screen is the panel you would get.
type PreviewRequest struct {
	Panel string          `json:"panel"`
	Query model.ViewQuery `json:"query"`
}

func (p PreviewRequest) Validate() error { return p.Query.ValidateFor(p.Panel) }

// ViewDataQuery runs a saved view. Range, From and To override the window the
// view was saved with, which is what lets one time picker drive every panel on
// the dashboard without rewriting sixteen stored queries.
type ViewDataQuery struct {
	ID    int64     `query:"id"`
	Range string    `query:"range"`
	From  time.Time `query:"from"`
	To    time.Time `query:"to"`
}

func (q ViewDataQuery) Validate() error {
	if q.ID <= 0 {
		return api.Invalid("id", "is required")
	}
	if q.Range != "" && q.Range != "all" {
		if _, err := model.ParseDuration(q.Range); err != nil {
			return api.Invalid("range", err.Error())
		}
	}
	return nil
}

// Apply layers the caller's window over the stored one. An empty override
// leaves the view's own window alone: a panel saved as "last 7 days" next to
// fifteen hourly ones is a deliberate thing to build.
func (q ViewDataQuery) Apply(stored model.ViewQuery) model.ViewQuery {
	if q.Range == "" && q.From.IsZero() && q.To.IsZero() {
		return stored
	}
	stored.Range, stored.From, stored.To = q.Range, q.From, q.To
	return stored
}

// Assignment states which machine an instance runs on, when its telemetry
// cannot. NodeID zero releases it back to the automatic match.
type Assignment struct {
	Service  string `json:"service"`
	Instance string `json:"instance"`
	NodeID   int64  `json:"node_id"`
}

func (a Assignment) Validate() error {
	if a.Service == "" {
		return api.Invalid("service", "is required")
	}
	return nil
}

// ViewOrder is the dashboard's layout: the panel ids, in the order they should
// appear. Views the caller does not mention keep their relative order and
// follow, so a panel added in another tab mid-drag is not quietly dropped.
type ViewOrder struct {
	IDs []int64 `json:"ids"`
}

func (o ViewOrder) Validate() error {
	if len(o.IDs) == 0 {
		return api.Invalid("ids", "is required")
	}
	return nil
}

// DrillRequest asks for the events behind one mark on a panel: the query that
// drew it, and which mark was clicked.
//
// The query travels with the request rather than a view id, because the mark
// you clicked may be on a preview that has never been saved — and because a
// saved view whose window was overridden by the dashboard picker is no longer
// the query stored under its id.
type DrillRequest struct {
	Panel     string          `json:"panel"`
	Query     model.ViewQuery `json:"query"`
	Selection model.Selection `json:"selection"`
}

func (d DrillRequest) Validate() error { return d.Query.ValidateFor(d.Panel) }

// Catalogue is everything the builder needs to offer a choice: the panels this
// binary can render, the aggregations the compiler implements, and the fields
// this instance has actually seen. One request, because all three change at
// once — a panel added without its fields is a builder that offers a control
// with nothing to put in it.
type Catalogue struct {
	Panels       []model.PanelSpec `json:"panels"`
	Aggregations []string          `json:"aggregations"`
	Fields       model.Fields      `json:"fields"`
}

// ActionList is one machine's commands, saved together.
//
// The whole list rather than one action at a time: the settings page edits them
// as a list, and a per-action endpoint would need a delete, a reorder, and an
// answer to what happens when two tabs disagree about the order.
type ActionList struct {
	NodeID  int64              `json:"node_id"`
	Actions []model.NodeAction `json:"actions"`
}

func (l ActionList) Validate() error {
	if l.NodeID <= 0 {
		return api.Invalid("node_id", "is required")
	}
	for _, action := range l.Actions {
		if err := action.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// RunRequest asks for one stored action to be run on the machine it belongs to.
//
// An action id, never a command. The two are the same power — anyone who can
// run this can save an action first — but they are not the same audit trail: a
// command that ran on somebody's server is a thing that should exist in the
// database before it exists on the machine.
type RunRequest struct {
	ActionID int64 `json:"action_id"`
}

func (r RunRequest) Validate() error {
	if r.ActionID <= 0 {
		return api.Invalid("action_id", "is required")
	}
	return nil
}

// NodeRequest names one machine and nothing else — what the endpoints that act
// on a whole machine take: the connection test, and the duplicate button.
type NodeRequest struct {
	NodeID int64 `json:"node_id"`
}

func (r NodeRequest) Validate() error {
	if r.NodeID <= 0 {
		return api.Invalid("node_id", "is required")
	}
	return nil
}

// BackupRequest is what an export is asked with.
//
// Empty is a real answer rather than a missing one: a backup with no passphrase
// is the configuration with the credentials cut out, which is a file somebody
// can keep anywhere. So there is nothing to validate here.
type BackupRequest struct {
	Passphrase string `json:"passphrase"`
}

// RestoreRequest is a backup file and the words that open it.
type RestoreRequest struct {
	Passphrase string       `json:"passphrase"`
	Backup     model.Backup `json:"backup"`
}

// AnalyticsQuery is the window the strip and the grid are read over.
//
// Bounded at both ends, unlike ViewDataQuery: the rollup is keyed by whole UTC
// days and the strip's second half is the window of equal length before this
// one, so "all retained" — a real answer everywhere else in the dashboard —
// has no previous to compare against and is refused here rather than answered
// with a comparison against nothing.
type AnalyticsQuery struct {
	Range string    `query:"range"`
	From  time.Time `query:"from"`
	To    time.Time `query:"to"`
}

// The window a request that names none is read over. Seven days because the
// rollup's grain is a day and one of them is a sample rather than a trend;
// there is no knob, for the reason there is no knob anywhere else in guard.
const defaultAnalyticsWindow = 7 * 24 * time.Hour

func (q AnalyticsQuery) Validate() error {
	if q.Range == "all" {
		return api.Invalid("range", "analytics is read over a bounded window, so it has no all")
	}
	if q.Range != "" {
		d, err := model.ParseDuration(q.Range)
		if err != nil {
			return api.Invalid("range", err.Error())
		}
		if d <= 0 {
			return api.Invalid("range", "is not a window")
		}
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.To.Before(q.From) {
		return api.Invalid("to", "is before from")
	}
	return nil
}

// Window resolves the request into the two instants the rollup is asked for. A
// custom From wins over a range, and an absent To means now — which is what
// makes "from the first of the month" a window somebody can type.
func (q AnalyticsQuery) Window(now time.Time) (from, to time.Time) {
	from, to = q.From, q.To
	if to.IsZero() {
		to = now
	}
	if !from.IsZero() {
		return from, to
	}
	d, err := model.ParseDuration(q.Range)
	if err != nil || d <= 0 {
		d = defaultAnalyticsWindow
	}
	return to.Add(-d), to
}

// Analytics is the top of the page in one answer: the strip, and the grid under
// it.
//
// One request rather than two for the reason Catalogue is one — the strip's
// totals and the grid's rows are the same window, and two calls a second apart
// are two windows, so the numbers somebody is reading side by side would not
// add up.
type Analytics struct {
	Summary model.AnalyticsSummary `json:"summary"`
	Paths   []model.PathRow        `json:"paths"`
}
