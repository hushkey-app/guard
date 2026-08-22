package telemetry

// The environment guard keeps for a machine.
//
// A list of variables per node, sealed at rest with the same keeper the SSH
// passwords use — because a machine's environment is where its database password
// is, and this database gets copied to laptops and attached to bug reports.
//
// Guard is the source of truth here, unlike the cloud accounts where nothing the
// key unlocks is stored. The difference is which direction the data flows: a
// registry's tag list is the provider's answer to a question, and this is
// somebody's intent, typed once and pushed to the box. Saving it and putting it
// on the machine are two presses for exactly that reason.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateClusterEnv(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cluster_env (
  node_id INTEGER NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  key TEXT NOT NULL,
  value BLOB,
  PRIMARY KEY(node_id, key)
);
CREATE TABLE IF NOT EXISTS cluster_env_state (
  node_id INTEGER PRIMARY KEY,
  saved_ns INTEGER NOT NULL DEFAULT 0,
  injected_ns INTEGER NOT NULL DEFAULT 0,
  injected_count INTEGER NOT NULL DEFAULT 0
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate cluster env: %w", err)
	}
	return nil
}

// NodeEnv is one machine's variables, in the order they were saved.
//
// A value that will not decrypt is skipped rather than failing the read: the key
// changed or the row came from another instance, and a page that refused to load
// over one unreadable variable is a page nobody can fix.
func (s *Store) NodeEnv(nodeID int64) ([]model.NodeEnvVar, error) {
	rows, err := s.rdb.Query(`SELECT key, COALESCE(value, x'') FROM cluster_env
WHERE node_id = ? ORDER BY position, key`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	vars := []model.NodeEnvVar{}
	for rows.Next() {
		var key string
		var sealed []byte
		if err := rows.Scan(&key, &sealed); err != nil {
			return nil, err
		}
		value, err := s.secrets.Open(sealed)
		if err != nil {
			continue
		}
		vars = append(vars, model.NodeEnvVar{Key: key, Value: value})
	}
	return vars, rows.Err()
}

// SaveNodeEnv replaces one machine's variables with the set given.
//
// The whole set at once, because that is how the page edits it: a textarea of
// KEY=value lines is one thing somebody saves, and reconciling it row by row would
// be three endpoints arguing about what was deleted.
//
// A locked machine may still be edited here. Saving changes nothing on the box —
// the lock closes what can *reach* the machine, and that is the inject.
func (s *Store) SaveNodeEnv(nodeID int64, vars []model.NodeEnvVar) ([]model.NodeEnvVar, error) {
	if _, err := s.Node(nodeID); err != nil {
		return nil, err
	}
	for i := range vars {
		vars[i].Key = strings.TrimSpace(vars[i].Key)
	}
	if err := model.ValidateEnvVars(vars); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM cluster_env WHERE node_id = ?`, nodeID); err != nil {
		return nil, err
	}
	for position, entry := range vars {
		sealed, err := s.secrets.Seal(entry.Value)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO cluster_env(node_id,position,key,value) VALUES(?,?,?,?)`,
			nodeID, position, entry.Key, sealed); err != nil {
			return nil, err
		}
	}
	now := time.Now().UnixNano()
	if _, err := tx.Exec(`INSERT INTO cluster_env_state(node_id,saved_ns) VALUES(?,?)
ON CONFLICT(node_id) DO UPDATE SET saved_ns=excluded.saved_ns`, nodeID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.NodeEnv(nodeID)
}

// EnvTarget is what the inject endpoint needs: the machine's variables and the
// login to reach it with.
type EnvTarget struct {
	NodeID int64
	Name   string
	Vars   []model.NodeEnvVar
	Login  SSHLogin
}

// EnvTargetFor reads a machine's environment and its login, and applies the lock.
//
// The lock refuses here and not on the save: putting variables on a machine is
// reaching into it, which is the thing a locked machine does not allow.
func (s *Store) EnvTargetFor(nodeID int64) (EnvTarget, error) {
	node, err := s.Node(nodeID)
	if err != nil {
		return EnvTarget{}, err
	}
	if node.Locked {
		return EnvTarget{}, errors.New("this machine is locked: nothing can be written to it from here")
	}
	vars, err := s.NodeEnv(nodeID)
	if err != nil {
		return EnvTarget{}, err
	}
	if len(vars) == 0 {
		return EnvTarget{}, errors.New("there is nothing to inject — save some variables first")
	}
	login, err := s.SSHLoginFor(nodeID)
	if err != nil {
		return EnvTarget{}, err
	}
	return EnvTarget{NodeID: nodeID, Name: node.Name, Vars: vars, Login: login}, nil
}

// EnvInjected records that the machine has them now. A failure to note it is not
// a failure to inject — the files are already there.
func (s *Store) EnvInjected(nodeID int64) error {
	_, err := s.db.Exec(`INSERT INTO cluster_env_state(node_id,injected_ns,injected_count) VALUES(?,?,1)
ON CONFLICT(node_id) DO UPDATE SET injected_ns=excluded.injected_ns, injected_count=injected_count+1`,
		nodeID, time.Now().UnixNano())
	return err
}

// envStateByNode is when each machine's environment was last saved and last put
// on the box — the two dates the page needs to say "saved, not injected yet".
func (s *Store) envStateByNode() (map[int64]model.NodeEnvState, error) {
	rows, err := s.rdb.Query(`SELECT node_id, saved_ns, injected_ns FROM cluster_env_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[int64]model.NodeEnvState{}
	for rows.Next() {
		var nodeID, saved, injected int64
		if err := rows.Scan(&nodeID, &saved, &injected); err != nil {
			return nil, err
		}
		state := model.NodeEnvState{}
		if saved > 0 {
			state.SavedAt = time.Unix(0, saved).UTC()
		}
		if injected > 0 {
			state.InjectedAt = time.Unix(0, injected).UTC()
		}
		states[nodeID] = state
	}
	return states, rows.Err()
}

// envCountByNode is how many variables each machine has, for the list.
func (s *Store) envCountByNode() (map[int64]int, error) {
	rows, err := s.rdb.Query(`SELECT node_id, COUNT(*) FROM cluster_env GROUP BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var nodeID int64
		var count int
		if err := rows.Scan(&nodeID, &count); err != nil {
			return nil, err
		}
		counts[nodeID] = count
	}
	return counts, rows.Err()
}
