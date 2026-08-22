package telemetry

// The event destinations, and the rules that send to them.
//
// Storage only, the same split every other outward-facing thing in guard has:
// the POST lives in internal/notify and the evaluation in internal/cluster,
// because neither belongs in a package whose job is a database file.
//
// A destination's token is sealed exactly like an SSH password, and for the
// same reason: this database gets copied to laptops and attached to bug
// reports, and a webhook token is a credential to somebody's chat.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateNotify(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS webhooks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  header TEXT NOT NULL DEFAULT '',
  token BLOB,
  created_ns INTEGER NOT NULL,
  last_sent_ns INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS cluster_monitors (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  node_id INTEGER NOT NULL DEFAULT 0,
  metric TEXT NOT NULL,
  op TEXT NOT NULL DEFAULT 'above',
  threshold REAL NOT NULL DEFAULT 0,
  for_seconds INTEGER NOT NULL DEFAULT 0,
  webhook_id INTEGER NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_ns INTEGER NOT NULL
);
-- One row per (rule, machine): a rule over every machine is firing about one
-- of them at a time, and a single state column would make the second machine
-- to go bad invisible.
CREATE TABLE IF NOT EXISTS cluster_monitor_state (
  monitor_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  firing INTEGER NOT NULL DEFAULT 0,
  since_ns INTEGER NOT NULL DEFAULT 0,
  alerted_ns INTEGER NOT NULL DEFAULT 0,
  value REAL NOT NULL DEFAULT 0,
  PRIMARY KEY(monitor_id, node_id)
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate notify: %w", err)
	}
	return nil
}

// Webhooks lists the destinations, without their tokens.
func (s *Store) Webhooks() ([]model.Webhook, error) {
	rows, err := s.rdb.Query(`SELECT id,name,url,header,
token IS NOT NULL AND length(token) > 0, created_ns, last_sent_ns, last_error
FROM webhooks ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hooks := []model.Webhook{}
	for rows.Next() {
		var hook model.Webhook
		var created, sent int64
		if err := rows.Scan(&hook.ID, &hook.Name, &hook.URL, &hook.Header,
			&hook.HasToken, &created, &sent, &hook.LastError); err != nil {
			return nil, err
		}
		if created > 0 {
			hook.CreatedAt = time.Unix(0, created).UTC()
		}
		if sent > 0 {
			hook.LastSentAt = time.Unix(0, sent).UTC()
		}
		hooks = append(hooks, hook)
	}
	return hooks, rows.Err()
}

// SaveWebhook adds a destination or edits one.
//
// Editable in place, unlike a cloud account, because there is nothing here to
// prove: a webhook that answers today may be behind a queue that is down, and
// refusing to store it would be guard deciding when somebody's endpoint is
// allowed to exist. The token still follows the pointer rule — absent leaves
// it, "" forgets it, a value replaces it — so renaming a destination cannot
// silently drop its credential.
func (s *Store) SaveWebhook(hook model.Webhook) (model.Webhook, error) {
	hook.Name = strings.TrimSpace(hook.Name)
	hook.URL = strings.TrimSpace(hook.URL)
	hook.Header = strings.TrimSpace(hook.Header)
	if err := hook.Validate(); err != nil {
		return model.Webhook{}, err
	}
	sealed, err := s.seal(hook.Token)
	if err != nil {
		return model.Webhook{}, err
	}
	if hook.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO webhooks(name,url,header,token,created_ns) VALUES(?,?,?,?,?)`,
			hook.Name, hook.URL, hook.Header, sealed, time.Now().UnixNano())
		if err != nil {
			return model.Webhook{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return model.Webhook{}, err
		}
		return s.Webhook(id)
	}
	if hook.Token == nil {
		_, err = s.db.Exec(`UPDATE webhooks SET name=?,url=?,header=? WHERE id=?`,
			hook.Name, hook.URL, hook.Header, hook.ID)
	} else {
		_, err = s.db.Exec(`UPDATE webhooks SET name=?,url=?,header=?,token=? WHERE id=?`,
			hook.Name, hook.URL, hook.Header, sealed, hook.ID)
	}
	if err != nil {
		return model.Webhook{}, err
	}
	return s.Webhook(hook.ID)
}

