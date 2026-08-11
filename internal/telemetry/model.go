package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
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

type Filter struct {
	Signal   string
	Service  string
	Severity string
	Name     string
	Query    string
	From     time.Time
	To       time.Time
	Limit    int
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

type Settings struct {
	DatabasePath   string `json:"database_path"`
	RetentionHours int    `json:"retention_hours"`
	MaxEvents      int    `json:"max_events"`
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

type Store struct {
	db         *sql.DB
	path       string
	writeCh    chan writeRequest
	stopWriter chan struct{}
	writerDone chan struct{}
	writeDelay time.Duration
	lifecycle  sync.RWMutex
	closing    bool
	lastPurge  atomic.Int64
}

type writeRequest struct {
	events []Event
	done   chan error
}

var memoryStoreID atomic.Uint64

func NewStore(capacity int) *Store {
	if capacity < 1 {
		capacity = 10_000
	}
	name := "guard-memory-" + strconv.FormatUint(memoryStoreID.Add(1), 10)
	store, err := open("file:"+name+"?mode=memory&cache=shared", "memory", Settings{RetentionHours: 24, MaxEvents: capacity}, true)
	if err != nil {
		panic(err)
	}
	return store
}

func Open(path string, defaults Settings) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = "guard.db"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("database directory: %w", err)
	}
	u := &url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return open(u.String(), abs, defaults, false)
}

