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

	"github.com/mirairoad/guard/internal/telemetry/model"
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
