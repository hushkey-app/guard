package telemetry

// Views: stored queries, and the compiler that turns one into a Frame.
//
// The whole feature is one idea. Every event guard receives is a row in one
// table, so a panel is a SELECT over that table with a shape the renderer
// recognises. This file owns the SELECT half: the field allowlist, the WHERE
// builder, one query shape per Shape, and the CRUD around the views table.
//
// Nothing here interpolates caller-supplied text into SQL. A field reference is
// either a key in model.Columns — which maps to a column name this file wrote —
// or an attribute key, which is bound as a JSON path parameter. Values are
// always parameters. That is the only reason it is safe to let a dashboard user
// compose queries at all.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

type View = model.View
type ViewQuery = model.ViewQuery
type Frame = model.Frame
type Trace = model.Trace
type TraceSpan = model.TraceSpan

// Attributes worth an index.
//
// Grouping by an attribute means json_extract over every candidate row, which
// is a full scan. These five are the ones a dashboard actually groups by, so
// they get a generated column and an index, and the compiler silently uses the
// column instead of the extract.
//
// Each column COALESCEs the two OpenTelemetry spellings — semconv renamed
// several of these, and exporters in the wild emit both. Merging them is
// deliberate: "the route, whichever name your SDK gives it" is the question
// people mean, and splitting one concept across two panels because a dependency
// was upgraded would be worse than surprising.
var indexedAttributes = []struct {
	Column   string
	Label    string
	Canonical string
	Keys     []string
}{
	{Column: "attr_http_route", Label: "HTTP route", Canonical: "http.route", Keys: []string{"http.route"}},
	{Column: "attr_http_method", Label: "HTTP method", Canonical: "http.request.method", Keys: []string{"http.request.method", "http.method"}},
	{Column: "attr_http_status", Label: "HTTP status", Canonical: "http.response.status_code", Keys: []string{"http.response.status_code", "http.status_code"}},
	{Column: "attr_client_address", Label: "Client address", Canonical: "client.address", Keys: []string{"client.address", "net.peer.ip", "http.client_ip"}},
	{Column: "attr_db_system", Label: "Database system", Canonical: "db.system.name", Keys: []string{"db.system.name", "db.system"}},
}

// attributeColumn maps an attribute key to its generated column, if it has one.
func attributeColumn(key string) (string, bool) {
	for _, a := range indexedAttributes {
		for _, k := range a.Keys {
			if k == key {
				return a.Column, true
			}
		}
	}
	return "", false
}