func open(dsn, displayPath string, defaults Settings, memory bool) (*Store, error) {
	if defaults.RetentionHours < 1 {
		defaults.RetentionHours = 24
	}
	if defaults.MaxEvents < 1 {
		defaults.MaxEvents = 1_000_000
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if memory {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
	}
	writeDelay := time.Duration(0)
	if !memory {
		writeDelay = 5 * time.Millisecond
	}
	store := &Store{db: db, path: displayPath, writeCh: make(chan writeRequest, 256), stopWriter: make(chan struct{}), writerDone: make(chan struct{}), writeDelay: writeDelay}
	if err := store.migrate(defaults); err != nil {
		db.Close()
		return nil, err
	}
	go store.writeLoop()
	return store, nil
}

func (s *Store) migrate(defaults Settings) error {
	const schema = `
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  signal TEXT NOT NULL,
  timestamp_ns INTEGER NOT NULL,
  received_at_ns INTEGER NOT NULL,
  service TEXT NOT NULL,
  instance TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  trace_id TEXT NOT NULL DEFAULT '',
  span_id TEXT NOT NULL DEFAULT '',
  parent_span_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  duration_ms REAL NOT NULL DEFAULT 0,
  metric_value REAL,
  unit TEXT NOT NULL DEFAULT '',
  metric_type TEXT NOT NULL DEFAULT '',
  attributes_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_events_signal_time ON events(signal, timestamp_ns DESC);
CREATE INDEX IF NOT EXISTS idx_events_service_time ON events(service, timestamp_ns DESC);
CREATE INDEX IF NOT EXISTS idx_events_trace ON events(trace_id);
CREATE INDEX IF NOT EXISTS idx_events_metric_time ON events(name, timestamp_ns DESC) WHERE signal = 'metrics';
CREATE TABLE IF NOT EXISTS settings (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  retention_hours INTEGER NOT NULL,
  max_events INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS metadata (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  started_at_ns INTEGER NOT NULL,
  total_received INTEGER NOT NULL DEFAULT 0
);`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO settings(id, retention_hours, max_events) VALUES(1, ?, ?)`, defaults.RetentionHours, defaults.MaxEvents); err != nil {
		return fmt.Errorf("initialize settings: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO metadata(id, started_at_ns, total_received) VALUES(1, ?, 0)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("initialize metadata: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	s.lifecycle.Lock()
	if s.closing {
		s.lifecycle.Unlock()
		return nil
	}
	s.closing = true
	close(s.stopWriter)
	s.lifecycle.Unlock()
	<-s.writerDone
	return s.db.Close()
}

func (s *Store) Add(events ...Event) error {
	if len(events) == 0 {
		return nil
	}
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.closing {
		return errors.New("telemetry store is closed")
	}
	request := writeRequest{events: append([]Event(nil), events...), done: make(chan error, 1)}
	s.writeCh <- request
	return <-request.done
}

func (s *Store) writeLoop() {
	defer close(s.writerDone)
	const maxBatchEvents = 2_000
	for {
		var pending []writeRequest
		select {
		case request := <-s.writeCh:
			pending = append(pending, request)
		case <-s.stopWriter:
			return
		}
		total := len(pending[0].events)
		timer := time.NewTimer(s.writeDelay)
	collect:
		for total < maxBatchEvents {
			select {
			case request := <-s.writeCh:
				pending = append(pending, request)
				total += len(request.events)
			case <-timer.C:
				break collect
			case <-s.stopWriter:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				err := s.insertRequests(pending)
				for _, request := range pending {
					request.done <- err
				}
				return
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		err := s.insertRequests(pending)
		for _, request := range pending {
			request.done <- err
		}
	}
}

func (s *Store) insertRequests(requests []writeRequest) error {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO events(
signal,timestamp_ns,received_at_ns,service,instance,scope,name,severity,message,trace_id,span_id,parent_span_id,kind,duration_ms,metric_value,unit,metric_type,attributes_json
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	total := 0
	for _, request := range requests {
		for i := range request.events {
			e := request.events[i]
			if e.Timestamp.IsZero() {
				e.Timestamp = now
			}
			if e.Service == "" {
				e.Service = "unknown-service"
			}
			attrs, err := json.Marshal(e.Attributes)
			if err != nil {
				return fmt.Errorf("encode attributes: %w", err)
			}
			var metricValue any
			if e.Value != nil {
				metricValue = *e.Value
			}
			if _, err := stmt.Exec(e.Signal, e.Timestamp.UTC().UnixNano(), now.UnixNano(), e.Service, e.Instance, e.Scope, e.Name,
				e.Severity, e.Message, e.TraceID, e.SpanID, e.ParentSpanID, e.Kind, e.DurationMS, metricValue, e.Unit, e.MetricType, string(attrs)); err != nil {
				return fmt.Errorf("insert telemetry: %w", err)
			}
			total++
		}
	}
	if _, err := tx.Exec(`UPDATE metadata SET total_received = total_received + ? WHERE id = 1`, total); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Bound cleanup work during sustained ingestion without a goroutine per request.
	last := time.Unix(0, s.lastPurge.Load())
	if time.Since(last) >= time.Minute && s.lastPurge.CompareAndSwap(last.UnixNano(), now.UnixNano()) {
		_, _ = s.Purge()
	}
	return nil
}

func (s *Store) Query(f Filter) ([]Event, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	where, args := filterSQL(f)
	rows, err := s.db.Query(`SELECT id,signal,timestamp_ns,received_at_ns,service,instance,scope,name,severity,message,trace_id,span_id,parent_span_id,kind,duration_ms,metric_value,unit,metric_type,attributes_json
FROM events `+where+` ORDER BY timestamp_ns DESC, id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func filterSQL(f Filter) (string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, value any) { clauses = append(clauses, clause); args = append(args, value) }
	if f.Signal != "" {
		add("signal = ?", f.Signal)
	}
	if f.Service != "" {
		add("service = ?", f.Service)
	}
	if f.Severity != "" {
		add("UPPER(severity) = UPPER(?)", f.Severity)
	}
	if f.Name != "" {
		add("name = ?", f.Name)
	}
	if !f.From.IsZero() {
		add("timestamp_ns >= ?", f.From.UTC().UnixNano())
	}
	if !f.To.IsZero() {
		add("timestamp_ns <= ?", f.To.UTC().UnixNano())
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		clauses = append(clauses, `(LOWER(message) LIKE ? OR LOWER(name) LIKE ? OR LOWER(service) LIKE ? OR LOWER(instance) LIKE ? OR LOWER(trace_id) LIKE ? OR LOWER(attributes_json) LIKE ?)`)
		for range 6 {
			args = append(args, like)
		}
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (Event, error) {
	var e Event
	var timestamp, received int64
	var metric sql.NullFloat64
	var attrs string
	err := row.Scan(&e.ID, &e.Signal, &timestamp, &received, &e.Service, &e.Instance, &e.Scope, &e.Name, &e.Severity, &e.Message,
		&e.TraceID, &e.SpanID, &e.ParentSpanID, &e.Kind, &e.DurationMS, &metric, &e.Unit, &e.MetricType, &attrs)
	if err != nil {
		return e, err
	}
	e.Timestamp = time.Unix(0, timestamp).UTC()
	e.ReceivedAt = time.Unix(0, received).UTC()
	if metric.Valid {
		value := metric.Float64
		e.Value = &value
	}
	if err := json.Unmarshal([]byte(attrs), &e.Attributes); err != nil {
		return e, err
	}
	return e, nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Event(id uint64) (Event, error) {
	row := s.db.QueryRow(`SELECT id,signal,timestamp_ns,received_at_ns,service,instance,scope,name,severity,message,trace_id,span_id,parent_span_id,kind,duration_ms,metric_value,unit,metric_type,attributes_json FROM events WHERE id = ?`, id)
	return scanEvent(row)
}

func (s *Store) Snapshot() (Summary, error) {
	settings, err := s.Settings()
	if err != nil {
		return Summary{}, err
	}
	var summary Summary
	summary.Capacity = settings.MaxEvents
	var started int64
	if err := s.db.QueryRow(`SELECT started_at_ns,total_received FROM metadata WHERE id=1`).Scan(&started, &summary.Received); err != nil {
		return summary, err
	}
	summary.StartedAt = time.Unix(0, started).UTC()
	if err := s.db.QueryRow(`SELECT COUNT(*),
COALESCE(SUM(signal='logs'),0), COALESCE(SUM(UPPER(severity) LIKE '%ERROR%' OR UPPER(severity) LIKE '%FATAL%'),0),
COALESCE(SUM(signal='traces'),0), COALESCE(SUM(signal='metrics'),0) FROM events`).Scan(&summary.Stored, &summary.Logs, &summary.Errors, &summary.Spans, &summary.Metrics); err != nil {
		return summary, err
	}
	rows, err := s.db.Query(`SELECT service,instance,MAX(received_at_ns),
SUM(signal='logs'),SUM(UPPER(severity) LIKE '%ERROR%' OR UPPER(severity) LIKE '%FATAL%'),SUM(signal='traces'),SUM(signal='metrics')
FROM events GROUP BY service,instance ORDER BY MAX(received_at_ns) DESC LIMIT 100`)
	if err != nil {
		return summary, err
	}
	for rows.Next() {
		var inst Instance
		var seen int64
		if err := rows.Scan(&inst.Service, &inst.Instance, &seen, &inst.Logs, &inst.Errors, &inst.Spans, &inst.Metrics); err != nil {
			rows.Close()
			return summary, err
		}
		inst.LastSeen = time.Unix(0, seen).UTC()
		summary.Instances = append(summary.Instances, inst)
	}
	if err := rows.Close(); err != nil {
		return summary, err
	}
	summary.Recent, err = s.Query(Filter{Limit: 12})
	return summary, err
}

func (s *Store) Facets() (Facets, error) {
	var result Facets
	for query, target := range map[string]*[]string{
		`SELECT DISTINCT service FROM events WHERE service<>'' ORDER BY service`:                      &result.Services,
		`SELECT DISTINCT severity FROM events WHERE signal='logs' AND severity<>'' ORDER BY severity`: &result.Severities,
		`SELECT DISTINCT name FROM events WHERE signal='metrics' AND name<>'' ORDER BY name`:          &result.MetricNames,
	} {
		rows, err := s.db.Query(query)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return result, err
			}
			*target = append(*target, value)
		}
		if err := rows.Close(); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Store) Metrics(f Filter, groupBy string) ([]MetricSeries, error) {
	f.Signal = "metrics"
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 2000
	}
	where, args := filterSQL(f)
	rows, err := s.db.Query(`SELECT timestamp_ns,service,instance,name,metric_value,unit,metric_type FROM events `+where+` AND metric_value IS NOT NULL ORDER BY timestamp_ns ASC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := map[string]*MetricSeries{}
	for rows.Next() {
		var timestamp int64
		var service, instance, name, unit, metricType string
		var value float64
		if err := rows.Scan(&timestamp, &service, &instance, &name, &value, &unit, &metricType); err != nil {
			return nil, err
		}
		key := name
		switch groupBy {
		case "service":
			key += " · " + service
		case "instance":
			key += " · " + service + "/" + instance
		}
		item := series[key]
		if item == nil {
			item = &MetricSeries{Key: key, Unit: unit, Type: metricType}
			series[key] = item
		}
		item.Points = append(item.Points, MetricPoint{Timestamp: time.Unix(0, timestamp).UTC(), Value: value})
	}
	out := make([]MetricSeries, 0, len(series))
	for _, item := range series {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, rows.Err()
}

func (s *Store) Settings() (Settings, error) {
	settings := Settings{DatabasePath: s.path}
	err := s.db.QueryRow(`SELECT retention_hours,max_events FROM settings WHERE id=1`).Scan(&settings.RetentionHours, &settings.MaxEvents)
	return settings, err
}

func (s *Store) UpdateSettings(settings Settings) error {
	if settings.RetentionHours < 1 || settings.RetentionHours > 24*365 {
		return errors.New("retention_hours must be between 1 and 8760")
	}
	if settings.MaxEvents < 100 || settings.MaxEvents > 100_000_000 {
		return errors.New("max_events must be between 100 and 100000000")
	}
	_, err := s.db.Exec(`UPDATE settings SET retention_hours=?,max_events=? WHERE id=1`, settings.RetentionHours, settings.MaxEvents)
	if err == nil {
		_, err = s.Purge()
	}
	return err
}

func (s *Store) Purge() (int64, error) {
	settings, err := s.Settings()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(settings.RetentionHours) * time.Hour).UnixNano()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`DELETE FROM events WHERE received_at_ns < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	removed, _ := result.RowsAffected()
	result, err = tx.Exec(`DELETE FROM events WHERE id IN (SELECT id FROM events ORDER BY id DESC LIMIT -1 OFFSET ?)`, settings.MaxEvents)
	if err != nil {
		return removed, err
	}
	bounded, _ := result.RowsAffected()
	if err := tx.Commit(); err != nil {
		return removed + bounded, err
	}
	return removed + bounded, nil
}

type snapshotKey struct{}

func WithSnapshot(ctx context.Context, value Summary) context.Context {
	return context.WithValue(ctx, snapshotKey{}, value)
}
func SnapshotFrom(ctx context.Context) Summary {
	value, _ := ctx.Value(snapshotKey{}).(Summary)
	return value
}
