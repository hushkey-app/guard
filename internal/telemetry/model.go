package telemetry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type Event struct {
	ID         uint64         `json:"id"`
	Signal     string         `json:"signal"`
	Timestamp  time.Time      `json:"timestamp"`
	ReceivedAt time.Time      `json:"received_at"`
	Service    string         `json:"service"`
	Instance   string         `json:"instance,omitempty"`
	Scope      string         `json:"scope,omitempty"`
	Name       string         `json:"name,omitempty"`
	Severity   string         `json:"severity,omitempty"`
	Message    string         `json:"message,omitempty"`
	TraceID    string         `json:"trace_id,omitempty"`
	SpanID     string         `json:"span_id,omitempty"`
	DurationMS float64        `json:"duration_ms,omitempty"`
	Value      *float64       `json:"value,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Filter struct {
	Signal  string
	Service string
	Query   string
	Limit   int
}

type Instance struct {
	Service  string    `json:"service"`
	Instance string    `json:"instance"`
	LastSeen time.Time `json:"last_seen"`
	Logs     int       `json:"logs"`
	Errors   int       `json:"errors"`
	Spans    int       `json:"spans"`
	Metrics  int       `json:"metrics"`
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

type Store struct {
	mu        sync.RWMutex
	capacity  int
	startedAt time.Time
	nextID    uint64
	received  uint64
	events    []Event
	start     int // oldest event; advances on overwrite once the ring is full
}

func NewStore(capacity int) *Store {
	if capacity < 1 {
		capacity = 10_000
	}
	return &Store{capacity: capacity, startedAt: time.Now(), events: make([]Event, 0, capacity)}
}

func (s *Store) Add(events ...Event) {
	if len(events) == 0 {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range events {
		e := events[i]
		s.nextID++
		s.received++
		e.ID = s.nextID
		e.ReceivedAt = now
		if e.Timestamp.IsZero() {
			e.Timestamp = now
		}
		if e.Service == "" {
			e.Service = "unknown-service"
		}
		if len(s.events) < s.capacity {
			s.events = append(s.events, e)
			continue
		}
		s.events[s.start] = e
		s.start = (s.start + 1) % s.capacity
	}
}

func (s *Store) Query(f Filter) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return query(s.orderedLocked(), f)
}

func (s *Store) orderedLocked() []Event {
	out := make([]Event, len(s.events))
	for i := range s.events {
		out[i] = s.events[(s.start+i)%len(s.events)]
	}
	return out
}

func query(events []Event, f Filter) []Event {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	needle := strings.ToLower(strings.TrimSpace(f.Query))
	out := make([]Event, 0, min(f.Limit, len(events)))
	for i := len(events) - 1; i >= 0 && len(out) < f.Limit; i-- {
		e := events[i]
		if f.Signal != "" && e.Signal != f.Signal {
			continue
		}
		if f.Service != "" && e.Service != f.Service {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(searchText(e)), needle) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func searchText(e Event) string {
	return fmt.Sprintf("%s %s %s %s %s %s %v", e.Service, e.Instance, e.Name, e.Severity, e.Message, e.TraceID, e.Attributes)
}

func (s *Store) Snapshot() Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ordered := s.orderedLocked()
	summary := Summary{
		StartedAt: s.startedAt,
		Capacity:  s.capacity,
		Stored:    len(s.events),
		Received:  s.received,
		Recent:    query(ordered, Filter{Limit: 12}),
	}
	instances := map[string]*Instance{}
	for _, e := range ordered {
		key := e.Service + "\x00" + e.Instance
		inst := instances[key]
		if inst == nil {
			inst = &Instance{Service: e.Service, Instance: e.Instance}
			instances[key] = inst
		}
		if e.ReceivedAt.After(inst.LastSeen) {
			inst.LastSeen = e.ReceivedAt
		}
		switch e.Signal {
		case "logs":
			summary.Logs++
			inst.Logs++
			if isError(e.Severity) {
				summary.Errors++
				inst.Errors++
			}
		case "traces":
			summary.Spans++
			inst.Spans++
			if isError(e.Severity) {
				summary.Errors++
				inst.Errors++
			}
		case "metrics":
			summary.Metrics++
			inst.Metrics++
		}
	}
	for _, inst := range instances {
		summary.Instances = append(summary.Instances, *inst)
	}
	sort.Slice(summary.Instances, func(i, j int) bool {
		return summary.Instances[i].LastSeen.After(summary.Instances[j].LastSeen)
	})
	return summary
}

func isError(severity string) bool {
	severity = strings.ToUpper(severity)
	return strings.Contains(severity, "ERROR") || strings.Contains(severity, "FATAL")
}

type snapshotKey struct{}

func WithSnapshot(ctx context.Context, value Summary) context.Context {
	return context.WithValue(ctx, snapshotKey{}, value)
}

func SnapshotFrom(ctx context.Context) Summary {
	value, _ := ctx.Value(snapshotKey{}).(Summary)
	return value
}