// migrateViews adds what views need to a database that predates them.
//
// The generated columns go on with ALTER TABLE, which SQLite has no IF NOT
// EXISTS for, so existing columns are read first. They are VIRTUAL: computed on
// read, costing nothing on the write path that ingestion actually cares about,
// and the index over them is what makes the read fast.
func migrateViews(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS views (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  panel TEXT NOT NULL,
  query_json TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 6,
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_views_position ON views(position, id);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate views: %w", err)
	}

	// table_xinfo, not table_info: a VIRTUAL generated column is hidden, so
	// table_info would report it missing on every start and the ALTER below
	// would fail with "duplicate column name" the second time guard ran.
	existing := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_xinfo('events')`)
	if err != nil {
		return fmt.Errorf("read events columns: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, attribute := range indexedAttributes {
		if !existing[attribute.Column] {
			extracts := make([]string, 0, len(attribute.Keys))
			for _, key := range attribute.Keys {
				extracts = append(extracts, fmt.Sprintf(`json_extract(attributes_json,'$."%s"')`, key))
			}
			expression := extracts[0]
			if len(extracts) > 1 {
				expression = "COALESCE(" + strings.Join(extracts, ",") + ")"
			}
			statement := fmt.Sprintf(`ALTER TABLE events ADD COLUMN %s TEXT GENERATED ALWAYS AS (%s) VIRTUAL`, attribute.Column, expression)
			if _, err := db.Exec(statement); err != nil {
				return fmt.Errorf("add %s: %w", attribute.Column, err)
			}
		}
		index := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_events_%s ON events(%s, timestamp_ns DESC) WHERE %s IS NOT NULL`,
			attribute.Column, attribute.Column, attribute.Column)
		if _, err := db.Exec(index); err != nil {
			return fmt.Errorf("index %s: %w", attribute.Column, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Stored views
// ---------------------------------------------------------------------------

func (s *Store) Views() ([]View, error) {
	rows, err := s.db.Query(`SELECT id,name,description,panel,query_json,position,width,created_at_ns,updated_at_ns
FROM views ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]View, 0)
	for rows.Next() {
		view, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

func (s *Store) View(id int64) (View, error) {
	row := s.db.QueryRow(`SELECT id,name,description,panel,query_json,position,width,created_at_ns,updated_at_ns
FROM views WHERE id = ?`, id)
	return scanView(row)
}

func scanView(row scanner) (View, error) {
	var view View
	var query string
	var created, updated int64
	if err := row.Scan(&view.ID, &view.Name, &view.Description, &view.Panel, &query, &view.Position, &view.Width, &created, &updated); err != nil {
		return view, err
	}
	view.CreatedAt = time.Unix(0, created).UTC()
	view.UpdatedAt = time.Unix(0, updated).UTC()
	return view, json.Unmarshal([]byte(query), &view.Query)
}

func (s *Store) SaveView(view View) (View, error) {
	if err := view.Validate(); err != nil {
		return View{}, err
	}
	view.Query = view.Query.Normalize(view.Panel)
	if view.Width <= 0 {
		view.Width = 6
	}
	query, err := json.Marshal(view.Query)
	if err != nil {
		return View{}, err
	}
	now := time.Now().UTC().UnixNano()
	if view.ID == 0 {
		// New views land at the end rather than the top: a dashboard that
		// reorders itself every time something is added is one nobody can learn
		// the shape of.
		var position int
		if err := s.db.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM views`).Scan(&position); err != nil {
			return View{}, err
		}
		result, err := s.db.Exec(`INSERT INTO views(name,description,panel,query_json,position,width,created_at_ns,updated_at_ns)
VALUES(?,?,?,?,?,?,?,?)`, view.Name, view.Description, view.Panel, string(query), position, view.Width, now, now)
		if err != nil {
			return View{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return View{}, err
		}
		return s.View(id)
	}
	result, err := s.db.Exec(`UPDATE views SET name=?,description=?,panel=?,query_json=?,position=?,width=?,updated_at_ns=? WHERE id=?`,
		view.Name, view.Description, view.Panel, string(query), view.Position, view.Width, now, view.ID)
	if err != nil {
		return View{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return View{}, sql.ErrNoRows
	}
	return s.View(view.ID)
}

func (s *Store) DeleteView(id int64) error {
	result, err := s.db.Exec(`DELETE FROM views WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// FieldCatalog is what the builder offers to group and filter by: the columns
// every event has, and the attribute keys this instance has actually seen.
//
// The attribute half is sampled from the newest few thousand events rather than
// scanned. A key that appears nowhere in recent telemetry is not one anybody is
// about to build a panel on, and the alternative — json_each over the whole
// table on every builder keystroke — is exactly the full scan the generated
// columns exist to avoid.
func (s *Store) FieldCatalog() (model.Fields, error) {
	var out model.Fields
	for _, info := range model.Columns {
		out.Columns = append(out.Columns, info)
	}
	sort.Slice(out.Columns, func(i, j int) bool { return out.Columns[i].Label < out.Columns[j].Label })

	seen := map[string]bool{}
	for _, attribute := range indexedAttributes {
		seen[attribute.Canonical] = true
		out.Attributes = append(out.Attributes, model.FieldInfo{
			Ref: "attr:" + attribute.Canonical, Label: attribute.Label, Type: "string", Indexed: true,
		})
	}

	// json_each over anything that is not an object or an array yields one row
	// with a NULL key, and an event stored with no attributes at all is the JSON
	// scalar `null`. One such row anywhere in the sample is enough to fail the
	// whole catalogue — and the catalogue is what the builder populates every
	// picker from, so the failure reads as "the builder does not open".
	rows, err := s.db.Query(`SELECT DISTINCT j.key FROM (SELECT attributes_json FROM events
WHERE json_valid(attributes_json) AND json_type(attributes_json) = 'object'
ORDER BY id DESC LIMIT 2000) e, json_each(e.attributes_json) j
WHERE j.key IS NOT NULL ORDER BY j.key LIMIT 300`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return out, err
		}
		if seen[key] {
			continue
		}
		// A key with an indexed column under another spelling is the same
		// field; offering both would let two panels disagree about one number.
		if column, ok := attributeColumn(key); ok {
			_ = column
			continue
		}
		out.Attributes = append(out.Attributes, model.FieldInfo{Ref: "attr:" + key, Label: key, Type: "string"})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Field references
// ---------------------------------------------------------------------------

// expr is a resolved field: SQL text this file produced, plus any parameters it
// needs, in the order they appear.
type expr struct {
	sql  string
	args []any
	typ  string
}

func resolveField(ref string) (expr, error) {
	if key, ok := model.AttributeRef(ref); ok {
		if strings.ContainsAny(key, `"\`) {
			return expr{}, fmt.Errorf("attribute key %q contains a quote", key)
		}
		if column, ok := attributeColumn(key); ok {
			return expr{sql: column, typ: "string"}, nil
		}
		return expr{sql: "json_extract(attributes_json, ?)", args: []any{`$."` + key + `"`}, typ: "string"}, nil
	}
	info, ok := model.Columns[ref]
	if !ok {
		return expr{}, fmt.Errorf("unknown field %q", ref)
	}
	column := ref
	switch ref {
	case "timestamp":
		column = "timestamp_ns"
	case "value":
		column = "metric_value"
	}
	return expr{sql: column, typ: info.Type}, nil
}

// numeric is a field read as a number. Attributes have no declared type, so
// they are cast; a numeric column is already one, and casting it would only
// stop an index being used.
func numericExpr(ref string) (expr, error) {
	e, err := resolveField(ref)
	if err != nil {
		return e, err
	}
	if e.typ != "number" {
		e.sql = "CAST(" + e.sql + " AS REAL)"
		e.typ = "number"
	}
	return e, nil
}

// builder accumulates SQL and its parameters together, so the two cannot get
// out of order — the failure mode that turns a query into either an error or,
// worse, a wrong answer.
type builder struct {
	sql  strings.Builder
	args []any
}

func (b *builder) write(text string, args ...any) *builder {
	b.sql.WriteString(text)
	b.args = append(b.args, args...)
	return b
}

func (b *builder) writeExpr(e expr) *builder { return b.write(e.sql, e.args...) }

func (b *builder) String() string { return b.sql.String() }

// ---------------------------------------------------------------------------
// WHERE
// ---------------------------------------------------------------------------

func whereClause(q ViewQuery, now time.Time) (string, []any, error) {
	var clauses []string
	var args []any

	if q.Signal != "" {
		clauses = append(clauses, "signal = ?")
		args = append(args, q.Signal)
	}
	from, to := q.Window(now)
	if !from.IsZero() {
		clauses = append(clauses, "timestamp_ns >= ?")
		args = append(args, from.UnixNano())
	}
	if !to.IsZero() {
		clauses = append(clauses, "timestamp_ns <= ?")
		args = append(args, to.UnixNano())
	}
	for _, condition := range q.Filters {
		clause, conditionArgs, err := conditionClause(condition)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		args = append(args, conditionArgs...)
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func conditionClause(c model.Condition) (string, []any, error) {
	e, err := resolveField(c.Field)
	if err != nil {
		return "", nil, err
	}
	args := append([]any(nil), e.args...)
	switch c.Op {
	case "eq":
		return "(" + e.sql + ") = ?", append(args, c.Value), nil
	case "ne":
		// A row where the field is absent is not equal to the value, and a
		// plain <> would drop it: NULL <> 'x' is NULL, not true.
		return "((" + e.sql + ") IS NULL OR (" + e.sql + ") <> ?)", append(append(args, e.args...), c.Value), nil
	case "contains":
		return "LOWER(CAST((" + e.sql + ") AS TEXT)) LIKE ?", append(args, "%"+strings.ToLower(c.Value)+"%"), nil
	case "prefix":
		return "LOWER(CAST((" + e.sql + ") AS TEXT)) LIKE ?", append(args, strings.ToLower(c.Value)+"%"), nil
	case "gt", "gte", "lt", "lte":
		number, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if err != nil {
			return "", nil, fmt.Errorf("%s needs a number, got %q", c.Op, c.Value)
		}
		operators := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}
		return "CAST((" + e.sql + ") AS REAL) " + operators[c.Op] + " ?", append(args, number), nil
	case "exists":
		return "((" + e.sql + ") IS NOT NULL AND (" + e.sql + ") <> '')", append(args, e.args...), nil
	case "missing":
		return "((" + e.sql + ") IS NULL OR (" + e.sql + ") = '')", append(args, e.args...), nil
	}
	return "", nil, fmt.Errorf("unknown operator %q", c.Op)
}

// ---------------------------------------------------------------------------
// Aggregation
// ---------------------------------------------------------------------------

var percentiles = map[string]float64{"p50": .50, "p75": .75, "p90": .90, "p95": .95, "p99": .99}

// aggregate writes the aggregate for the non-percentile cases, over the scoped
// CTE's already-computed v. Percentiles are not expressible as one — see
// percentileFrom.
func aggregate(agg string) (string, bool) {
	switch agg {
	case "", "count":
		return "COUNT(*)", true
	case "sum", "avg", "min", "max":
		return strings.ToUpper(agg) + "(v)", true
	}
	return "", false
}

// percentileFrom is the exact nearest-rank percentile, over a subquery that has
// already numbered each partition's rows.
//
// SQLite has no percentile function and no extension is loaded here, so the
// choice was between shipping an interpolated approximation and computing the
// real thing with window functions. p95 that is quietly an estimate is the kind
// of number people make decisions on, so: the real thing.
func percentileFrom(fraction float64) string {
	return fmt.Sprintf("rn = MAX(1, CAST(ROUND(n * %g) AS INTEGER))", fraction)
}

// ---------------------------------------------------------------------------
// RunView
// ---------------------------------------------------------------------------

// RunView compiles the query for the panel's shape and runs it.
//
// The switch is on Shape, not on panel: a bar chart and a pie chart ask the
// database exactly the same question, and a candlestick and a box plot differ
// only in which four numbers per bucket they want. That is the whole reason
// adding a panel is usually a renderer and nothing else.
func (s *Store) RunView(panel string, q ViewQuery) (Frame, error) {
	spec, ok := model.PanelByName(panel)
	if !ok {
		return Frame{}, fmt.Errorf("unknown panel %q", panel)
	}
	if err := q.ValidateFor(panel); err != nil {
		return Frame{}, err
	}
	q = q.Normalize(panel)
	now := time.Now().UTC()
	frame := Frame{Shape: spec.Shape, Panel: panel}

	var err error
	switch spec.Shape {
	case model.ShapeTimeseries:
		err = s.runTimeseries(&frame, q, now)
	case model.ShapeCategorical:
		err = s.runCategorical(&frame, q, now)
	case model.ShapeDistribution:
		err = s.runDistribution(&frame, q, now)
	case model.ShapeHeatmap:
		err = s.runHeatmap(&frame, q, now)
	case model.ShapeScatter:
		err = s.runScatter(&frame, q, now)
	case model.ShapeOHLC:
		err = s.runOHLC(&frame, q, now, panel)
	case model.ShapeSingle:
		err = s.runSingle(&frame, q, now)
	case model.ShapeSpanTree:
		err = s.runSpanTree(&frame, q, now)
	default:
		err = fmt.Errorf("no compiler for shape %q", spec.Shape)
	}
	if err != nil {
		return Frame{}, err
	}
	if frame.Rows == nil {
		frame.Rows = [][]any{}
	}
	// An empty panel is the one result that cannot explain itself. "Nothing
	// matched" is true of a filter that matches nothing, of a window that ends
	// before the data starts, and of an instance no exporter has ever reached —
	// three different problems with three different fixes, and the panel knows
	// which one it is.
	if len(frame.Rows) == 0 {
		if hint, err := s.emptyHint(q, now); err == nil && hint != "" {
			frame.Notes = append(frame.Notes, hint)
		}
	}
	return frame, nil
}

// emptyHint says why a panel is empty, in one sentence.
//
// It costs two small queries, and only on the path where the answer was empty
// anyway — which is exactly the path where someone is about to go looking for a
// reason.
func (s *Store) emptyHint(q ViewQuery, now time.Time) (string, error) {
	var stored int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&stored); err != nil {
		return "", err
	}
	if stored == 0 {
		return "No telemetry has arrived yet — this instance's store is empty. Point an exporter at its OTLP/HTTP endpoint, or run `make seed` for a sample workload.", nil
	}

	// The same filters over all of time: if they match nothing at all, the
	// window is not the problem and widening it will not help.
	unbounded := q
	unbounded.Range, unbounded.From, unbounded.To = "all", time.Time{}, time.Time{}
	where, args, err := whereClause(unbounded, now)
	if err != nil {
		return "", err
	}
	var newest sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(timestamp_ns) FROM events`+where, args...).Scan(&newest); err != nil {
		return "", err
	}
	if !newest.Valid {
		if len(q.Filters) > 0 || q.Signal != "" {
			return "Nothing matches these filters in any window — the events are there, but not these ones.", nil
		}
		return "Nothing matches this query in any window.", nil
	}

	from, _ := q.Window(now)
	at := time.Unix(0, newest.Int64).UTC()
	if from.IsZero() || !at.Before(from) {
		return "", nil // Something matched outside the window but inside it too: not this problem.
	}
	age := now.Sub(at).Round(time.Minute)
	return fmt.Sprintf("Nothing in this window. The newest match is %s old (%s) — widen the time range to see it.",
		humaniseAge(age), at.Local().Format("15:04 on 2 Jan")), nil
}

func humaniseAge(age time.Duration) string {
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%d minutes", int(age.Minutes()))
	case age < 48*time.Hour:
		return fmt.Sprintf("%.1f hours", age.Hours())
	default:
		return fmt.Sprintf("%d days", int(age.Hours()/24))
	}
}

// scopedCTE builds the CTE every aggregate shape reads: the rows in the window,
// with the time bucket, the series key and the numeric value already computed.
// Callers that do not need one of the three pass an empty ref and get a
// constant, which keeps a single CTE shape instead of four near-identical ones.
//
// It does not filter out rows whose value is NULL — an alias is not in scope in
// its own WHERE clause. Every consumer does it instead, and must: an average
// over the rows that happened to carry the field, reported as though it covered
// all of them, is a wrong number rather than a missing one.
func scopedCTE(q ViewQuery, now time.Time, bucketNS int64, groupRef, valueRef string) (*builder, error) {
	b := &builder{}
	b.write("WITH scoped AS (SELECT id, timestamp_ns AS ts")

	if bucketNS > 0 {
		b.write(", (timestamp_ns / ?) * ? AS bucket", bucketNS, bucketNS)
	} else {
		b.write(", 0 AS bucket")
	}

	if groupRef != "" {
		group, err := resolveField(groupRef)
		if err != nil {
			return nil, err
		}
		// COALESCE, not the raw extract: a NULL group key silently forms its
		// own invisible series, and "the events with no route" is a real answer
		// that deserves a label.
		b.write(", COALESCE(NULLIF(CAST(").writeExpr(group).write(" AS TEXT), ''), '(none)') AS series")
	} else {
		b.write(", '' AS series")
	}

	if valueRef != "" {
		value, err := numericExpr(valueRef)
		if err != nil {
			return nil, err
		}
		b.write(", ").writeExpr(value).write(" AS v")
	} else {
		b.write(", 1.0 AS v")
	}

	b.write(" FROM events")
	where, args, err := whereClause(q, now)
	if err != nil {
		return nil, err
	}
	b.write(where, args...)
	b.write(")")
	return b, nil
}

func (s *Store) runTimeseries(frame *Frame, q ViewQuery, now time.Time) error {
	bucket := q.BucketSize(now)
	valueRef := q.Value
	if q.Agg == "count" {
		valueRef = ""
	}
	scoped, err := scopedCTE(q, now, bucket.Nanoseconds(), q.GroupBy, valueRef)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)

	// Cap the number of series in SQL rather than after the fact: grouping by
	// something high-cardinality (trace_id, say) is one keystroke away in the
	// builder, and the difference between a bounded query and an unbounded one
	// is a dashboard that stays up.
	seriesLimit := 12
	b.write(", ranked AS (SELECT series FROM scoped WHERE v IS NOT NULL GROUP BY series ORDER BY COUNT(*) DESC LIMIT ?)", seriesLimit)

	if fraction, ok := percentiles[q.Agg]; ok {
		b.write(` SELECT bucket, series, v FROM (
SELECT bucket, series, v, ROW_NUMBER() OVER (PARTITION BY bucket, series ORDER BY v) AS rn,
COUNT(*) OVER (PARTITION BY bucket, series) AS n FROM scoped
WHERE v IS NOT NULL AND series IN (SELECT series FROM ranked)) WHERE `)
		b.write(percentileFrom(fraction))
		b.write(" ORDER BY bucket, series")
	} else {
		aggSQL, ok := aggregate(q.Agg)
		if !ok {
			return fmt.Errorf("unknown aggregation %q", q.Agg)
		}
		b.write(" SELECT bucket, series, " + aggSQL + " AS value FROM scoped WHERE v IS NOT NULL AND series IN (SELECT series FROM ranked) GROUP BY bucket, series ORDER BY bucket, series")
	}

	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var bucketNS int64
		var series string
		var value float64
		if err := rows.Scan(&bucketNS, &series, &value); err != nil {
			return err
		}
		if !seen[series] {
			seen[series] = true
			frame.Series = append(frame.Series, series)
		}
		frame.Rows = append(frame.Rows, []any{time.Unix(0, bucketNS).UTC(), series, value})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(frame.Series)
	frame.Fields = []model.Field{{Name: "time", Type: "time"}, {Name: "series", Type: "string"}, {Name: "value", Type: "number"}}
	frame.Unit = unitFor(q)

	if q.GroupBy != "" {
		total, err := s.distinctSeries(q, now)
		if err == nil && total > seriesLimit {
			frame.Notes = append(frame.Notes, fmt.Sprintf("%d series matched; showing the %d busiest.", total, seriesLimit))
		}
	}
	return nil
}

func (s *Store) distinctSeries(q ViewQuery, now time.Time) (int, error) {
	group, err := resolveField(q.GroupBy)
	if err != nil {
		return 0, err
	}
	b := &builder{}
	b.write("SELECT COUNT(DISTINCT ").writeExpr(group).write(") FROM events")
	where, args, err := whereClause(q, now)
	if err != nil {
		return 0, err
	}
	b.write(where, args...)
	var total int
	return total, s.db.QueryRow(b.String(), b.args...).Scan(&total)
}

func (s *Store) runCategorical(frame *Frame, q ViewQuery, now time.Time) error {
	valueRef := q.Value
	if q.Agg == "count" {
		valueRef = ""
	}
	scoped, err := scopedCTE(q, now, 0, q.GroupBy, valueRef)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)

	if fraction, ok := percentiles[q.Agg]; ok {
		b.write(` SELECT series, v FROM (
SELECT series, v, ROW_NUMBER() OVER (PARTITION BY series ORDER BY v) AS rn,
COUNT(*) OVER (PARTITION BY series) AS n FROM scoped WHERE v IS NOT NULL) WHERE `)
		b.write(percentileFrom(fraction))
	} else {
		aggSQL, ok := aggregate(q.Agg)
		if !ok {
			return fmt.Errorf("unknown aggregation %q", q.Agg)
		}
		b.write(" SELECT series, " + aggSQL + " AS value FROM scoped WHERE v IS NOT NULL GROUP BY series")
	}
	b.write(orderClause(q.Order))
	b.write(" LIMIT ?", q.Limit)

	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var category string
		var value float64
		if err := rows.Scan(&category, &value); err != nil {
			return err
		}
		frame.Rows = append(frame.Rows, []any{category, value})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	frame.Fields = []model.Field{{Name: "category", Type: "string"}, {Name: "value", Type: "number"}}
	frame.Unit = unitFor(q)

	total, err := s.distinctSeries(q, now)
	if err == nil && total > len(frame.Rows) {
		frame.Notes = append(frame.Notes, fmt.Sprintf("%d categories matched; showing %d.", total, len(frame.Rows)))
	}
	return nil
}

func orderClause(order string) string {
	switch order {
	case "value_asc":
		return " ORDER BY 2 ASC"
	case "key_asc":
		return " ORDER BY 1 ASC"
	case "key_desc":
		return " ORDER BY 1 DESC"
	default:
		return " ORDER BY 2 DESC"
	}
}

// bounds is the min/max/count of the value across the window: a histogram
// cannot choose its buckets without knowing the range, and two round trips beat
// pulling every value into Go to find out.
func (s *Store) bounds(q ViewQuery, now time.Time) (low, high float64, count int, err error) {
	scoped, err := scopedCTE(q, now, 0, "", q.Value)
	if err != nil {
		return 0, 0, 0, err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)
	b.write(" SELECT MIN(v), MAX(v), COUNT(*) FROM scoped WHERE v IS NOT NULL")
	var minimum, maximum sql.NullFloat64
	if err := s.db.QueryRow(b.String(), b.args...).Scan(&minimum, &maximum, &count); err != nil {
		return 0, 0, 0, err
	}
	if !minimum.Valid || count == 0 {
		return 0, 0, 0, nil
	}
	low, high = minimum.Float64, maximum.Float64
	if high <= low {
		// One distinct value still deserves a bar rather than a division by
		// zero, so give it a nominal width.
		high = low + math.Max(math.Abs(low)*0.1, 1)
	}
	return low, high, count, nil
}

func (s *Store) runDistribution(frame *Frame, q ViewQuery, now time.Time) error {
	low, high, count, err := s.bounds(q, now)
	if err != nil {
		return err
	}
	frame.Fields = []model.Field{{Name: "bucket_start", Type: "number"}, {Name: "bucket_end", Type: "number"}, {Name: "count", Type: "number"}}
	frame.Unit = unitFor(q)
	if count == 0 {
		return nil
	}
	buckets := q.Buckets
	width := (high - low) / float64(buckets)

	scoped, err := scopedCTE(q, now, 0, "", q.Value)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)
	// MIN(…, buckets-1) puts the maximum value in the last bucket instead of a
	// bucket of its own past the end.
	b.write(" SELECT MIN(CAST((v - ?) / ? AS INTEGER), ?) AS b, COUNT(*) FROM scoped WHERE v IS NOT NULL GROUP BY b ORDER BY b",
		low, width, buckets-1)

	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := make([]int, buckets)
	for rows.Next() {
		var index, n int
		if err := rows.Scan(&index, &n); err != nil {
			return err
		}
		if index >= 0 && index < buckets {
			counts[index] = n
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Empty buckets are emitted too: a gap in a histogram is information, and
	// leaving them out makes a bimodal distribution look continuous.
	for i, n := range counts {
		frame.Rows = append(frame.Rows, []any{low + float64(i)*width, low + float64(i+1)*width, n})
	}
	return nil
}

func (s *Store) runHeatmap(frame *Frame, q ViewQuery, now time.Time) error {
	low, high, count, err := s.bounds(q, now)
	if err != nil {
		return err
	}
	frame.Fields = []model.Field{{Name: "time", Type: "time"}, {Name: "bucket_start", Type: "number"}, {Name: "bucket_end", Type: "number"}, {Name: "count", Type: "number"}}
	frame.Unit = unitFor(q)
	if count == 0 {
		return nil
	}
	buckets := q.Buckets
	width := (high - low) / float64(buckets)
	bucket := q.BucketSize(now)

	scoped, err := scopedCTE(q, now, bucket.Nanoseconds(), "", q.Value)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)
	b.write(" SELECT bucket, MIN(CAST((v - ?) / ? AS INTEGER), ?) AS b, COUNT(*) FROM scoped WHERE v IS NOT NULL GROUP BY bucket, b ORDER BY bucket, b",
		low, width, buckets-1)

	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucketNS int64
		var index, n int
		if err := rows.Scan(&bucketNS, &index, &n); err != nil {
			return err
		}
		frame.Rows = append(frame.Rows, []any{
			time.Unix(0, bucketNS).UTC(),
			low + float64(index)*width,
			low + float64(index+1)*width,
			n,
		})
	}
	return rows.Err()
}

func (s *Store) runScatter(frame *Frame, q ViewQuery, now time.Time) error {
	x, err := resolveField(q.X)
	if err != nil {
		return err
	}
	y, err := numericExpr(q.Value)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write("SELECT id, ").writeExpr(x).write(" AS x, ").writeExpr(y).write(" AS y")
	if q.GroupBy != "" {
		group, err := resolveField(q.GroupBy)
		if err != nil {
			return err
		}
		b.write(", COALESCE(NULLIF(CAST(").writeExpr(group).write(" AS TEXT), ''), '(none)') AS label")
	} else {
		b.write(", '' AS label")
	}
	b.write(" FROM events")
	where, args, err := whereClause(q, now)
	if err != nil {
		return err
	}
	b.write(where, args...)
	b.write(" ORDER BY timestamp_ns DESC LIMIT ?", q.Limit)
	// Wrapped rather than filtered inline: x and y are aliases from this same
	// SELECT list, and dropping the rows where either is missing is a different
	// query from limiting to the newest matches.
	statement := "SELECT * FROM (" + b.String() + ") WHERE x IS NOT NULL AND y IS NOT NULL"

	rows, err := s.db.Query(statement, b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	timeAxis := x.typ == "time"
	seen := map[string]bool{}
	for rows.Next() {
		var id uint64
		var xv float64
		var yv float64
		var label string
		if err := rows.Scan(&id, &xv, &yv, &label); err != nil {
			return err
		}
		var xValue any = xv
		if timeAxis {
			xValue = time.Unix(0, int64(xv)).UTC()
		}
		if label != "" && !seen[label] {
			seen[label] = true
			frame.Series = append(frame.Series, label)
		}
		frame.Rows = append(frame.Rows, []any{xValue, yv, label, id})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sort.Strings(frame.Series)
	xType := "number"
	if timeAxis {
		xType = "time"
	}
	frame.Fields = []model.Field{{Name: "x", Type: xType}, {Name: "y", Type: "number"}, {Name: "label", Type: "string"}, {Name: "event_id", Type: "number"}}
	frame.Unit = unitFor(q)
	if len(frame.Rows) == q.Limit {
		frame.Notes = append(frame.Notes, fmt.Sprintf("Showing the newest %d points; older matches are not drawn.", q.Limit))
	}
	return nil
}

// runOHLC produces the four-number-per-bucket frame both candlestick and box
// read, and which four they are depends on the panel.
//
// Candlestick wants open and close, which only mean something for a quantity
// that exists continuously and is sampled — queue depth, active carts, memory.
// Box wants min/p25/p75/max, which is the honest summary for discrete events,
// where "the first checkout in this hour" is arrival order and nothing more.
// Same shape, same SQL skeleton, different four columns.
func (s *Store) runOHLC(frame *Frame, q ViewQuery, now time.Time, panel string) error {
	bucket := q.BucketSize(now)
	scoped, err := scopedCTE(q, now, bucket.Nanoseconds(), "", q.Value)
	if err != nil {
		return err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)

	if panel == "candlestick" {
		b.write(` SELECT bucket,
MAX(CASE WHEN first_rn = 1 THEN v END) AS open,
MAX(v) AS high,
MIN(v) AS low,
MAX(CASE WHEN last_rn = 1 THEN v END) AS close
FROM (SELECT bucket, v,
ROW_NUMBER() OVER (PARTITION BY bucket ORDER BY ts, id) AS first_rn,
ROW_NUMBER() OVER (PARTITION BY bucket ORDER BY ts DESC, id DESC) AS last_rn
FROM scoped WHERE v IS NOT NULL)
GROUP BY bucket ORDER BY bucket`)
		frame.Fields = []model.Field{{Name: "time", Type: "time"}, {Name: "open", Type: "number"}, {Name: "high", Type: "number"}, {Name: "low", Type: "number"}, {Name: "close", Type: "number"}}
	} else {
		b.write(` SELECT bucket,
MIN(v) AS low,
MAX(CASE WHEN rn = MAX(1, CAST(ROUND(n * 0.25) AS INTEGER)) THEN v END) AS q1,
MAX(CASE WHEN rn = MAX(1, CAST(ROUND(n * 0.75) AS INTEGER)) THEN v END) AS q3,
MAX(v) AS high
FROM (SELECT bucket, v,
ROW_NUMBER() OVER (PARTITION BY bucket ORDER BY v) AS rn,
COUNT(*) OVER (PARTITION BY bucket) AS n
FROM scoped WHERE v IS NOT NULL)
GROUP BY bucket ORDER BY bucket`)
		frame.Fields = []model.Field{{Name: "time", Type: "time"}, {Name: "min", Type: "number"}, {Name: "p25", Type: "number"}, {Name: "p75", Type: "number"}, {Name: "max", Type: "number"}}
	}

	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var bucketNS int64
		var a, c, d, e sql.NullFloat64
		if err := rows.Scan(&bucketNS, &a, &c, &d, &e); err != nil {
			return err
		}
		frame.Rows = append(frame.Rows, []any{time.Unix(0, bucketNS).UTC(), a.Float64, c.Float64, d.Float64, e.Float64})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	frame.Unit = unitFor(q)
	return nil
}

// runSingle is one number, plus the same number over the window before it. The
// comparison is what makes a stat panel worth more than the count in the corner
// of the page — 412 means nothing until you know yesterday was 40.
func (s *Store) runSingle(frame *Frame, q ViewQuery, now time.Time) error {
	current, err := s.singleValue(q, now)
	if err != nil {
		return err
	}
	frame.Fields = []model.Field{{Name: "value", Type: "number"}, {Name: "previous", Type: "number"}}
	frame.Unit = unitFor(q)

	from, to := q.Window(now)
	if from.IsZero() || to.IsZero() {
		frame.Rows = append(frame.Rows, []any{current, nil})
		return nil
	}
	if spark, err := s.spark(q, now, from, to); err == nil {
		frame.Spark = spark
	}
	window := to.Sub(from)
	previousQuery := q
	previousQuery.Range = ""
	previousQuery.From, previousQuery.To = from.Add(-window), from
	previous, err := s.singleValue(previousQuery, now)
	if err != nil {
		return err
	}
	frame.Rows = append(frame.Rows, []any{current, previous})
	return nil
}

// spark is the same measurement as the stat, bucketed across the window, so a
// number can be read against its own recent history.
//
// Empty buckets come back as zero rather than being skipped: a gap drawn as a
// straight line between the points either side of it is a sparkline that
// invents a quiet period it did not have.
func (s *Store) spark(q ViewQuery, now, from, to time.Time) ([]float64, error) {
	const buckets = 24
	step := to.Sub(from) / buckets
	if step <= 0 {
		return nil, nil
	}
	valueRef := q.Value
	if q.Agg == "count" {
		valueRef = ""
	}
	scoped, err := scopedCTE(q, now, step.Nanoseconds(), "", valueRef)
	if err != nil {
		return nil, err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)
	if fraction, ok := percentiles[q.Agg]; ok {
		b.write(` SELECT bucket, v FROM (SELECT bucket, v, ROW_NUMBER() OVER (PARTITION BY bucket ORDER BY v) AS rn,
COUNT(*) OVER (PARTITION BY bucket) AS n FROM scoped WHERE v IS NOT NULL) WHERE `)
		b.write(percentileFrom(fraction))
		b.write(" ORDER BY bucket")
	} else {
		aggSQL, ok := aggregate(q.Agg)
		if !ok {
			return nil, fmt.Errorf("unknown aggregation %q", q.Agg)
		}
		b.write(" SELECT bucket, " + aggSQL + " FROM scoped WHERE v IS NOT NULL GROUP BY bucket ORDER BY bucket")
	}
	rows, err := s.db.Query(b.String(), b.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]float64, buckets)
	origin := from.UnixNano() / step.Nanoseconds()
	for rows.Next() {
		var bucketNS int64
		var value float64
		if err := rows.Scan(&bucketNS, &value); err != nil {
			return nil, err
		}
		if index := int(bucketNS/step.Nanoseconds() - origin); index >= 0 && index < buckets {
			out[index] = value
		}
	}
	return out, rows.Err()
}

func (s *Store) singleValue(q ViewQuery, now time.Time) (float64, error) {
	valueRef := q.Value
	if q.Agg == "count" {
		valueRef = ""
	}
	scoped, err := scopedCTE(q, now, 0, "", valueRef)
	if err != nil {
		return 0, err
	}
	b := &builder{}
	b.write(scoped.String(), scoped.args...)
	if fraction, ok := percentiles[q.Agg]; ok {
		b.write(` SELECT v FROM (SELECT v, ROW_NUMBER() OVER (ORDER BY v) AS rn, COUNT(*) OVER () AS n
FROM scoped WHERE v IS NOT NULL) WHERE `)
		b.write(percentileFrom(fraction))
	} else {
		aggSQL, ok := aggregate(q.Agg)
		if !ok {
			return 0, fmt.Errorf("unknown aggregation %q", q.Agg)
		}
		b.write(" SELECT " + aggSQL + " FROM scoped WHERE v IS NOT NULL")
	}
	var value sql.NullFloat64
	err = s.db.QueryRow(b.String(), b.args...).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return value.Float64, err
}

// runSpanTree resolves which trace the panel is about and returns it as rows,
// so a waterfall is a Frame like everything else. The heavy lifting is in
// Trace, which the traces page calls directly.
func (s *Store) runSpanTree(frame *Frame, q ViewQuery, now time.Time) error {
	traceID := q.TraceID
	if traceID == "" {
		var err error
		traceID, err = s.pickTrace(q, now)
		if err != nil {
			return err
		}
	}
	frame.Fields = []model.Field{{Name: "trace", Type: "string"}}
	if traceID == "" {
		frame.Notes = append(frame.Notes, "No trace matched this window.")
		return nil
	}
	trace, err := s.Trace(traceID)
	if err != nil {
		return err
	}
	frame.Rows = append(frame.Rows, []any{trace})
	if q.TraceID == "" {
		which := "most recent"
		if q.Order == "slowest" {
			which = "slowest"
		}
		frame.Notes = append(frame.Notes, "Showing the "+which+" matching trace.")
	}
	return nil
}

// pickTrace answers "the latest one" or "the slowest one" for a waterfall that
// was saved without a trace id — which is the useful way to save one, since the
// interesting trace is a different trace every hour.
func (s *Store) pickTrace(q ViewQuery, now time.Time) (string, error) {
	scopedQuery := q
	scopedQuery.Signal = "traces"
	where, args, err := whereClause(scopedQuery, now)
	if err != nil {
		return "", err
	}
	order := "MAX(timestamp_ns) DESC"
	if q.Order == "slowest" {
		order = "MAX(duration_ms) DESC"
	}
	statement := `SELECT trace_id FROM events` + where + ` AND trace_id <> '' GROUP BY trace_id ORDER BY ` + order + ` LIMIT 1`
	if where == "" {
		statement = `SELECT trace_id FROM events WHERE trace_id <> '' GROUP BY trace_id ORDER BY ` + order + ` LIMIT 1`
	}
	var traceID string
	err = s.db.QueryRow(statement, args...).Scan(&traceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return traceID, err
}

// DrillView returns the events behind one mark on a panel.
//
// This is the reverse of the compiler: the same WHERE clause the panel was
// drawn with, narrowed by the mark that was clicked. It has to be built from
// the query rather than from the frame, because a frame is a summary — the row
// "12:35, /checkout, 217" does not contain the 217 events, and asking the
// database again is the only way to get them.
func (s *Store) DrillView(panel string, q ViewQuery, selection model.Selection) (model.Drill, error) {
	if err := q.ValidateFor(panel); err != nil {
		return model.Drill{}, err
	}
	q = q.Normalize(panel)
	now := time.Now().UTC()

	where, args, err := whereClause(q, now)
	if err != nil {
		return model.Drill{}, err
	}
	clauses := []string{"1=1"}
	if where != "" {
		clauses = []string{strings.TrimPrefix(where, " WHERE ")}
	}

	if !selection.From.IsZero() {
		clauses = append(clauses, "timestamp_ns >= ?")
		args = append(args, selection.From.UnixNano())
	}
	if !selection.To.IsZero() {
		// The end of a bucket is the start of the next one, so it is excluded:
		// an event exactly on the boundary belongs to one bucket, not to both.
		clauses = append(clauses, "timestamp_ns < ?")
		args = append(args, selection.To.UnixNano())
	}
	if selection.HasSeries && q.GroupBy != "" {
		group, err := resolveField(q.GroupBy)
		if err != nil {
			return model.Drill{}, err
		}
		if selection.Series == "(none)" {
			clauses = append(clauses, "(("+group.sql+") IS NULL OR CAST(("+group.sql+") AS TEXT) = '')")
			args = append(args, group.args...)
			args = append(args, group.args...)
		} else {
			clauses = append(clauses, "CAST(("+group.sql+") AS TEXT) = ?")
			args = append(args, group.args...)
			args = append(args, selection.Series)
		}
	}
	if selection.Min != nil || selection.Max != nil {
		value, err := numericExpr(q.Value)
		if err != nil {
			return model.Drill{}, err
		}
		if selection.Min != nil {
			clauses = append(clauses, "("+value.sql+") >= ?")
			args = append(args, value.args...)
			args = append(args, *selection.Min)
		}
		if selection.Max != nil {
			clauses = append(clauses, "("+value.sql+") <= ?")
			args = append(args, value.args...)
			args = append(args, *selection.Max)
		}
	}
	filter := " WHERE " + strings.Join(clauses, " AND ")

	var drill model.Drill
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events`+filter, args...).Scan(&drill.Total); err != nil {
		return drill, err
	}
	limit := selection.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Ordered by the measured value when there is one: a bar you clicked
	// because it was tall is a bar whose slowest examples you want first.
	order := "timestamp_ns DESC, id DESC"
	if q.Value != "" && q.Agg != "count" {
		value, err := numericExpr(q.Value)
		if err != nil {
			return drill, err
		}
		order = "(" + value.sql + ") DESC, timestamp_ns DESC"
		args = append(args, value.args...)
	}
	rows, err := s.db.Query(`SELECT id,signal,timestamp_ns,received_at_ns,service,instance,scope,name,severity,message,trace_id,span_id,parent_span_id,kind,duration_ms,metric_value,unit,metric_type,attributes_json
FROM events`+filter+` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return drill, err
	}
	defer rows.Close()
	drill.Events, err = scanEvents(rows)
	return drill, err
}

// Trace assembles one trace into the flattened, depth-first order a waterfall
// draws top to bottom.
//
// The parent walk happens here rather than in the browser because it needs
// every span at once, and because "this span's parent was never exported" is a
// real and common state that has to be decided somewhere. Orphans are hoisted
// to the root instead of disappearing: a span you cannot see is worse than a
// span whose position you cannot fully trust.
func (s *Store) Trace(traceID string) (Trace, error) {
	trace := Trace{TraceID: traceID, Spans: []TraceSpan{}}
	if strings.TrimSpace(traceID) == "" {
		return trace, errors.New("trace id is required")
	}
	rows, err := s.db.Query(`SELECT id,span_id,parent_span_id,name,service,kind,severity,timestamp_ns,duration_ms
FROM events WHERE trace_id = ? AND signal = 'traces' ORDER BY timestamp_ns ASC, id ASC LIMIT 5000`, traceID)
	if err != nil {
		return trace, err
	}
	defer rows.Close()

	type node struct {
		span     TraceSpan
		children []int
	}
	var nodes []*node
	index := map[string]int{}
	services := map[string]bool{}
	for rows.Next() {
		var span TraceSpan
		var startNS int64
		if err := rows.Scan(&span.ID, &span.SpanID, &span.ParentSpanID, &span.Name, &span.Service, &span.Kind, &span.Severity, &startNS, &span.DurationMS); err != nil {
			return trace, err
		}
		span.Start = time.Unix(0, startNS).UTC()
		services[span.Service] = true
		if strings.Contains(strings.ToUpper(span.Severity), "ERROR") {
			trace.Errors++
		}
		if span.SpanID != "" {
			index[span.SpanID] = len(nodes)
		}
		nodes = append(nodes, &node{span: span})
	}
	if err := rows.Err(); err != nil {
		return trace, err
	}
	if len(nodes) == 0 {
		return trace, sql.ErrNoRows
	}

	var roots []int
	for i, n := range nodes {
		parent, ok := index[n.span.ParentSpanID]
		// A span that claims itself as its parent would loop forever below.
		if !ok || n.span.ParentSpanID == "" || parent == i {
			if n.span.ParentSpanID != "" {
				nodes[i].span.Orphan = true
			}
			roots = append(roots, i)
			continue
		}
		nodes[parent].children = append(nodes[parent].children, i)
	}

	start := nodes[0].span.Start
	end := start
	for _, n := range nodes {
		if n.span.Start.Before(start) {
			start = n.span.Start
		}
		if finish := n.span.Start.Add(time.Duration(n.span.DurationMS * float64(time.Millisecond))); finish.After(end) {
			end = finish
		}
	}
	trace.Start = start
	trace.DurationMS = float64(end.Sub(start)) / float64(time.Millisecond)

	// Iterative depth-first walk. A recursive one would be shorter, but a
	// malformed export with a deep chain would take the process down with it.
	visited := make([]bool, len(nodes))
	type frame struct{ index, depth int }
	stack := make([]frame, 0, len(nodes))
	for i := len(roots) - 1; i >= 0; i-- {
		stack = append(stack, frame{index: roots[i], depth: 0})
	}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[top.index] {
			continue
		}
		visited[top.index] = true
		span := nodes[top.index].span
		span.Depth = top.depth
		span.OffsetMS = float64(span.Start.Sub(start)) / float64(time.Millisecond)
		trace.Spans = append(trace.Spans, span)
		children := nodes[top.index].children
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, frame{index: children[i], depth: top.depth + 1})
		}
	}
	// A cycle among parent pointers would leave spans unvisited. Append them
	// flat rather than dropping them.
	for i, n := range nodes {
		if !visited[i] {
			span := n.span
			span.Orphan = true
			span.OffsetMS = float64(span.Start.Sub(start)) / float64(time.Millisecond)
			trace.Spans = append(trace.Spans, span)
		}
	}

	for service := range services {
		trace.Services = append(trace.Services, service)
	}
	sort.Strings(trace.Services)
	return trace, nil
}

// unitFor labels the axis. Duration is the one field whose unit guard knows
// without asking; a metric carries its own unit per row, which can differ
// within a single group, so that one is left to the panel's title.
func unitFor(q ViewQuery) string {
	if q.Value == "duration_ms" && q.Agg != "count" {
		return "ms"
	}
	return ""
}
