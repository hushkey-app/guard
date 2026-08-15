package telemetry

// The cluster: machines guard watches from the outside, and the record of what
// it saw.
//
// Storage only. The polling lives in internal/cluster, because making outbound
// HTTP requests is not this package's job and because a prober that could not
// be run without a database would be untestable.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type Node = model.Node
type Check = model.Check

// checkRetention is exactly the window the uptime figure reads, so nothing is
// stored that nothing can look at.
//
// It is a day rather than a week because the cadence is per node and defaults
// to three seconds: one node at that rate is 28,800 rows a day. A week of them,
// across twenty machines, would make this table the largest thing in the
// database — in exchange for a number no panel shows.
const checkRetention = 24 * time.Hour

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
CREATE INDEX IF NOT EXISTS idx_cluster_checks_node ON cluster_checks(node_id, checked_at_ns DESC);
CREATE TABLE IF NOT EXISTS cluster_assignments (
  service TEXT NOT NULL,
  instance TEXT NOT NULL,
  node_id INTEGER NOT NULL REFERENCES cluster_nodes(id) ON DELETE CASCADE,
  PRIMARY KEY(service, instance)
);
CREATE TABLE IF NOT EXISTS cluster_snapshots (
  snapshot_id TEXT PRIMARY KEY,
  node_id INTEGER NOT NULL REFERENCES cluster_nodes(id) ON DELETE CASCADE,
  description TEXT NOT NULL DEFAULT '',
  created_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cluster_snapshots_node ON cluster_snapshots(node_id);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate cluster: %w", err)
	}

	// The favicon, added after the table existed. Stored rather than linked:
	// guard's network can reach an internal box that the browser looking at
	// this dashboard often cannot, and an <img> pointing at http://vps-1/ from
	// an https page is blocked as mixed content before it is ever attempted.
	existing := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_xinfo('cluster_nodes')`)
	if err != nil {
		return fmt.Errorf("read cluster columns: %w", err)
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
	for column, definition := range map[string]string{
		"icon":             "BLOB",
		"icon_type":        "TEXT NOT NULL DEFAULT ''",
		"icon_fetched_ns":  "INTEGER NOT NULL DEFAULT 0",
		"interval_seconds": fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", model.DefaultIntervalSeconds),
		// The machine described in parts: the public name, the address guard
		// dials, and the path that follows the machine between the two.
		"domain":       "TEXT NOT NULL DEFAULT ''",
		"internal_url": "TEXT NOT NULL DEFAULT ''",
		"health_path":  "TEXT NOT NULL DEFAULT ''",
		// The way in. The password is a blob because it is ciphertext, and it
		// is ciphertext because a database file travels.
		"ssh_address":     "TEXT NOT NULL DEFAULT ''",
		"ssh_password":    "BLOB",
		"ssh_fingerprint": "TEXT NOT NULL DEFAULT ''",
		"locked":          "INTEGER NOT NULL DEFAULT 0",
		// The tags, as JSON in one column. A join table would buy ordering and
		// uniqueness guarantees for a list that is at most eight labels long
		// and is always read with its machine — and cost a second query on the
		// path the dashboard walks every three seconds.
		"tags": "TEXT NOT NULL DEFAULT '[]'",
		// Where the machine is. One value, free text, laid out by rather than
		// searched by — the difference between a group and a tag.
		"node_group": "TEXT NOT NULL DEFAULT ''",
		// How often to ask the machine itself, over SSH. Its own cadence,
		// because a sample costs a handshake where a health check costs a
		// request on a connection that was already open.
		"stats_interval_seconds": fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", model.DefaultStatsIntervalSeconds),
		// The link to a cloud account: which account, and which instance in it.
		// Two ids, because everything else about the instance is the provider's
		// to answer and would be stale here within the minute.
		"provider":             "TEXT NOT NULL DEFAULT ''",
		"provider_account_id":  "INTEGER NOT NULL DEFAULT 0",
		"provider_instance_id": "TEXT NOT NULL DEFAULT ''",
	} {
		if existing[column] {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE cluster_nodes ADD COLUMN %s %s`, column, definition)); err != nil {
			return fmt.Errorf("add %s: %w", column, err)
		}
	}

	// A short-lived second flag, folded back into the one lock it should always
	// have been. Left in the table because SQLite makes dropping a column a
	// table rewrite, and read by nothing.
	if existing["sealed"] {
		if _, err := db.Exec(`UPDATE cluster_nodes SET locked = 1 WHERE sealed = 1`); err != nil {
			return fmt.Errorf("fold sealed into locked: %w", err)
		}
	}

	// Nodes added before the split have one URL and no parts. Reading it back
	// into a domain and a path means an existing machine opens the new form
	// already filled in, rather than looking unconfigured.
	if !existing["domain"] {
		if err := backfillNodeParts(db); err != nil {
			return err
		}
	}

	const actions = `
CREATE TABLE IF NOT EXISTS cluster_actions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id INTEGER NOT NULL REFERENCES cluster_nodes(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  command TEXT NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  last_run_ns INTEGER NOT NULL DEFAULT 0,
  last_exit INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cluster_actions_node ON cluster_actions(node_id, position);
CREATE TABLE IF NOT EXISTS cluster_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  action_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  ran_at_ns INTEGER NOT NULL,
  duration_ms REAL NOT NULL DEFAULT 0,
  exit_code INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_cluster_runs_action ON cluster_runs(action_id, ran_at_ns DESC);
CREATE INDEX IF NOT EXISTS idx_cluster_runs_node ON cluster_runs(node_id, ran_at_ns DESC);`
	if _, err := db.Exec(actions); err != nil {
		return fmt.Errorf("migrate cluster actions: %w", err)
	}

	// The schedule, added after the commands existed. An action with one is run
	// by guard on a timer; every other thing about it — the machine, the login,
	// the audit line — is unchanged, which is why this is a handful of columns
	// rather than a job table.
	haveColumn := map[string]bool{}
	rows, err = db.Query(`SELECT name FROM pragma_table_xinfo('cluster_actions')`)
	if err != nil {
		return fmt.Errorf("read action columns: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		haveColumn[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for column, definition := range map[string]string{
		"schedule":      "TEXT NOT NULL DEFAULT ''",
		"stale_after_s": "INTEGER NOT NULL DEFAULT 0",
		// The last run that worked, kept apart from the last run: the staleness
		// watch reads this one, and a job failing on the dot every six hours
		// has a very recent last run.
		"last_ok_ns": "INTEGER NOT NULL DEFAULT 0",
		// When the staleness watch last said something, so a stale job is
		// reported and repeated occasionally rather than every wake-up.
		"alerted_ns": "INTEGER NOT NULL DEFAULT 0",
		"created_ns": "INTEGER NOT NULL DEFAULT 0",
		// When the expression was last written. An action that has never run
		// counts its first fire from here, and it has to be a fixed point:
		// counting from "now" would move the due time forward on every pass of
		// the loop, which is a job that is always about to run and never does.
		"schedule_from_ns": "INTEGER NOT NULL DEFAULT 0",
		// Where this job's staleness alert goes. Zero means the instance-wide
		// destination, which is what an upgrade finds every row set to.
		"webhook_id": "INTEGER NOT NULL DEFAULT 0",
	} {
		if haveColumn[column] {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE cluster_actions ADD COLUMN %s %s`, column, definition)); err != nil {
			return fmt.Errorf("add %s: %w", column, err)
		}
	}
	// Actions that predate the column: dated now, so the staleness watch has an
	// anchor for one that has never succeeded. Dating them zero would make
	// every existing action instantly stale the moment somebody set a
	// threshold on it.
	if !haveColumn["created_ns"] {
		if _, err := db.Exec(`UPDATE cluster_actions SET created_ns = ? WHERE created_ns = 0`, time.Now().UnixNano()); err != nil {
			return fmt.Errorf("date existing actions: %w", err)
		}
	}
	return nil
}

// backfillNodeParts splits each existing url into the domain and health path it
// was assembled from. Done in Go rather than SQL because the split is a URL
// parse, and a string of SQLite instr() calls that gets it nearly right would
// be worse than leaving the column empty.
func backfillNodeParts(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, url FROM cluster_nodes`)
	if err != nil {
		return err
	}
	type part struct {
		id           int64
		domain, path string
	}
	var parts []part
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Host == "" {
			continue
		}
		path := parsed.EscapedPath()
		if path == "/" {
			path = ""
		}
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
		parts = append(parts, part{id: id, domain: parsed.Scheme + "://" + parsed.Host, path: path})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range parts {
		if _, err := db.Exec(`UPDATE cluster_nodes SET domain = ?, health_path = ? WHERE id = ?`,
			p.domain, p.path, p.id); err != nil {
			return err
		}
	}
	return nil
}

// nodeColumns is the read shape of a node, in one place because three queries
// select it and a column added to two of them is a bug that only shows up on
// whichever page reads the third.
//
// The password is never selected — only whether there is one. A row the
// dashboard renders should not be able to carry a credential by accident.
const nodeColumns = `id,name,url,domain,internal_url,health_path,ssh_address,ssh_fingerprint,
LENGTH(COALESCE(ssh_password,'')) > 0,locked,enabled,interval_seconds,created_at_ns,updated_at_ns,
LENGTH(COALESCE(icon,'')) > 0,COALESCE(tags,'[]'),
COALESCE(provider,''),COALESCE(provider_account_id,0),COALESCE(provider_instance_id,''),
COALESCE(stats_interval_seconds,0),COALESCE(node_group,'')`

func scanNode(scan func(...any) error) (Node, error) {
	var node Node
	var created, updated int64
	var tags string
	err := scan(&node.ID, &node.Name, &node.URL, &node.Domain, &node.InternalURL, &node.HealthPath,
		&node.SSHAddress, &node.SSHFingerprint, &node.HasPassword, &node.Locked, &node.Enabled, &node.IntervalSeconds,
		&created, &updated, &node.HasIcon, &tags,
		&node.Provider, &node.ProviderAccountID, &node.ProviderInstanceID, &node.StatsIntervalSeconds,
		&node.Group)
	if err != nil {
		return node, err
	}
	// A row written before tags existed, or by hand, must not take the whole
	// cluster list down with it: an unreadable list is no tags, not an error.
	if tags != "" && tags != "[]" {
		if err := json.Unmarshal([]byte(tags), &node.Tags); err != nil {
			node.Tags = nil
		}
	}
	node.CreatedAt = time.Unix(0, created).UTC()
	node.UpdatedAt = time.Unix(0, updated).UTC()
	node.Status = model.StatusUnknown
	return node, nil
}

// Nodes returns every node with its latest check and a day of history.
//
// One query per concern rather than one clever join: the node list is small by
// nature — it is machines someone typed in — and three readable statements beat
// a correlated subquery that has to be re-derived every time someone adds a
// column.
func (s *Store) Nodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeColumns + ` FROM cluster_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]Node, 0)
	for rows.Next() {
		node, err := scanNode(rows.Scan)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// One query for every machine's actions rather than one per machine: the
	// dashboard asks for this list every three seconds, and a cluster of twenty
	// would otherwise be twenty round trips for two buttons each.
	actions, err := s.actionsByNode(0)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		nodes[i].Actions = actions[nodes[i].ID]
		if err := s.attachChecks(&nodes[i]); err != nil {
			return nil, err
		}
		if err := s.attachStats(&nodes[i]); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// NodesForProbe is what the scheduler reads: enough to decide whether a node is
// due, and nothing else.
//
// Nodes() runs three statements per node to assemble uptime and history, which
// is right for a dashboard drawing them and wrong for a loop that wakes several
// times a second to ask "anything to do". This is one statement for the whole
// cluster.
func (s *Store) NodesForProbe() ([]Node, error) {
	rows, err := s.db.Query(`SELECT n.id, n.name, n.url, n.enabled, n.interval_seconds,
COALESCE((SELECT MAX(checked_at_ns) FROM cluster_checks WHERE node_id = n.id), 0)
FROM cluster_nodes n WHERE n.enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := make([]Node, 0)
	for rows.Next() {
		var node Node
		var checked int64
		if err := rows.Scan(&node.ID, &node.Name, &node.URL, &node.Enabled, &node.IntervalSeconds, &checked); err != nil {
			return nil, err
		}
		if checked > 0 {
			node.CheckedAt = time.Unix(0, checked).UTC()
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) Node(id int64) (Node, error) {
	node, err := scanNode(s.db.QueryRow(`SELECT `+nodeColumns+` FROM cluster_nodes WHERE id = ?`, id).Scan)
	if err != nil {
		return node, err
	}
	actions, err := s.actionsByNode(id)
	if err != nil {
		return node, err
	}
	node.Actions = actions[id]
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
	node.Group = strings.TrimSpace(node.Group)
	node.URL = strings.TrimSpace(node.URL)
	node.Domain = strings.TrimSpace(node.Domain)
	node.InternalURL = strings.TrimSpace(node.InternalURL)
	node.HealthPath = strings.TrimSpace(node.HealthPath)
	node.SSHAddress = strings.TrimSpace(node.SSHAddress)
	node.Tags = normaliseTags(node.Tags)
	if err := node.Validate(); err != nil {
		return Node{}, err
	}
	tags, err := encodeTags(node.Tags)
	if err != nil {
		return Node{}, err
	}
	// The probed address is derived, never typed twice. Everything downstream —
	// the prober, the topology's host matching — reads this one column, so the
	// parts cannot drift away from what is actually being checked.
	node.URL = node.ProbeURL()
	if node.IntervalSeconds < model.MinIntervalSeconds {
		node.IntervalSeconds = model.DefaultIntervalSeconds
	}
	// Below the floor but not off is a mistake worth correcting rather than
	// refusing: zero means "do not sample", and everything else means "as
	// often as an SSH handshake can manage".
	if node.StatsIntervalSeconds < 0 {
		node.StatsIntervalSeconds = 0
	}
	if node.StatsIntervalSeconds > 0 && node.StatsIntervalSeconds < model.MinStatsIntervalSeconds {
		node.StatsIntervalSeconds = model.MinStatsIntervalSeconds
	}
	// A machine added or repointed changes which hosts are being watched, and
	// therefore what is grouped under what.
	s.topology.invalidate()
	now := time.Now().UTC().UnixNano()
	if node.ID == 0 {
		// A machine added without saying gets the default cadence, the same one
		// every machine that predates this feature got on migration. It costs
		// nothing until there is a login to sample with — and a new machine
		// silently sampling nothing while an old one reports would be a
		// difference nobody could see the cause of.
		if node.StatsIntervalSeconds == 0 {
			node.StatsIntervalSeconds = model.DefaultStatsIntervalSeconds
		}
		sealedPassword, err := s.seal(node.Password)
		if err != nil {
			return Node{}, err
		}
		// The link is accepted on insert and nowhere else in this function: it is
		// how a machine imported from a cloud account arrives already linked.
		// Editing it later goes through LinkNode, so an ordinary save — which
		// carries the whole node back from a form — cannot repoint or drop it by
		// leaving a field out.
		provider, accountID, instanceID := normaliseLink(node.Provider, node.ProviderAccountID, node.ProviderInstanceID)
		result, err := s.db.Exec(`INSERT INTO cluster_nodes
(name,url,domain,internal_url,health_path,ssh_address,ssh_password,locked,enabled,interval_seconds,tags,
stats_interval_seconds,provider,provider_account_id,provider_instance_id,node_group,created_at_ns,updated_at_ns)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			node.Name, node.URL, node.Domain, node.InternalURL, node.HealthPath, node.SSHAddress, sealedPassword,
			node.Locked, node.Enabled, node.IntervalSeconds, tags, node.StatsIntervalSeconds,
			provider, accountID, instanceID, node.Group, now, now)
		if err != nil {
			return Node{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return Node{}, err
		}
		if node.Actions != nil {
			if _, err := s.SaveActions(id, node.Actions); err != nil {
				return Node{}, err
			}
		}
		return s.Node(id)
	}

	// What the lock actually forbids, checked here rather than in the endpoint:
	// the store is the only thing every writer goes through, and a rule that
	// lives in a handler is a rule the next handler forgets.
	current, err := s.Node(node.ID)
	if err != nil {
		return Node{}, err
	}
	if current.Locked {
		if node.SSHAddress != current.SSHAddress {
			return Node{}, errors.New("this machine is locked: its ssh address cannot be changed")
		}
		if node.Password != nil {
			return Node{}, errors.New("this machine is locked: its password cannot be changed")
		}
		// It does not come off from the page that put it on, or from any other
		// caller. Deleting the machine is the only way past it, and that is not
		// a quiet act.
		node.Locked = true
	}

	result, err := s.db.Exec(`UPDATE cluster_nodes SET name=?,url=?,domain=?,internal_url=?,health_path=?,
ssh_address=?,locked=?,enabled=?,interval_seconds=?,stats_interval_seconds=?,tags=?,node_group=?,updated_at_ns=? WHERE id=?`,
		node.Name, node.URL, node.Domain, node.InternalURL, node.HealthPath, node.SSHAddress,
		node.Locked, node.Enabled, node.IntervalSeconds, node.StatsIntervalSeconds, tags, node.Group, now, node.ID)
	if err != nil {
		return Node{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Node{}, sql.ErrNoRows
	}
	// A machine repointed at a different address is, as far as the host key
	// goes, a different machine. Keeping the old pin would either refuse every
	// connection or — worse — accept one because the pin was never checked
	// against the box now answering.
	if _, err := s.db.Exec(`UPDATE cluster_nodes SET ssh_fingerprint = '' WHERE id = ? AND ssh_address <> ?`,
		node.ID, node.SSHAddress); err != nil {
		return Node{}, err
	}
	// The password is written only when the request said something about it.
	// An edit that renames a machine must not silently drop the login, and the
	// form that renames it has no password to send — it never received one.
	if node.Password != nil {
		sealed, err := s.seal(node.Password)
		if err != nil {
			return Node{}, err
		}
		if _, err := s.db.Exec(`UPDATE cluster_nodes SET ssh_password = ? WHERE id = ?`, sealed, node.ID); err != nil {
			return Node{}, err
		}
	}
	// Only when the list actually changed. Every edit to a machine carries its
	// actions along — the dashboard reads a node and writes it back — and
	// rewriting an unchanged list would make a rename fail on a locked machine
	// for a change nobody asked for.
	if node.Actions != nil && !sameActions(node.Actions, current.Actions) {
		if _, err := s.SaveActions(node.ID, node.Actions); err != nil {
			return Node{}, err
		}
	}
	return s.Node(node.ID)
}

// normaliseLink makes the three link columns agree with each other. Half a
// link is worse than none: an instance id with no account is a row that every
// provider call has to guess about.
func normaliseLink(provider string, accountID int64, instanceID string) (string, int64, string) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" || accountID <= 0 {
		return "", 0, ""
	}
	if strings.TrimSpace(provider) == "" {
		provider = model.ProviderVultr
	}
	return provider, accountID, instanceID
}

// LinkNode points one machine at one instance in one cloud account, or — with
// an empty link — forgets the pointing.
//
// Its own method rather than a field on the save, because a save is what the
// settings form does on every rename, and a link that could be dropped by a
// form that never knew about it is a link nobody can trust. Every provider
// endpoint reads the instance from here, so this is also the only place a
// caller can decide which box a power switch belongs to.
//
// A locked machine refuses. Locking is the statement that this machine's
// dangerous half is finished being configured, and a link is a new way to act
// on it — the switch, the snapshots, the rollback. Adding one afterwards would
// be exactly the quiet growth the lock exists to stop.
func (s *Store) LinkNode(nodeID int64, link model.ProviderLink) (Node, error) {
	node, err := s.Node(nodeID)
	if err != nil {
		return Node{}, err
	}
	if node.Locked {
		return Node{}, errors.New("this machine is locked: its cloud link cannot be changed")
	}
	provider, accountID, instanceID := normaliseLink(link.Provider, link.AccountID, link.InstanceID)
	if instanceID != "" {
		if err := (model.ProviderLink{AccountID: accountID, Provider: provider, InstanceID: instanceID}).Validate(); err != nil {
			return Node{}, err
		}
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM provider_accounts WHERE id = ?`, accountID).Scan(&exists); err != nil {
			return Node{}, err
		}
		if exists == 0 {
			return Node{}, errors.New("no such cloud account")
		}
	}
	if _, err := s.db.Exec(`UPDATE cluster_nodes SET provider=?,provider_account_id=?,provider_instance_id=?,updated_at_ns=?
WHERE id=?`, provider, accountID, instanceID, time.Now().UTC().UnixNano(), nodeID); err != nil {
		return Node{}, err
	}
	// An unlinked machine keeps no record of snapshots taken through a link
	// that no longer exists: the images are still the account's, and guard
	// claiming them for a machine it can no longer identify would be a lie
	// that survives longer than the link did.
	if instanceID == "" {
		if _, err := s.db.Exec(`DELETE FROM cluster_snapshots WHERE node_id = ?`, nodeID); err != nil {
			return Node{}, err
		}
	}
	return s.Node(nodeID)
}

// ProviderTargetFor resolves which instance, in which account, one machine
// points at — and it is the only way to find out.
//
// That is deliberate. Every provider endpoint takes a node id and asks this,
// exactly as every run takes an action id and reads its machine from the
// action: a caller cannot name an instance, so a caller cannot aim a power
// switch or a rollback at a box that is not the one on the row.
//
// destructive says whether the caller is about to change the machine rather
// than read it. A locked machine answers reads and refuses those, for the
// same reason it refuses new commands — locking is the statement that this
// machine's dangerous half is finished, and a rollback is as final as
// anything in the command list.
func (s *Store) ProviderTargetFor(nodeID int64, destructive bool) (model.ProviderLink, error) {
	node, err := s.Node(nodeID)
	if err != nil {
		return model.ProviderLink{}, err
	}
	if !node.Linked() {
		return model.ProviderLink{}, errors.New("this machine is not linked to a cloud account")
	}
	if destructive && node.Locked {
		return model.ProviderLink{}, errors.New("this machine is locked: it cannot be changed at the provider")
	}
	return model.ProviderLink{
		NodeID:     node.ID,
		AccountID:  node.ProviderAccountID,
		Provider:   node.Provider,
		InstanceID: node.ProviderInstanceID,
	}, nil
}

// NodeForInstance finds the machine already pointing at one instance, if
// there is one. The import list asks so it can grey what is already watched:
// two rows for one box is two health checks, two sets of commands and one
// argument about which is the real one.
func (s *Store) NodeForInstance(accountID int64, instanceID string) (int64, string, error) {
	var id int64
	var name string
	err := s.db.QueryRow(`SELECT id, name FROM cluster_nodes
WHERE provider_account_id = ? AND provider_instance_id = ?`, accountID, instanceID).Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return id, name, err
}

// RecordSnapshot remembers that one snapshot was taken of one machine.
//
// The provider's snapshot carries no instance — Vultr's object has a
// description and nothing else that could say where it came from — so this
// association exists only if guard writes it down. Only the association:
// the size, the status and whether the image still exists are read live, and
// a row here for a snapshot somebody deleted in the provider's console simply
// stops appearing.
func (s *Store) RecordSnapshot(nodeID int64, snapshotID, description string) error {
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return errors.New("a snapshot id is required")
	}
	_, err := s.db.Exec(`INSERT INTO cluster_snapshots (snapshot_id,node_id,description,created_at_ns)
VALUES(?,?,?,?) ON CONFLICT(snapshot_id) DO UPDATE SET node_id=excluded.node_id,description=excluded.description`,
		snapshotID, nodeID, strings.TrimSpace(description), time.Now().UTC().UnixNano())
	return err
}

// NodeSnapshots is the set of snapshot ids guard took of one machine.
func (s *Store) NodeSnapshots(nodeID int64) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT snapshot_id FROM cluster_snapshots WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ForgetSnapshot drops one association, after the image behind it is gone.
func (s *Store) ForgetSnapshot(snapshotID string) error {
	_, err := s.db.Exec(`DELETE FROM cluster_snapshots WHERE snapshot_id = ?`, snapshotID)
	return err
}

// sameActions reports whether two lists say the same thing: same commands, same
// names, same order. The run history is not part of the comparison — it is
// something that happened to an action rather than something about it.
func sameActions(a, b []model.NodeAction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID ||
			strings.TrimSpace(a[i].Name) != b[i].Name ||
			strings.TrimSpace(a[i].Command) != b[i].Command {
			return false
		}
	}
	return true
}

