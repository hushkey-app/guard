package telemetry

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateHealthChecks(db *sql.DB) error {
	// The table boundary is the migration marker. A database upgrading from the
	// old machine-probe model has no health_checks table; one that has ever run
	// the dedicated Checks feature does. This must be read before CREATE TABLE:
	// re-running the legacy lift after somebody deletes a check resurrects it on
	// every restart.
	var healthChecksExisted bool
	if err := db.QueryRow(`SELECT EXISTS(
SELECT 1 FROM sqlite_master WHERE type='table' AND name='health_checks')`).Scan(&healthChecksExisted); err != nil {
		return fmt.Errorf("inspect health check migration state: %w", err)
	}

	const schema = `
CREATE TABLE IF NOT EXISTS health_checks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  interval_seconds INTEGER NOT NULL DEFAULT 3,
  public_name TEXT NOT NULL DEFAULT '',
  public INTEGER NOT NULL DEFAULT 0,
  node_id INTEGER REFERENCES cluster_nodes(id) ON DELETE SET NULL,
  machine_id INTEGER REFERENCES cluster_nodes(id) ON DELETE SET NULL,
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS health_check_results (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  check_id INTEGER NOT NULL REFERENCES health_checks(id) ON DELETE CASCADE,
  checked_at_ns INTEGER NOT NULL,
  ok INTEGER NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0,
  latency_ms REAL NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_health_check_results_check ON health_check_results(check_id, checked_at_ns DESC);
CREATE TABLE IF NOT EXISTS health_check_uptime_days (
  check_id INTEGER NOT NULL REFERENCES health_checks(id) ON DELETE CASCADE,
  day INTEGER NOT NULL,
  checks INTEGER NOT NULL DEFAULT 0,
  ok INTEGER NOT NULL DEFAULT 0,
  downtime_seconds INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(check_id, day)
);
CREATE TABLE IF NOT EXISTS health_check_incidents (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  check_id INTEGER NOT NULL REFERENCES health_checks(id) ON DELETE CASCADE,
  started_at_ns INTEGER NOT NULL,
  ended_at_ns INTEGER,
  comment TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL DEFAULT 'partial',
  allocated_minutes INTEGER NOT NULL DEFAULT 0,
  report_day INTEGER NOT NULL DEFAULT 0,
  confirmed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_health_check_incidents_check ON health_check_incidents(check_id, started_at_ns DESC);
CREATE TABLE IF NOT EXISTS health_check_incident_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id INTEGER NOT NULL REFERENCES health_check_incidents(id) ON DELETE CASCADE,
  checked_at_ns INTEGER NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate health checks: %w", err)
	}
	// Early builds used a unique node_id column for the compatibility lift. A
	// separate association column lets several service checks point at the same
	// machine without rebuilding installations that already have that schema.
	var hasMachineID bool
	rows, err := db.Query(`SELECT name FROM pragma_table_xinfo('health_checks')`)
	if err != nil {
		return fmt.Errorf("inspect health checks: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("inspect health checks: %w", err)
		}
		hasMachineID = hasMachineID || name == "machine_id"
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect health checks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect health checks: %w", err)
	}
	if !hasMachineID {
		if _, err := db.Exec(`ALTER TABLE health_checks ADD COLUMN machine_id INTEGER REFERENCES cluster_nodes(id) ON DELETE SET NULL`); err != nil {
			return fmt.Errorf("add health check machine association: %w", err)
		}
	}
	// These additive columns keep existing installations in place. Downtime is
	// accumulated only for intervals whose preceding probe was down; the next
	// probe closes the interval, including across a UTC midnight.
	for _, column := range []struct{ table, name, definition string }{
		{"health_check_uptime_days", "downtime_seconds", "INTEGER NOT NULL DEFAULT 0"},
		{"health_check_incidents", "severity", "TEXT NOT NULL DEFAULT 'partial'"},
		{"health_check_incidents", "allocated_minutes", "INTEGER NOT NULL DEFAULT 0"},
		{"health_check_incidents", "report_day", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_xinfo(?) WHERE name=?)`, column.table, column.name).Scan(&exists); err != nil {
			return fmt.Errorf("inspect %s.%s: %w", column.table, column.name, err)
		}
		if !exists {
			if _, err := db.Exec(`ALTER TABLE ` + column.table + ` ADD COLUMN ` + column.name + ` ` + column.definition); err != nil {
				return fmt.Errorf("add %s.%s: %w", column.table, column.name, err)
			}
		}
	}
	if _, err := db.Exec(`UPDATE health_check_incidents SET report_day=started_at_ns/86400000000000 WHERE report_day=0`); err != nil {
		return fmt.Errorf("date existing health incidents: %w", err)
	}

	// Lift legacy machine probes only while creating the dedicated table. Table
	// existence, rather than row existence, is intentional: an empty table may
	// mean the user deleted every check, and empty is a decision to preserve.
	if !healthChecksExisted {
		if _, err := db.Exec(`INSERT OR IGNORE INTO health_checks
(name,url,enabled,interval_seconds,public_name,public,node_id,created_at_ns,updated_at_ns)
SELECT name,url,enabled,interval_seconds,public_name,public,id,created_at_ns,updated_at_ns
FROM cluster_nodes WHERE TRIM(url) <> ''`); err != nil {
			return fmt.Errorf("lift machine probes: %w", err)
		}
	}
	if _, err := db.Exec(`UPDATE health_checks SET machine_id=node_id WHERE machine_id IS NULL AND node_id IS NOT NULL`); err != nil {
		return fmt.Errorf("copy health check machine associations: %w", err)
	}
	if !healthChecksExisted {
		if _, err := db.Exec(`INSERT INTO health_check_results(check_id,checked_at_ns,ok,status_code,latency_ms,error)
SELECT h.id,c.checked_at_ns,c.ok,c.status_code,c.latency_ms,c.error
FROM cluster_checks c JOIN health_checks h ON COALESCE(h.machine_id,h.node_id) = c.node_id
WHERE NOT EXISTS (SELECT 1 FROM health_check_results r WHERE r.check_id = h.id)`); err != nil {
			return fmt.Errorf("lift machine check history: %w", err)
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO health_check_uptime_days(check_id,day,checks,ok)
SELECT h.id,u.day,u.checks,u.ok FROM cluster_uptime_days u
JOIN health_checks h ON COALESCE(h.machine_id,h.node_id) = u.node_id`); err != nil {
			return fmt.Errorf("lift machine uptime: %w", err)
		}
	}
	return nil
}

const healthCheckColumns = `id,name,url,enabled,interval_seconds,public_name,public,COALESCE(machine_id,node_id,0),created_at_ns,updated_at_ns`

func scanHealthCheck(scan func(...any) error) (model.HealthCheck, error) {
	var check model.HealthCheck
	var created, updated int64
	err := scan(&check.ID, &check.Name, &check.URL, &check.Enabled, &check.IntervalSeconds,
		&check.PublicName, &check.Public, &check.NodeID, &created, &updated)
	if err != nil {
		return check, err
	}
	check.CreatedAt = time.Unix(0, created).UTC()
	check.UpdatedAt = time.Unix(0, updated).UTC()
	check.Status = model.StatusUnknown
	return check, nil
}

func (s *Store) HealthChecks() ([]model.HealthCheck, error) {
	rows, err := s.rdb.Query(`SELECT ` + healthCheckColumns + ` FROM health_checks ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	checks := make([]model.HealthCheck, 0)
	for rows.Next() {
		check, err := scanHealthCheck(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range checks {
		if err := s.attachHealthCheck(&checks[i]); err != nil {
			return nil, err
		}
	}
	return checks, nil
}

func (s *Store) HealthChecksForProbe() ([]model.HealthCheck, error) {
	rows, err := s.rdb.Query(`SELECT ` + healthCheckColumns + `,
COALESCE((SELECT MAX(checked_at_ns) FROM health_check_results r WHERE r.check_id=h.id),0)
FROM health_checks h WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := make([]model.HealthCheck, 0)
	for rows.Next() {
		var check model.HealthCheck
		var created, updated, checked int64
		if err := rows.Scan(&check.ID, &check.Name, &check.URL, &check.Enabled, &check.IntervalSeconds,
			&check.PublicName, &check.Public, &check.NodeID, &created, &updated, &checked); err != nil {
			return nil, err
		}
		check.CreatedAt = time.Unix(0, created).UTC()
		check.UpdatedAt = time.Unix(0, updated).UTC()
		if checked > 0 {
			check.CheckedAt = time.Unix(0, checked).UTC()
		}
		checks = append(checks, check)
	}
	return checks, rows.Err()
}

func (s *Store) HealthCheck(id int64) (model.HealthCheck, error) {
	check, err := scanHealthCheck(s.rdb.QueryRow(`SELECT `+healthCheckColumns+` FROM health_checks WHERE id=?`, id).Scan)
	if err != nil {
		return check, err
	}
	return check, s.attachHealthCheck(&check)
}

func (s *Store) attachHealthCheck(check *model.HealthCheck) error {
	var at sql.NullInt64
	var ok sql.NullBool
	var code sql.NullInt64
	var latency sql.NullFloat64
	var failure sql.NullString
	err := s.rdb.QueryRow(`SELECT checked_at_ns,ok,status_code,latency_ms,error FROM health_check_results
WHERE check_id=? ORDER BY checked_at_ns DESC LIMIT 1`, check.ID).Scan(&at, &ok, &code, &latency, &failure)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if at.Valid {
		check.CheckedAt = time.Unix(0, at.Int64).UTC()
		check.StatusCode = int(code.Int64)
		check.LatencyMS = latency.Float64
		check.Error = failure.String
		check.Status = model.StatusDown
		if ok.Bool {
			check.Status = model.StatusUp
		}
	}
	since := time.Now().UTC().Add(-24 * time.Hour).UnixNano()
	var total, up int
	if err := s.rdb.QueryRow(`SELECT COUNT(*),COALESCE(SUM(ok),0) FROM health_check_results
WHERE check_id=? AND checked_at_ns>=?`, check.ID, since).Scan(&total, &up); err != nil {
		return err
	}
	check.Checks = total
	if total > 0 {
		check.Uptime = float64(up) / float64(total) * 100
	}
	rows, err := s.rdb.Query(`SELECT ok FROM (SELECT ok,checked_at_ns FROM health_check_results
WHERE check_id=? ORDER BY checked_at_ns DESC LIMIT 60) ORDER BY checked_at_ns`, check.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ok bool
		if err := rows.Scan(&ok); err != nil {
			return err
		}
		check.History = append(check.History, boolToFloat(ok))
	}
	return rows.Err()
}

func (s *Store) SaveHealthCheck(check model.HealthCheck) (model.HealthCheck, error) {
	check.Name = strings.TrimSpace(check.Name)
	check.URL = strings.TrimSpace(check.URL)
	check.PublicName = strings.TrimSpace(check.PublicName)
	if err := check.Validate(); err != nil {
		return model.HealthCheck{}, err
	}
	if check.IntervalSeconds == 0 {
		check.IntervalSeconds = model.DefaultIntervalSeconds
	}
	now := time.Now().UTC().UnixNano()
	if check.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO health_checks
(name,url,enabled,interval_seconds,public_name,public,machine_id,created_at_ns,updated_at_ns)
VALUES(?,?,?,?,?,?,NULLIF(?,0),?,?)`, check.Name, check.URL, check.Enabled, check.IntervalSeconds,
			check.PublicName, check.Public, check.NodeID, now, now)
		if err != nil {
			return model.HealthCheck{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return model.HealthCheck{}, err
		}
		return s.HealthCheck(id)
	}
	result, err := s.db.Exec(`UPDATE health_checks SET name=?,url=?,enabled=?,interval_seconds=?,
public_name=?,public=?,machine_id=NULLIF(?,0),updated_at_ns=? WHERE id=?`, check.Name, check.URL,
		check.Enabled, check.IntervalSeconds, check.PublicName, check.Public, check.NodeID, now, check.ID)
	if err != nil {
		return model.HealthCheck{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.HealthCheck{}, sql.ErrNoRows
	}
	return s.HealthCheck(check.ID)
}

func (s *Store) DeleteHealthCheck(id int64) error {
	result, err := s.db.Exec(`DELETE FROM health_checks WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RecordHealthCheck(checkID int64, result model.Check) error {
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	var previousAt int64
	var previousOK bool
	previousErr := s.rdb.QueryRow(`SELECT checked_at_ns,ok FROM health_check_results
WHERE check_id=? ORDER BY checked_at_ns DESC LIMIT 1`, checkID).Scan(&previousAt, &previousOK)
	if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
		return previousErr
	}
	if !previousOK && previousAt > 0 && result.CheckedAt.UnixNano() > previousAt {
		if err := s.recordHealthCheckDowntime(checkID, time.Unix(0, previousAt), result.CheckedAt); err != nil {
			return err
		}
	}
	if result.OK {
		if !previousOK && previousAt > 0 {
			if _, err := s.db.Exec(`UPDATE health_check_incidents SET ended_at_ns=?
WHERE id=(SELECT id FROM health_check_incidents WHERE check_id=? AND ended_at_ns IS NULL ORDER BY started_at_ns DESC LIMIT 1)`, result.CheckedAt.UnixNano(), checkID); err != nil {
				return err
			}
		}
	} else {
		incidentStart := int64(0)
		if previousAt > 0 && !previousOK {
			incidentStart = previousAt
		}
		if err := s.recordHealthIncidentEvent(checkID, incidentStart, result); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`INSERT INTO health_check_results
(check_id,checked_at_ns,ok,status_code,latency_ms,error) VALUES(?,?,?,?,?,?)`, checkID,
		result.CheckedAt.UnixNano(), result.OK, result.StatusCode, result.LatencyMS, result.Error); err != nil {
		return err
	}
	success := 0
	if result.OK {
		success = 1
	}
	if _, err := s.db.Exec(`INSERT INTO health_check_uptime_days(check_id,day,checks,ok) VALUES(?,?,1,?)
ON CONFLICT(check_id,day) DO UPDATE SET checks=checks+1,ok=ok+?`, checkID, epochDay(result.CheckedAt), success, success); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM health_check_results WHERE check_id=? AND checked_at_ns<?`,
		checkID, result.CheckedAt.Add(-checkRetention).UnixNano()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM health_check_uptime_days WHERE day<?`, epochDay(time.Now())-uptimeDayRetention)
	return err
}

func (s *Store) recordHealthIncidentEvent(checkID, suggestedStart int64, result model.Check) error {
	var incidentID int64
	err := s.rdb.QueryRow(`SELECT id FROM health_check_incidents
WHERE check_id=? AND ended_at_ns IS NULL ORDER BY started_at_ns DESC LIMIT 1`, checkID).Scan(&incidentID)
	if errors.Is(err, sql.ErrNoRows) {
		started := result.CheckedAt.UnixNano()
		if suggestedStart > 0 {
			started = suggestedStart
		}
		created, createErr := s.db.Exec(`INSERT INTO health_check_incidents(check_id,started_at_ns,report_day) VALUES(?,?,?)`, checkID, started, epochDay(time.Unix(0, started)))
		if createErr != nil {
			return createErr
		}
		incidentID, err = created.LastInsertId()
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO health_check_incident_events(incident_id,checked_at_ns,status_code,error)
VALUES(?,?,?,?)`, incidentID, result.CheckedAt.UnixNano(), result.StatusCode, result.Error)
	return err
}

func (s *Store) HealthIncidents(checkID int64) ([]model.HealthIncident, error) {
	rows, err := s.rdb.Query(`SELECT id,check_id,started_at_ns,ended_at_ns,comment,severity,allocated_minutes,report_day,confirmed
FROM health_check_incidents WHERE check_id=? AND ended_at_ns IS NOT NULL ORDER BY started_at_ns DESC LIMIT 50`, checkID)
	if err != nil {
		return nil, err
	}
	incidents := make([]model.HealthIncident, 0)
	for rows.Next() {
		var incident model.HealthIncident
		var started, ended, day int64
		if err := rows.Scan(&incident.ID, &incident.CheckID, &started, &ended, &incident.Comment, &incident.Severity, &incident.AllocatedMinutes, &day, &incident.Confirmed); err != nil {
			return nil, err
		}
		incident.StartedAt = time.Unix(0, started).UTC()
		incident.EndedAt = time.Unix(0, ended).UTC()
		incident.DurationSeconds = int64(incident.EndedAt.Sub(incident.StartedAt) / time.Second)
		incident.Day = time.Unix(day*86400, 0).UTC().Format("2006-01-02")
		incidents = append(incidents, incident)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range incidents {
		events, err := s.rdb.Query(`SELECT checked_at_ns,status_code,error FROM health_check_incident_events WHERE incident_id=? ORDER BY checked_at_ns`, incidents[i].ID)
		if err != nil {
			return nil, err
		}
		for events.Next() {
			var event model.HealthIncidentEvent
			var checked int64
			if err := events.Scan(&checked, &event.StatusCode, &event.Error); err != nil {
				events.Close()
				return nil, err
			}
			event.CheckedAt = time.Unix(0, checked).UTC()
			incidents[i].Events = append(incidents[i].Events, event)
		}
		if err := events.Close(); err != nil {
			return nil, err
		}
	}
	return incidents, nil
}

func (s *Store) HealthIncidentBoard(checkID int64) (model.HealthIncidentBoard, error) {
	incidents, err := s.HealthIncidents(checkID)
	if err != nil {
		return model.HealthIncidentBoard{}, err
	}
	board := model.HealthIncidentBoard{Incidents: incidents, AvailableMinutes: make(map[string]int)}
	first := epochDay(time.Now().UTC()) - 2
	rows, err := s.rdb.Query(`SELECT day,downtime_seconds FROM health_check_uptime_days WHERE check_id=? AND day>=?`, checkID, first)
	if err != nil {
		return model.HealthIncidentBoard{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var day, seconds int64
		if err := rows.Scan(&day, &seconds); err != nil {
			return model.HealthIncidentBoard{}, err
		}
		board.AvailableMinutes[time.Unix(day*86400, 0).UTC().Format("2006-01-02")] = int((seconds + 59) / 60)
	}
	return board, rows.Err()
}

func (s *Store) SaveHealthIncident(id int64, report model.HealthIncidentReport) error {
	report.Comment = strings.TrimSpace(report.Comment)
	if err := report.Validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var checkID, day int64
	if err := tx.QueryRow(`SELECT check_id,report_day FROM health_check_incidents WHERE id=? AND ended_at_ns IS NOT NULL`, id).Scan(&checkID, &day); err != nil {
		return err
	}
	var available, allocated int
	if err := tx.QueryRow(`SELECT COALESCE((downtime_seconds+59)/60,0) FROM health_check_uptime_days WHERE check_id=? AND day=?`, checkID, day).Scan(&available); errors.Is(err, sql.ErrNoRows) {
		available = 0
	} else if err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COALESCE(SUM(allocated_minutes),0) FROM health_check_incidents WHERE check_id=? AND report_day=? AND id<>?`, checkID, day, id).Scan(&allocated); err != nil {
		return err
	}
	if allocated+report.AllocatedMinutes > available {
		return fmt.Errorf("only %d downtime minutes are available; %d are already assigned", available, allocated)
	}
	result, err := tx.Exec(`UPDATE health_check_incidents SET comment=?,severity=?,allocated_minutes=?,confirmed=? WHERE id=? AND ended_at_ns IS NOT NULL`, report.Comment, report.Severity, report.AllocatedMinutes, report.Confirmed, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) CreateHealthIncident(input model.HealthIncidentCreate) (model.HealthIncident, error) {
	if err := input.Validate(); err != nil {
		return model.HealthIncident{}, err
	}
	day, _ := time.Parse("2006-01-02", input.Day)
	at := day.Add(12 * time.Hour).UnixNano()
	result, err := s.db.Exec(`INSERT INTO health_check_incidents(check_id,started_at_ns,ended_at_ns,severity,report_day)
SELECT id,?,?,'partial',? FROM health_checks WHERE id=?`, at, at, epochDay(day), input.CheckID)
	if err != nil {
		return model.HealthIncident{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return model.HealthIncident{}, sql.ErrNoRows
	}
	id, err := result.LastInsertId()
	if err != nil || id == 0 {
		return model.HealthIncident{}, sql.ErrNoRows
	}
	incidents, err := s.HealthIncidents(input.CheckID)
	if err != nil {
		return model.HealthIncident{}, err
	}
	for _, incident := range incidents {
		if incident.ID == id {
			return incident, nil
		}
	}
	return model.HealthIncident{}, sql.ErrNoRows
}

func (s *Store) DeleteHealthIncident(id int64) error {
	result, err := s.db.Exec(`DELETE FROM health_check_incidents WHERE id=? AND confirmed=0
AND NOT EXISTS(SELECT 1 FROM health_check_incident_events WHERE incident_id=health_check_incidents.id)`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) recordHealthCheckDowntime(checkID int64, from, to time.Time) error {
	for cursor := from.UTC(); cursor.Before(to); {
		nextDay := time.Unix((epochDay(cursor)+1)*86400, 0).UTC()
		end := to.UTC()
		if nextDay.Before(end) {
			end = nextDay
		}
		seconds := int64(end.Sub(cursor) / time.Second)
		if seconds > 0 {
			if _, err := s.db.Exec(`INSERT INTO health_check_uptime_days(check_id,day,downtime_seconds)
VALUES(?,?,?) ON CONFLICT(check_id,day) DO UPDATE SET
downtime_seconds=downtime_seconds+excluded.downtime_seconds`, checkID, epochDay(cursor), seconds); err != nil {
				return err
			}
		}
		cursor = end
	}
	return nil
}
