// Package model is guard's data, and nothing else.
//
// It exists as its own package for one reason: the generated API client imports
// the types an endpoint declares, and that client has to compile for
// GOOS=js/wasm so a page can call the API with the same types the handler
// validates. The parent package opens SQLite, so importing it from a browser
// build fails at the linker with "build constraints exclude all Go files in
// modernc.org/libc/pthread" — a message about pthreads, from a page that only
// wanted an Event.
//
// So: types here, storage above. The parent aliases every name, so the rest of
// guard is unaffected.
package model

import (
	"errors"
	"time"
)

type Event struct {
	ID           uint64         `json:"id"`
	Signal       string         `json:"signal"`
	Timestamp    time.Time      `json:"timestamp"`
	ReceivedAt   time.Time      `json:"received_at"`
	Service      string         `json:"service"`
	Instance     string         `json:"instance,omitempty"`
	Scope        string         `json:"scope,omitempty"`
	Name         string         `json:"name,omitempty"`
	Severity     string         `json:"severity,omitempty"`
	Message      string         `json:"message,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	SpanID       string         `json:"span_id,omitempty"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Kind         string         `json:"kind,omitempty"`
	DurationMS   float64        `json:"duration_ms,omitempty"`
	Value        *float64       `json:"value,omitempty"`
	Unit         string         `json:"unit,omitempty"`
	MetricType   string         `json:"metric_type,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

// Filter is both the store's query and the HTTP query contract: the `query`
// tags are what core/api decodes a request into, so there is one definition of
// "how you ask guard for events" rather than a struct here and a set of
// r.URL.Query().Get calls somewhere else that drift apart.
type Filter struct {
	Signal   string `query:"signal"`
	Service  string `query:"service"`
	Severity string `query:"severity"`
	Name     string `query:"name"`
	Query    string `query:"q"`
	// Nodes narrows to the machines these cluster nodes cover — comma-separated
	// ids, because more than one is the useful case: "everything on the two
	// web boxes" is a question, and "everything on exactly one" usually is not.
	//
	// A list of ids rather than a list of services, so that the filter follows
	// the cluster as its membership changes. Pin a new service to a machine and
	// a saved "node 3" filter includes it without being edited.
	Nodes string `query:"nodes"`
	// RUMPath narrows to the browser sessions that saw one analytics path —
	// the walk from a row on /analytics into the spans those visits produced,
	// which is the whole reason the session id is a join key.
	//
	// A path rather than the session ids themselves: the ids belong to the
	// database, a link carrying two hundred of them is one nobody can share or
	// read, and the set they name changes as the window moves.
	RUMPath string    `query:"rum_path"`
	From    time.Time `query:"from"`
	To      time.Time `query:"to"`
	Limit   int       `query:"limit"`
	Offset  int       `query:"offset"`
}

type Instance struct {
	Service  string    `json:"service"`
	Instance string    `json:"instance"`
	LastSeen time.Time `json:"last_seen"`
	Logs     int       `json:"logs"`
	Errors   int       `json:"errors"`
	Spans    int       `json:"spans"`
	Metrics  int       `json:"metrics"`
	// NodeID and Placement are filled in only by ClusterTopology: which machine
	// this instance was filed under, and whether that was worked out from its
	// telemetry ("host") or stated by a person ("assigned"). Empty everywhere
	// else, because everywhere else the question has not been asked.
	NodeID    int64  `json:"node_id,omitempty"`
	Placement string `json:"placement,omitempty"`
}

type Summary struct {
	StartedAt time.Time  `json:"started_at"`
	Capacity  int        `json:"capacity"`
	Stored    int        `json:"stored"`
	Received  uint64     `json:"received"`
	Logs      int        `json:"logs"`
	Errors    int        `json:"errors"`
	Spans     int        `json:"spans"`
	Metrics   int        `json:"metrics"`
	Instances []Instance `json:"instances"`
	Recent    []Event    `json:"recent"`
}

// The analytics windows are days rather than hours, and there are two of them,
// because the tables they sweep are asked different questions. The rollup
// answers "versus last month", so it is kept in months; `analytics_seen` only
// makes a day's session count exact while that day is still being written to,
// and it is the one analytics table that grows with traffic rather than with
// content — so it goes first, and after it has gone the counts stand and cannot
// be recomputed. The ceiling is ten years, which is a refusal rather than a
// recommendation: a number nobody typed on purpose.
const (
	DefaultAnalyticsRollupDays = 90
	DefaultAnalyticsSeenDays   = 7
	MaxAnalyticsDays           = 3650
)

type Settings struct {
	DatabasePath        string `json:"database_path"`
	RetentionHours      int    `json:"retention_hours"`
	MaxEvents           int    `json:"max_events"`
	AnalyticsRollupDays int    `json:"analytics_rollup_days"`
	AnalyticsSeenDays   int    `json:"analytics_seen_days"`
}

type Facets struct {
	Services    []string `json:"services"`
	Severities  []string `json:"severities"`
	MetricNames []string `json:"metric_names"`
}

type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type MetricSeries struct {
	Key    string        `json:"key"`
	Unit   string        `json:"unit,omitempty"`
	Type   string        `json:"type,omitempty"`
	Points []MetricPoint `json:"points"`
}

// Validate is core/api's hook: it runs after the query string is decoded and
// before the handler, and a plain error here becomes a 400. Clamping a negative
// limit silently would answer 200 with a page the caller did not ask for, which
// is harder to debug than being told.
func (f Filter) Validate() error {
	if f.Limit < 0 {
		return errors.New("limit must not be negative")
	}
	if f.Offset < 0 {
		return errors.New("offset must not be negative")
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return errors.New("to must not be before from")
	}
	return nil
}
