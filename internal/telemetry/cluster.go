package telemetry

// The cluster: machines guard watches from the outside, and the record of what
// it saw.
//
// Storage only. The polling lives in internal/cluster, because making outbound
// HTTP requests is not this package's job and because a prober that could not
// be run without a database would be untestable.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

type Node = model.Node
type Check = model.Check

// checkRetention is how far back the uptime figure can see. Longer would make
// the checks table the largest thing in the database on an instance watching
// twenty nodes every fifteen seconds, in exchange for a number nobody reads.
const checkRetention = 7 * 24 * time.Hour

func migrateCluster(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cluster_nodes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cluster_checks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id INTEGER NOT NULL REFERENCES cluster_nodes(id) ON DELETE CASCADE,
  checked_at_ns INTEGER NOT NULL,
  ok INTEGER NOT NULL,
  status_code INTEGER NOT NULL DEFAULT 0,
  latency_ms REAL NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cluster_checks_node ON cluster_checks(node_id, checked_at_ns DESC);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate cluster: %w", err)
	}
	return nil
}

// Nodes returns every node with its latest check and a day of history.
//
// One query per concern rather than one clever join: the node list is small by
// nature — it is machines someone typed in — and three readable statements beat
// a correlated subquery that has to be re-derived every time someone adds a
// column.
func (s *Store) Nodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT id,name,url,enabled,created_at_ns,updated_at_ns FROM cluster_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]Node, 0)
	for rows.Next() {
		var node Node
		var created, updated int64
		if err := rows.Scan(&node.ID, &node.Name, &node.URL, &node.Enabled, &created, &updated); err != nil {
			return nil, err
		}
		node.CreatedAt = time.Unix(0, created).UTC()
		node.UpdatedAt = time.Unix(0, updated).UTC()
		node.Status = model.StatusUnknown
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range nodes {
		if err := s.attachChecks(&nodes[i]); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

func (s *Store) Node(id int64) (Node, error) {
	var node Node
	var created, updated int64
	err := s.db.QueryRow(`SELECT id,name,url,enabled,created_at_ns,updated_at_ns FROM cluster_nodes WHERE id = ?`, id).
		Scan(&node.ID, &node.Name, &node.URL, &node.Enabled, &created, &updated)
	if err != nil {
		return node, err
	}
	node.CreatedAt = time.Unix(0, created).UTC()
	node.UpdatedAt = time.Unix(0, updated).UTC()
	node.Status = model.StatusUnknown
	return node, s.attachChecks(&node)
}

func (s *Store) attachChecks(node *Node) error {
	var checkedAt sql.NullInt64
	var ok sql.NullBool
	var code sql.NullInt64
	var latency sql.NullFloat64
	var failure sql.NullString
	err := s.db.QueryRow(`SELECT checked_at_ns, ok, status_code, latency_ms, error FROM cluster_checks
WHERE node_id = ? ORDER BY checked_at_ns DESC LIMIT 1`, node.ID).Scan(&checkedAt, &ok, &code, &latency, &failure)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if checkedAt.Valid {
		node.CheckedAt = time.Unix(0, checkedAt.Int64).UTC()
		node.StatusCode = int(code.Int64)
		node.LatencyMS = latency.Float64
		node.Error = failure.String
		node.Status = model.StatusDown
		if ok.Bool {
			node.Status = model.StatusUp
		}
	}

	since := time.Now().UTC().Add(-24 * time.Hour).UnixNano()
	var total, up int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ok),0) FROM cluster_checks WHERE node_id = ? AND checked_at_ns >= ?`,
		node.ID, since).Scan(&total, &up); err != nil {
		return err
	}
	node.Checks = total
	if total > 0 {
		node.Uptime = float64(up) / float64(total) * 100
	}

	history, err := s.db.Query(`SELECT ok FROM (SELECT ok, checked_at_ns FROM cluster_checks
WHERE node_id = ? ORDER BY checked_at_ns DESC LIMIT 60) ORDER BY checked_at_ns ASC`, node.ID)
	if err != nil {
		return err
	}
	defer history.Close()
	for history.Next() {
		var up bool
		if err := history.Scan(&up); err != nil {
			return err
		}
		node.History = append(node.History, boolToFloat(up))
	}
	return history.Err()
}

func boolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (s *Store) SaveNode(node Node) (Node, error) {
	node.Name = strings.TrimSpace(node.Name)
	node.URL = strings.TrimSpace(node.URL)
	if err := node.Validate(); err != nil {
		return Node{}, err
	}
	now := time.Now().UTC().UnixNano()
	if node.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO cluster_nodes(name,url,enabled,created_at_ns,updated_at_ns) VALUES(?,?,?,?,?)`,
			node.Name, node.URL, node.Enabled, now, now)
		if err != nil {
			return Node{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return Node{}, err
		}
		return s.Node(id)
	}
	result, err := s.db.Exec(`UPDATE cluster_nodes SET name=?,url=?,enabled=?,updated_at_ns=? WHERE id=?`,
		node.Name, node.URL, node.Enabled, now, node.ID)
	if err != nil {
		return Node{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Node{}, sql.ErrNoRows
	}
	return s.Node(node.ID)
}

// DeleteNode removes the node and its history. The checks are meaningless
// without the node they were of, and keeping them would leave the database
// growing rows nothing can ever read.
func (s *Store) DeleteNode(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cluster_checks WHERE node_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM cluster_nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// RecordCheck stores one probe result and trims the node's history.
func (s *Store) RecordCheck(nodeID int64, check Check) error {
	if check.CheckedAt.IsZero() {
		check.CheckedAt = time.Now().UTC()
	}
	if _, err := s.db.Exec(`INSERT INTO cluster_checks(node_id,checked_at_ns,ok,status_code,latency_ms,error)
VALUES(?,?,?,?,?,?)`, nodeID, check.CheckedAt.UnixNano(), check.OK, check.StatusCode, check.LatencyMS, check.Error); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM cluster_checks WHERE node_id = ? AND checked_at_ns < ?`,
		nodeID, time.Now().UTC().Add(-checkRetention).UnixNano())
	return err
}

// ClusterSummary is what the overview reads: the shape of the cluster in one
// line, without pulling every node's history across to count them.
func (s *Store) ClusterSummary() (model.ClusterSummary, error) {
	nodes, err := s.Nodes()
	if err != nil {
		return model.ClusterSummary{}, err
	}
	summary := model.ClusterSummary{Nodes: len(nodes)}
	for _, node := range nodes {
		switch node.Status {
		case model.StatusUp:
			summary.Up++
		case model.StatusDown:
			summary.Down++
			// The first name is enough: a header that lists every failing node
			// is a header nobody finishes reading.
			if summary.Worst == "" {
				summary.Worst = node.Name
			}
		default:
			summary.Unknown++
		}
	}
	return summary, nil
}
