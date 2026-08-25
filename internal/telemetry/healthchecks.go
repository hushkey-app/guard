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
  PRIMARY KEY(check_id, day)
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
