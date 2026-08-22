package telemetry

// What a machine says about itself: storage for the SSH samples.
//
// Storage only, the same split as the rest of the cluster — opening an SSH
// session lives in internal/remote, deciding when to lives in
// internal/cluster, and this is where the numbers land.
//
// A sample is small and frequent, so it is stored like a check: one row, an
// index on (node, time), and a day of them. What the dashboard reads is the
// latest one plus a short CPU history, both attached to the node it belongs
// to so a card is still one request.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// statsRetention matches the checks': exactly the window something can look
// at, and nothing stored that nothing reads. At the default minute cadence a
// machine writes 1,440 rows a day, which is a rounding error next to the
// events table.
const statsRetention = 24 * time.Hour

// cpuHistoryPoints is how many samples the sparkline draws. An hour at the
// default cadence — long enough to show a climb, short enough that the answer
// is still about now.
const cpuHistoryPoints = 60

func migrateHostStats(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cluster_stats (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id INTEGER NOT NULL REFERENCES cluster_nodes(id) ON DELETE CASCADE,
  at_ns INTEGER NOT NULL,
  cpu_percent REAL NOT NULL DEFAULT 0,
  has_cpu INTEGER NOT NULL DEFAULT 0,
  cpu_busy INTEGER NOT NULL DEFAULT 0,
  cpu_total INTEGER NOT NULL DEFAULT 0,
  cpu_count INTEGER NOT NULL DEFAULT 0,
  load1 REAL NOT NULL DEFAULT 0,
  load5 REAL NOT NULL DEFAULT 0,
  load15 REAL NOT NULL DEFAULT 0,
  mem_used_kb INTEGER NOT NULL DEFAULT 0,
  mem_total_kb INTEGER NOT NULL DEFAULT 0,
  disk_used_kb INTEGER NOT NULL DEFAULT 0,
  disk_total_kb INTEGER NOT NULL DEFAULT 0,
  uptime_seconds REAL NOT NULL DEFAULT 0,
  containers TEXT NOT NULL DEFAULT '[]',
  docker_error TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cluster_stats_node ON cluster_stats(node_id, at_ns DESC);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate cluster stats: %w", err)
	}
	return nil
}

const statsColumns = `at_ns,cpu_percent,has_cpu,cpu_busy,cpu_total,cpu_count,load1,load5,load15,
mem_used_kb,mem_total_kb,disk_used_kb,disk_total_kb,uptime_seconds,containers,docker_error,error`

func scanStats(scan func(...any) error) (model.HostStats, error) {
	var stats model.HostStats
	var at int64
	var containers string
	err := scan(&at, &stats.CPUPercent, &stats.HasCPU, &stats.CPUBusy, &stats.CPUTotal, &stats.CPUCount,
		&stats.Load1, &stats.Load5, &stats.Load15,
		&stats.MemUsedKB, &stats.MemTotalKB, &stats.DiskUsedKB, &stats.DiskTotalKB,
		&stats.UptimeSeconds, &containers, &stats.DockerError, &stats.Error)
	if err != nil {
		return stats, err
	}
	stats.At = time.Unix(0, at).UTC()
	// A row written by an older guard, or by hand, must not take the cluster
	// list down with it: an unreadable list is no containers, not an error.
	if containers != "" && containers != "[]" {
		if err := json.Unmarshal([]byte(containers), &stats.Containers); err != nil {
			stats.Containers = nil
		}
	}
	return stats, nil
}

// RecordStats stores one sample and trims the machine's history.
func (s *Store) RecordStats(nodeID int64, stats model.HostStats) error {
	if stats.At.IsZero() {
		stats.At = time.Now().UTC()
	}
	containers, err := json.Marshal(stats.Containers)
	if err != nil {
		containers = []byte("[]")
	}
	if _, err := s.db.Exec(`INSERT INTO cluster_stats
(node_id,`+statsColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nodeID, stats.At.UnixNano(), stats.CPUPercent, stats.HasCPU, stats.CPUBusy, stats.CPUTotal,
		stats.CPUCount, stats.Load1, stats.Load5, stats.Load15,
		stats.MemUsedKB, stats.MemTotalKB, stats.DiskUsedKB, stats.DiskTotalKB,
		stats.UptimeSeconds, string(containers), stats.DockerError, stats.Error); err != nil {
		return err
	}
	cutoff := time.Now().Add(-statsRetention).UnixNano()
	_, err = s.db.Exec(`DELETE FROM cluster_stats WHERE node_id = ? AND at_ns < ?`, nodeID, cutoff)
	return err
}

// LastStats is the most recent sample, whatever it said. The collector reads
// it to turn CPU counters into a percentage, so it must be the last *sample*
// and not the last successful one: a reading taken across a gap where the
// machine rebooted is exactly the one whose counters went backwards, which is
// a case the arithmetic already knows how to refuse.
func (s *Store) LastStats(nodeID int64) (model.HostStats, error) {
	return scanStats(s.rdb.QueryRow(`SELECT `+statsColumns+`
FROM cluster_stats WHERE node_id = ? ORDER BY at_ns DESC LIMIT 1`, nodeID).Scan)
}

// attachStats hangs the latest sample and the CPU sparkline off one node.
//
// Two small indexed reads per machine, on the path the dashboard walks every
// three seconds — the same bargain attachChecks makes, and for the same
// reason: a card that needed a second request per machine would be twenty
// requests for a page that is mostly green.
func (s *Store) attachStats(node *Node) error {
	if node.StatsIntervalSeconds == 0 {
		// Off, so there is nothing to show and nothing to ask for. Old samples
		// stay in the table until retention takes them.
		return nil
	}
	stats, err := s.LastStats(node.ID)
	switch {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return err
	}
	node.Stats = &stats

	rows, err := s.rdb.Query(`SELECT cpu_percent, has_cpu FROM cluster_stats
WHERE node_id = ? ORDER BY at_ns DESC LIMIT ?`, node.ID, cpuHistoryPoints)
	if err != nil {
		return err
	}
	defer rows.Close()
	history := []float64{}
	for rows.Next() {
		var percent float64
		var has bool
		if err := rows.Scan(&percent, &has); err != nil {
			return err
		}
		if !has {
			// A sample with no percentage is a gap, not a zero. Zero is a
			// machine doing nothing, which is a different picture.
			percent = -1
		}
		history = append(history, percent)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Read newest first for the LIMIT; drawn oldest first.
	for left, right := 0, len(history)-1; left < right; left, right = left+1, right-1 {
		history[left], history[right] = history[right], history[left]
	}
	node.CPUHistory = history
	return nil
}

// NodesForStats is the collector's read: the machines that are sampled, with
// enough of each to decide what is due and nothing more. No uptime, no
// history, no actions — this runs on a timer, not for a person.
func (s *Store) NodesForStats() ([]model.Node, error) {
	rows, err := s.rdb.Query(`SELECT n.id, n.name, n.enabled, n.stats_interval_seconds,
COALESCE((SELECT MAX(at_ns) FROM cluster_stats WHERE node_id = n.id), 0)
FROM cluster_nodes n
WHERE n.enabled = 1 AND n.stats_interval_seconds > 0 AND n.ssh_address <> ''
  AND LENGTH(COALESCE(n.ssh_password,'')) > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := []model.Node{}
	for rows.Next() {
		var node model.Node
		var last int64
		if err := rows.Scan(&node.ID, &node.Name, &node.Enabled, &node.StatsIntervalSeconds, &last); err != nil {
			return nil, err
		}
		if last > 0 {
			node.Stats = &model.HostStats{At: time.Unix(0, last).UTC()}
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}
