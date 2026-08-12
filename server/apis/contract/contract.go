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