// normaliseTags trims the labels, fills in the default colour, and drops the
// empties and the duplicates. Done here rather than in the endpoint because
// the store is what every writer goes through: a tag list is small enough
// that tidying it is cheaper than explaining to somebody why "postgres " and
// "postgres" are two different chips.
func normaliseTags(tags []model.NodeTag) []model.NodeTag {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]model.NodeTag, 0, len(tags))
	for _, tag := range tags {
		tag.Label = strings.TrimSpace(tag.Label)
		if tag.Label == "" || seen[strings.ToLower(tag.Label)] {
			continue
		}
		if tag.Colour == "" {
			tag.Colour = model.TagColours[0]
		}
		seen[strings.ToLower(tag.Label)] = true
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func encodeTags(tags []model.NodeTag) (string, error) {
	if len(tags) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *Store) seal(password *string) ([]byte, error) {
	if password == nil {
		return nil, nil
	}
	return s.secrets.Seal(strings.TrimSpace(*password))
}

// SSHLogin is what the runner needs and nothing else: where to connect, as
// whom, with what, and which host key was pinned the first time.
type SSHLogin struct {
	User        string
	Address     string
	Password    string
	Fingerprint string
}

// SSHLoginFor decrypts one node's login.
//
// It is a separate call from Node on purpose. Everything else reads a node
// through a path that cannot return the password even by mistake; this is the
// one function that can, it is named for it, and it is called by exactly one
// thing.
func (s *Store) SSHLoginFor(nodeID int64) (SSHLogin, error) {
	var address, fingerprint string
	var sealed []byte
	err := s.db.QueryRow(`SELECT ssh_address, COALESCE(ssh_password, x''), ssh_fingerprint FROM cluster_nodes WHERE id = ?`, nodeID).
		Scan(&address, &sealed, &fingerprint)
	if err != nil {
		return SSHLogin{}, err
	}
	node := model.Node{SSHAddress: address}
	user, hostPort, ok := node.SSHDial()
	if !ok {
		return SSHLogin{}, errors.New("this machine has no ssh address")
	}
	password, err := s.secrets.Open(sealed)
	if err != nil {
		return SSHLogin{}, err
	}
	if password == "" {
		return SSHLogin{}, errors.New("this machine has no stored password")
	}
	return SSHLogin{User: user, Address: hostPort, Password: password, Fingerprint: fingerprint}, nil
}

// PinFingerprint records the host key guard saw the first time it connected.
// Every later connection is compared against it, which is the whole of guard's
// answer to "is this still the same machine".
func (s *Store) PinFingerprint(nodeID int64, fingerprint string) error {
	_, err := s.db.Exec(`UPDATE cluster_nodes SET ssh_fingerprint = ? WHERE id = ?`, fingerprint, nodeID)
	return err
}

// Actions returns one machine's commands, in the order they were arranged.
func (s *Store) Actions(nodeID int64) ([]model.NodeAction, error) {
	byNode, err := s.actionsByNode(nodeID)
	if err != nil {
		return nil, err
	}
	return byNode[nodeID], nil
}

// actionsByNode reads every action, or one node's, keyed by node. Zero means
// all of them — the shape the node list wants, in one statement.
func (s *Store) actionsByNode(nodeID int64) (map[int64][]model.NodeAction, error) {
	query := `SELECT ` + actionColumns + ` FROM cluster_actions`
	args := []any{}
	if nodeID != 0 {
		query += ` WHERE node_id = ?`
		args = append(args, nodeID)
	}
	query += ` ORDER BY position, id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]model.NodeAction{}
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out[action.NodeID] = append(out[action.NodeID], action)
	}
	return out, rows.Err()
}

// actionColumns is one list, read in three places, so a column added to the
// schedule cannot arrive in the settings page and go missing in the scheduler.
const actionColumns = `id,node_id,name,command,schedule,stale_after_s,webhook_id,last_run_ns,last_exit,last_error,last_ok_ns,alerted_ns,created_ns,schedule_from_ns`

func scanAction(row scanner) (model.NodeAction, error) {
	var action model.NodeAction
	var ran, ok, alerted, created, from int64
	err := row.Scan(&action.ID, &action.NodeID, &action.Name, &action.Command,
		&action.Schedule, &action.StaleAfterSeconds, &action.WebhookID,
		&ran, &action.LastExit, &action.LastError, &ok, &alerted, &created, &from)
	if err != nil {
		return model.NodeAction{}, err
	}
	// A zero timestamp is "never", and the year 1754 on a card is how a reader
	// learns to distrust every other date on the page.
	for _, pair := range []struct {
		ns   int64
		into *time.Time
	}{{ran, &action.LastRunAt}, {ok, &action.LastOKAt}, {alerted, &action.AlertedAt},
		{created, &action.CreatedAt}, {from, &action.ScheduleFrom}} {
		if pair.ns > 0 {
			*pair.into = time.Unix(0, pair.ns).UTC()
		}
	}
	action.NextRunAt = action.NextRun(time.Now())
	return action, nil
}

// SaveActions replaces one machine's list with the one that was sent.
//
// Matched by id rather than rewritten wholesale: an action keeps what happened
// the last time it ran, and a list that is saved every time a name is edited
// would otherwise forget it on every keystroke that reaches the server.
func (s *Store) SaveActions(nodeID int64, actions []model.NodeAction) ([]model.NodeAction, error) {
	node, err := s.Node(nodeID)
	if err != nil {
		return nil, err
	}
	for i := range actions {
		actions[i].Name = strings.TrimSpace(actions[i].Name)
		actions[i].Command = strings.TrimSpace(actions[i].Command)
		if err := actions[i].Validate(); err != nil {
			return nil, err
		}
	}
	// Locked: the list is closed. Not "edit carefully" but "there is nothing to
	// edit" — and, more to the point, nothing to add. An add is the whole
	// attack: one request, one new row, any command at all, sitting in the list
	// looking exactly as official as the ones somebody vetted.
	if node.Locked {
		return nil, errors.New("this machine is locked: its commands cannot be added to, edited or removed")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	keep := make([]any, 0, len(actions))
	for position, action := range actions {
		if action.ID > 0 {
			// The schedule's anchor moves only when the expression itself
			// changes: renaming a command must not push its next dump six
			// hours out, and a page that saves the whole list on every edit
			// would otherwise do exactly that.
			result, err := tx.Exec(`UPDATE cluster_actions SET name=?,command=?,position=?,stale_after_s=?,webhook_id=?,
schedule_from_ns = CASE WHEN schedule <> ? THEN ? ELSE schedule_from_ns END,
schedule = ? WHERE id=? AND node_id=?`,
				action.Name, action.Command, position, action.StaleAfterSeconds, action.WebhookID,
				strings.TrimSpace(action.Schedule), time.Now().UnixNano(),
				strings.TrimSpace(action.Schedule), action.ID, nodeID)
			if err != nil {
				return nil, err
			}
			if affected, _ := result.RowsAffected(); affected > 0 {
				keep = append(keep, action.ID)
				continue
			}
			// An id that belongs to another machine, or to nothing: treated as
			// new rather than refused, because the alternative is a settings
			// page that cannot be saved and does not say why.
		}
		now := time.Now().UnixNano()
		result, err := tx.Exec(`INSERT INTO cluster_actions(node_id,name,command,position,schedule,stale_after_s,webhook_id,created_ns,schedule_from_ns)
VALUES(?,?,?,?,?,?,?,?,?)`,
			nodeID, action.Name, action.Command, position, strings.TrimSpace(action.Schedule),
			action.StaleAfterSeconds, action.WebhookID, now, now)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		keep = append(keep, id)
	}

	// Whatever was not in the list was removed from it.
	query := `DELETE FROM cluster_actions WHERE node_id = ?`
	args := append([]any{nodeID}, keep...)
	if len(keep) > 0 {
		query += ` AND id NOT IN (?` + strings.Repeat(",?", len(keep)-1) + `)`
	}
	if _, err := tx.Exec(query, args...); err != nil {
		return nil, err
	}
	// The history of a command that no longer exists is rows nothing can read.
	if _, err := tx.Exec(`DELETE FROM cluster_runs WHERE node_id = ? AND action_id NOT IN (SELECT id FROM cluster_actions WHERE node_id = ?)`,
		nodeID, nodeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Actions(nodeID)
}

// Action reads one command, with the machine it belongs to. Both, because a
// request to run something names an action id, and trusting it to also name the
// right node would be trusting the caller to say which machine a command runs
// on.
func (s *Store) Action(id int64) (model.NodeAction, error) {
	return scanAction(s.db.QueryRow(`SELECT `+actionColumns+` FROM cluster_actions WHERE id = ?`, id))
}

// ScheduledActions is what the scheduler reads: every action carrying a
// schedule, on a machine that is not paused.
//
// Paused counts, because pausing a machine is what somebody does before
// working on it, and a box being rebuilt is the last one that should have a
// backup job opening SSH sessions into it. It is the same switch that stops
// the health checks, which is the point — one pause, one meaning.
func (s *Store) ScheduledActions() ([]model.NodeAction, error) {
	rows, err := s.db.Query(`SELECT a.id,a.node_id,a.name,a.command,a.schedule,a.stale_after_s,
a.webhook_id,a.last_run_ns,a.last_exit,a.last_error,a.last_ok_ns,a.alerted_ns,a.created_ns,a.schedule_from_ns
FROM cluster_actions a JOIN cluster_nodes n ON n.id = a.node_id
WHERE a.schedule <> '' AND n.enabled = 1 ORDER BY a.node_id, a.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeAction
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

// WatchedActions is what the staleness watch reads: every action somebody set
// a threshold on, whether or not it has a schedule.
//
// Without a schedule too, on purpose. A job run by CI, or by a person every
// morning, is exactly as capable of quietly stopping as one guard runs itself —
// and the watch is a separate loop from the scheduler for the same reason: a
// check that only runs as part of the job it checks never fires on the day the
// job does not.
func (s *Store) WatchedActions() ([]model.NodeAction, error) {
	rows, err := s.db.Query(`SELECT ` + actionColumns + ` FROM cluster_actions WHERE stale_after_s > 0 ORDER BY node_id, position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodeAction
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

// MarkAlerted records that the staleness watch has spoken about this action, so
// the next wake-up repeats it on a slow cadence instead of at every pass.
func (s *Store) MarkAlerted(actionID int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE cluster_actions SET alerted_ns=? WHERE id=?`, at.UnixNano(), actionID)
	return err
}

// runRetention is how much history one action keeps. Fifty rows is enough to
// see a pattern — every Sunday, or since Tuesday — and few enough that the
// output column cannot grow into the largest thing in the database.
const runRetention = 50

// runOutputKept is how much of a run's output the history keeps. The whole of
// it goes back to whoever pressed the button; what is stored is the tail, which
// is where a command that failed says why.
const runOutputKept = 8 << 10

// RecordRun remembers how an action ended: on the action, so its button can say
// so, and in the history, so a schedule nobody is watching can be read back.
//
// The last *successful* run is kept separately and is what the staleness watch
// reads. A success also clears the alert, so a job that comes back reports
// again the next time it goes away.
func (s *Store) RecordRun(actionID int64, run model.Run) error {
	failure := run.Error
	if failure == "" && run.ExitCode != 0 {
		failure = fmt.Sprintf("exit %d", run.ExitCode)
	}
	outcome := run.Outcome
	if outcome == "" {
		outcome = run.Result()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// A skipped run never happened, so it does not become the action's last
	// run: it is a row in the history and nothing else. Letting it stand as the
	// last run would also push the next fire a whole period away, which is the
	// opposite of what a job that is running long needs.
	if outcome != model.OutcomeSkipped {
		if _, err := tx.Exec(`UPDATE cluster_actions SET last_run_ns=?,last_exit=?,last_error=? WHERE id=?`,
			run.RanAt.UnixNano(), run.ExitCode, failure, actionID); err != nil {
			return err
		}
		if outcome == model.OutcomeOK {
			if _, err := tx.Exec(`UPDATE cluster_actions SET last_ok_ns=?,alerted_ns=0 WHERE id=?`,
				run.RanAt.UnixNano(), actionID); err != nil {
				return err
			}
		}
	}
	output := run.Output
	if len(output) > runOutputKept {
		output = output[len(output)-runOutputKept:]
	}
	nodeID := run.NodeID
	if nodeID == 0 {
		// The node is read from the action rather than taken from the caller,
		// the same rule the run endpoint follows.
		if err := tx.QueryRow(`SELECT node_id FROM cluster_actions WHERE id=?`, actionID).Scan(&nodeID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO cluster_runs(action_id,node_id,ran_at_ns,duration_ms,exit_code,outcome,trigger,error,output)
VALUES(?,?,?,?,?,?,?,?,?)`,
		actionID, nodeID, run.RanAt.UnixNano(), run.DurationMS, run.ExitCode, outcome, run.Trigger, failure, output); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_runs WHERE action_id = ? AND id NOT IN (
SELECT id FROM cluster_runs WHERE action_id = ? ORDER BY id DESC LIMIT ?)`, actionID, actionID, runRetention); err != nil {
		return err
	}
	return tx.Commit()
}

// Runs reads the history back, newest first: one action's, or a whole
// machine's when actionID is zero.
//
// A machine's, because that is the question somebody standing in front of a
// card actually has — "what has been running on this box" — and answering it
// per action would be one request per button.
func (s *Store) Runs(nodeID, actionID int64, limit int) ([]model.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT r.id,r.action_id,r.node_id,r.ran_at_ns,r.duration_ms,r.exit_code,r.outcome,r.trigger,r.error,r.output,
COALESCE(a.name,''),COALESCE(a.command,'')
FROM cluster_runs r LEFT JOIN cluster_actions a ON a.id = r.action_id WHERE `
	args := []any{}
	if actionID > 0 {
		query += `r.action_id = ?`
		args = append(args, actionID)
	} else {
		query += `r.node_id = ?`
		args = append(args, nodeID)
	}
	query += ` ORDER BY r.ran_at_ns DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Run{}
	for rows.Next() {
		var run model.Run
		var ranAt int64
		if err := rows.Scan(&run.ID, &run.ActionID, &run.NodeID, &ranAt, &run.DurationMS, &run.ExitCode,
			&run.Outcome, &run.Trigger, &run.Error, &run.Output, &run.ActionName, &run.Command); err != nil {
			return nil, err
		}
		run.RanAt = time.Unix(0, ranAt).UTC()
		out = append(out, run)
	}
	return out, rows.Err()
}

// DuplicateNode copies a machine's shape onto a new one: the address, the
// health path, the cadence, and the commands.
//
// What it deliberately does not copy is the login. A password proved against
// one box is not proof against another, and the whole point of checking a login
// before storing it is that every stored login worked at least once — a
// duplicate that inherited one would be the exception that makes the rule
// useless. It does not copy the lock or the seal either: those are statements
// about a machine that somebody finished configuring, and this one is not
// configured yet.
//
// The actions come across without their history. The copy has never run
// anything, and claiming otherwise would be inventing a fact about a machine.
func (s *Store) DuplicateNode(id int64) (Node, error) {
	source, err := s.Node(id)
	if err != nil {
		return Node{}, err
	}
	copied := Node{
		Name:        s.copyName(source.Name),
		Group:       source.Group,
		Domain:      source.Domain,
		InternalURL: source.InternalURL,
		HealthPath:  source.HealthPath,
		URL:         source.URL,
		// Paused, because a copy points at the machine it was copied from until
		// somebody changes the address — and two rows checking one box, one of
		// them by accident, is a way to be told the same thing twice.
		Enabled:         false,
		IntervalSeconds: source.IntervalSeconds,
		// Copied: a duplicate is nearly always the same kind of machine, and
		// re-typing "postgres" is the first thing anyone would do to it.
		Tags: source.Tags,
		// Not copied: the cloud link. Two rows pointing at one instance is two
		// power switches for one box, and the copy exists to become a different
		// machine — one of those switches would be aimed at the wrong one.
	}
	saved, err := s.SaveNode(copied)
	if err != nil {
		return Node{}, err
	}
	fresh := make([]model.NodeAction, 0, len(source.Actions))
	for _, action := range source.Actions {
		// The schedule and the alert come across with the command — a copy of
		// five boxes wants the same dump at the same hour — and the copy
		// arrives paused, so nothing fires until somebody has pointed it at the
		// machine it is actually for.
		fresh = append(fresh, model.NodeAction{
			Name:              action.Name,
			Command:           action.Command,
			Schedule:          action.Schedule,
			StaleAfterSeconds: action.StaleAfterSeconds,
		})
	}
	if len(fresh) > 0 {
		if _, err := s.SaveActions(saved.ID, fresh); err != nil {
			return Node{}, err
		}
	}
	return s.Node(saved.ID)
}

// copyName is "VPS-1 copy", then "VPS-1 copy 2" — a name somebody can tell
// apart from the original at a glance, and that does not collide on the third
// press of the button.
func (s *Store) copyName(name string) string {
	candidate := name + " copy"
	for suffix := 2; suffix < 100; suffix++ {
		var taken int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM cluster_nodes WHERE name = ?`, candidate).Scan(&taken); err != nil || taken == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s copy %d", name, suffix)
	}
	return candidate
}

// DeleteNode removes the node and its history. The checks are meaningless
// without the node they were of, and keeping them would leave the database
// growing rows nothing can ever read.
func (s *Store) DeleteNode(id int64) error {
	s.topology.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cluster_checks WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_actions WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_snapshots WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_runs WHERE node_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_stats WHERE node_id = ?`, id); err != nil {
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

// AssignInstance pins one instance to one machine by hand, or with a zero node
// releases it back to whatever the telemetry implies.
//
// Guessing from hosts covers the ordinary case and cannot cover all of them. A
// browser runs on nobody's machine; a service reached through a load balancer
// reports the balancer's host, and adding that balancer as a "machine to watch"
// to make the grouping come out right would put a thing on the dashboard that
// is not a thing anyone wants to watch. So the answer can also just be typed.
func (s *Store) AssignInstance(service, instance string, nodeID int64) error {
	service, instance = strings.TrimSpace(service), strings.TrimSpace(instance)
	if service == "" {
		return errors.New("service is required")
	}
	s.topology.invalidate()
	if nodeID == 0 {
		_, err := s.db.Exec(`DELETE FROM cluster_assignments WHERE service = ? AND instance = ?`, service, instance)
		return err
	}
	if _, err := s.Node(nodeID); err != nil {
		return fmt.Errorf("node %d: %w", nodeID, err)
	}
	_, err := s.db.Exec(`INSERT INTO cluster_assignments(service,instance,node_id) VALUES(?,?,?)
ON CONFLICT(service,instance) DO UPDATE SET node_id = excluded.node_id`, service, instance, nodeID)
	return err
}

// assignments is every hand-made placement, keyed the way instances are.
func (s *Store) assignments() (map[string]int64, error) {
	rows, err := s.db.Query(`SELECT service, instance, node_id FROM cluster_assignments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var service, instance string
		var nodeID int64
		if err := rows.Scan(&service, &instance, &nodeID); err != nil {
			return nil, err
		}
		out[service+"\x00"+instance] = nodeID
	}
	return out, rows.Err()
}

// iconRetry is how long to wait before asking a node for its favicon again.
// Most machines never have one, and the point of remembering the attempt is
// that "no icon" costs one request a day rather than one per check.
const iconRetry = 24 * time.Hour

// IconStale reports whether it is worth asking this node for its icon.
func (s *Store) IconStale(nodeID int64) (bool, error) {
	var fetched int64
	err := s.db.QueryRow(`SELECT icon_fetched_ns FROM cluster_nodes WHERE id = ?`, nodeID).Scan(&fetched)
	if err != nil {
		return false, err
	}
	return time.Since(time.Unix(0, fetched)) > iconRetry, nil
}

// SaveIcon stores what the prober found, including nothing. An empty body with
// the attempt recorded is the difference between "we looked and there is none"
// and "we have not looked", and only the second is worth retrying soon.
func (s *Store) SaveIcon(nodeID int64, icon []byte, contentType string) error {
	_, err := s.db.Exec(`UPDATE cluster_nodes SET icon = ?, icon_type = ?, icon_fetched_ns = ? WHERE id = ?`,
		icon, contentType, time.Now().UTC().UnixNano(), nodeID)
	return err
}

// Icon returns the stored bytes and their content type.
func (s *Store) Icon(nodeID int64) ([]byte, string, error) {
	var icon []byte
	var contentType string
	err := s.db.QueryRow(`SELECT icon, icon_type FROM cluster_nodes WHERE id = ?`, nodeID).Scan(&icon, &contentType)
	if err != nil {
		return nil, "", err
	}
	if len(icon) == 0 {
		return nil, "", sql.ErrNoRows
	}
	return icon, contentType, nil
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