// Webhook reads one, without its token.
func (s *Store) Webhook(id int64) (model.Webhook, error) {
	var hook model.Webhook
	var created, sent int64
	err := s.rdb.QueryRow(`SELECT id,name,url,header,
token IS NOT NULL AND length(token) > 0, created_ns, last_sent_ns, last_error
FROM webhooks WHERE id = ?`, id).
		Scan(&hook.ID, &hook.Name, &hook.URL, &hook.Header, &hook.HasToken, &created, &sent, &hook.LastError)
	if created > 0 {
		hook.CreatedAt = time.Unix(0, created).UTC()
	}
	if sent > 0 {
		hook.LastSentAt = time.Unix(0, sent).UTC()
	}
	return hook, err
}

// DeleteWebhook removes a destination and every rule pointing at it.
//
// The rules go too rather than being left aimed at nothing: a monitor with no
// destination is a rule that evaluates, decides something is wrong, and says
// it to no one — which is worse than not having the rule, because the page
// still lists it.
func (s *Store) DeleteWebhook(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM cluster_monitor_state WHERE monitor_id IN
(SELECT id FROM cluster_monitors WHERE webhook_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM cluster_monitors WHERE webhook_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// DestinationFor is the one call that returns a token, and it is named for it.
// Reached only by the loops that deliver events.
func (s *Store) DestinationFor(id int64) (notify.Destination, error) {
	var destination notify.Destination
	var sealed []byte
	err := s.rdb.QueryRow(`SELECT id,name,url,header,token FROM webhooks WHERE id = ?`, id).
		Scan(&destination.ID, &destination.Name, &destination.URL, &destination.Header, &sealed)
	if err != nil {
		return notify.Destination{}, err
	}
	if len(sealed) > 0 {
		token, err := s.secrets.Open(sealed)
		if err != nil {
			return notify.Destination{}, fmt.Errorf("the token for %q cannot be read with this key: %w", destination.Name, err)
		}
		destination.Token = token
	}
	return destination, nil
}

// RecordDelivery remembers how a destination last answered, so one that has
// been quietly refusing since Tuesday does not look like one nothing has
// happened on.
func (s *Store) RecordDelivery(id int64, at time.Time, failure string) error {
	_, err := s.db.Exec(`UPDATE webhooks SET last_sent_ns=?,last_error=? WHERE id=?`,
		at.UnixNano(), failure, id)
	return err
}

// Monitors lists the rules, each with where it stands.
func (s *Store) Monitors() ([]model.Monitor, error) {
	rows, err := s.rdb.Query(`SELECT m.id,m.node_id,COALESCE(n.name,''),m.metric,m.op,m.threshold,
m.for_seconds,m.webhook_id,m.enabled
FROM cluster_monitors m LEFT JOIN cluster_nodes n ON n.id = m.node_id
ORDER BY m.node_id, m.metric, m.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	monitors := []model.Monitor{}
	for rows.Next() {
		var monitor model.Monitor
		if err := rows.Scan(&monitor.ID, &monitor.NodeID, &monitor.NodeName, &monitor.Metric,
			&monitor.Op, &monitor.Threshold, &monitor.ForSeconds, &monitor.WebhookID, &monitor.Enabled); err != nil {
			return nil, err
		}
		monitors = append(monitors, monitor)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	states, err := s.monitorStates()
	if err != nil {
		return nil, err
	}
	for i := range monitors {
		monitors[i].States = states[monitors[i].ID]
	}
	return monitors, nil
}

func (s *Store) monitorStates() (map[int64][]model.MonitorState, error) {
	rows, err := s.rdb.Query(`SELECT s.monitor_id,s.node_id,COALESCE(n.name,''),s.firing,s.since_ns,s.alerted_ns,s.value
FROM cluster_monitor_state s LEFT JOIN cluster_nodes n ON n.id = s.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]model.MonitorState{}
	for rows.Next() {
		var id int64
		var state model.MonitorState
		var since, alerted int64
		if err := rows.Scan(&id, &state.NodeID, &state.NodeName, &state.Firing, &since, &alerted, &state.Value); err != nil {
			return nil, err
		}
		if since > 0 {
			state.Since = time.Unix(0, since).UTC()
		}
		if alerted > 0 {
			state.Alerted = time.Unix(0, alerted).UTC()
		}
		out[id] = append(out[id], state)
	}
	return out, rows.Err()
}

// SaveMonitor adds or edits one rule.
func (s *Store) SaveMonitor(monitor model.Monitor) (model.Monitor, error) {
	if err := monitor.Validate(); err != nil {
		return model.Monitor{}, err
	}
	if _, err := s.Webhook(monitor.WebhookID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Monitor{}, errors.New("that destination no longer exists")
		}
		return model.Monitor{}, err
	}
	if monitor.NodeID > 0 {
		if _, err := s.Node(monitor.NodeID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return model.Monitor{}, errors.New("that machine no longer exists")
			}
			return model.Monitor{}, err
		}
	}
	if monitor.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO cluster_monitors(node_id,metric,op,threshold,for_seconds,webhook_id,enabled,created_ns)
VALUES(?,?,?,?,?,?,?,?)`, monitor.NodeID, monitor.Metric, monitor.Op, monitor.Threshold,
			monitor.ForSeconds, monitor.WebhookID, monitor.Enabled, time.Now().UnixNano())
		if err != nil {
			return model.Monitor{}, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return model.Monitor{}, err
		}
		monitor.ID = id
		return monitor, nil
	}
	if _, err := s.db.Exec(`UPDATE cluster_monitors SET node_id=?,metric=?,op=?,threshold=?,for_seconds=?,webhook_id=?,enabled=? WHERE id=?`,
		monitor.NodeID, monitor.Metric, monitor.Op, monitor.Threshold, monitor.ForSeconds,
		monitor.WebhookID, monitor.Enabled, monitor.ID); err != nil {
		return model.Monitor{}, err
	}
	// An edited rule forgets that it has already spoken, but not that it is
	// firing. Dropping the whole judgement would close an incident silently —
	// whatever received the "firing" event would never be told it ended — so
	// only the "already told them" stamp goes, and the next pass either
	// re-fires against the new line or sends the resolved event that closes it.
	if _, err := s.db.Exec(`UPDATE cluster_monitor_state SET alerted_ns = 0 WHERE monitor_id = ?`, monitor.ID); err != nil {
		return model.Monitor{}, err
	}
	return monitor, nil
}

func (s *Store) DeleteMonitor(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM cluster_monitor_state WHERE monitor_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM cluster_monitors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ActiveMonitors is what the evaluator reads: the enabled rules only.
func (s *Store) ActiveMonitors() ([]model.Monitor, error) {
	monitors, err := s.Monitors()
	if err != nil {
		return nil, err
	}
	active := monitors[:0]
	for _, monitor := range monitors {
		if monitor.Enabled {
			active = append(active, monitor)
		}
	}
	return active, nil
}

// SaveMonitorState records where one rule stands against one machine, so the
// judgement survives a restart. A monitor that forgot it had been firing would
// alert again on every deploy, and one that forgot when a condition started
// would never reach a five-minute hold on a guard that restarts hourly.
func (s *Store) SaveMonitorState(monitorID int64, state model.MonitorState) error {
	_, err := s.db.Exec(`INSERT INTO cluster_monitor_state(monitor_id,node_id,firing,since_ns,alerted_ns,value)
VALUES(?,?,?,?,?,?)
ON CONFLICT(monitor_id,node_id) DO UPDATE SET firing=excluded.firing,since_ns=excluded.since_ns,
alerted_ns=excluded.alerted_ns,value=excluded.value`,
		monitorID, state.NodeID, state.Firing, unixOrZero(state.Since), unixOrZero(state.Alerted), state.Value)
	return err
}

// ClearMonitorState forgets a rule's opinion about a machine — used when the
// machine stops being measurable at all, so a box with its login removed does
// not sit "firing" forever.
func (s *Store) ClearMonitorState(monitorID, nodeID int64) error {
	_, err := s.db.Exec(`DELETE FROM cluster_monitor_state WHERE monitor_id = ? AND node_id = ?`, monitorID, nodeID)
	return err
}

func unixOrZero(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}
