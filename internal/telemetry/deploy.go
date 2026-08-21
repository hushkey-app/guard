package telemetry

// Deploys: the groups, the versioned templates, the runs and what each machine
// is running.
//
// Three things here are load-bearing and are in the store rather than in a
// handler, which is the same rule the cluster's lock keeps:
//
//   - **A locked machine refuses a deploy.** A deploy writes files and runs
//     docker over SSH; it is the command line wearing a template's name. If the
//     lock did not reach it, locking a machine would mean "its stored commands
//     are frozen and anything else may still happen to it", which is not a lock.
//     `DeployTarget` is the only way to a login here, and it applies it.
//   - **A template version is never overwritten.** Saving writes the next
//     version; the row a run points at stays exactly as it was deployed.
//   - **State is written only on healthy.** `current_tag` and
//     `last_known_good_tag` move together, on the way out of a passed health
//     gate and nowhere else, so the tag rollback offers is always one that
//     answered.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateDeploy(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS deploy_groups (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  webhook_id INTEGER NOT NULL DEFAULT 0,
  created_ns INTEGER NOT NULL DEFAULT 0,
  updated_ns INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS deploy_groups_name ON deploy_groups(name);
CREATE TABLE IF NOT EXISTS deploy_group_nodes (
  group_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  position INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(group_id, node_id)
);
CREATE TABLE IF NOT EXISTS deploy_templates (
  id INTEGER NOT NULL,
  version INTEGER NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  service_name TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  compose_yaml TEXT NOT NULL DEFAULT '',
  health_path TEXT NOT NULL DEFAULT '',
  health_port INTEGER NOT NULL DEFAULT 0,
  secret_env_id INTEGER NOT NULL DEFAULT 0,
  vars TEXT NOT NULL DEFAULT '[]',
  created_ns INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(id, version)
);
CREATE TABLE IF NOT EXISTS deploy_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  group_id INTEGER NOT NULL DEFAULT 0,
  group_name TEXT NOT NULL DEFAULT '',
  template_id INTEGER NOT NULL DEFAULT 0,
  template_version INTEGER NOT NULL DEFAULT 0,
  template_name TEXT NOT NULL DEFAULT '',
  service_name TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT '',
  tag TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  rollback INTEGER NOT NULL DEFAULT 0,
  started_ns INTEGER NOT NULL DEFAULT 0,
  finished_ns INTEGER NOT NULL DEFAULT 0,
  awaiting_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS deploy_runs_started ON deploy_runs(started_ns DESC);
CREATE TABLE IF NOT EXISTS deploy_run_instances (
  run_id INTEGER NOT NULL,
  node_id INTEGER NOT NULL,
  node_name TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',
  started_ns INTEGER NOT NULL DEFAULT 0,
  finished_ns INTEGER NOT NULL DEFAULT 0,
  previous_tag TEXT NOT NULL DEFAULT '',
  health TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  output TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(run_id, node_id)
);
CREATE TABLE IF NOT EXISTS deploy_state (
  node_id INTEGER NOT NULL,
  service_name TEXT NOT NULL,
  current_tag TEXT NOT NULL DEFAULT '',
  last_good_tag TEXT NOT NULL DEFAULT '',
  template_id INTEGER NOT NULL DEFAULT 0,
  updated_ns INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(node_id, service_name)
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate deploys: %w", err)
	}
	// A column added after a table existed. `CREATE TABLE IF NOT EXISTS` is
	// silent about a table it did not create, so an instance that ran an
	// earlier build keeps the old shape and every read of it fails — which is
	// the same reason the cluster's migration does this rather than trusting
	// its CREATE.
	for table, columns := range map[string]map[string]string{
		"deploy_groups": {"webhook_id": "INTEGER NOT NULL DEFAULT 0"},
		"deploy_state": {
			"current_version":   "INTEGER NOT NULL DEFAULT 0",
			"last_good_version": "INTEGER NOT NULL DEFAULT 0",
		},
	} {
		existing, err := tableColumns(db, table)
		if err != nil {
			return fmt.Errorf("read %s columns: %w", table, err)
		}
		for column, definition := range columns {
			if contains(existing, column) {
				continue
			}
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %s %s`, table, column, definition)); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, column, err)
			}
		}
	}
	// The version each machine is running, for rows written before it was
	// recorded. Read out of the run history rather than left at zero, because a
	// zero would show up on the next rollback button as "v0" — a version that
	// never existed. Only where it is still unknown, so this is a one-time
	// backfill that costs nothing on every later start.
	if _, err := db.Exec(`UPDATE deploy_state SET current_version = COALESCE((
SELECT r.template_version FROM deploy_runs r
JOIN deploy_run_instances i ON i.run_id = r.id
WHERE i.node_id = deploy_state.node_id AND r.service_name = deploy_state.service_name
  AND i.status = 'healthy' ORDER BY r.id DESC LIMIT 1), 0)
WHERE current_version = 0`); err != nil {
		return fmt.Errorf("backfill deploy versions: %w", err)
	}
	return nil
}

// ErrDeployLocked is a deploy aimed at a locked machine. Its own error because
// it is the one refusal here with something for somebody to do about it, and
// because the API turns it into a 409 rather than a 400.
var ErrDeployLocked = errors.New("this machine is locked: nothing can be deployed to it")

// DeployGroups is every group, with its machines.
func (s *Store) DeployGroups() ([]model.DeployGroup, error) {
	rows, err := s.db.Query(`SELECT id, name, webhook_id, created_ns, updated_ns FROM deploy_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []model.DeployGroup{}
	for rows.Next() {
		var group model.DeployGroup
		var created, updated int64
		if err := rows.Scan(&group.ID, &group.Name, &group.WebhookID, &created, &updated); err != nil {
			return nil, err
		}
		group.CreatedAt = time.Unix(0, created).UTC()
		group.UpdatedAt = time.Unix(0, updated).UTC()
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range groups {
		members, err := s.groupMembers(groups[i].ID)
		if err != nil {
			return nil, err
		}
		groups[i].Nodes = members
		groups[i].NodeIDs = memberIDs(members)
	}
	return groups, nil
}

// DeployGroup is one of them.
func (s *Store) DeployGroup(id int64) (model.DeployGroup, error) {
	var group model.DeployGroup
	var created, updated int64
	err := s.db.QueryRow(`SELECT id, name, webhook_id, created_ns, updated_ns FROM deploy_groups WHERE id = ?`, id).
		Scan(&group.ID, &group.Name, &group.WebhookID, &created, &updated)
	if err != nil {
		return model.DeployGroup{}, err
	}
	group.CreatedAt = time.Unix(0, created).UTC()
	group.UpdatedAt = time.Unix(0, updated).UTC()
	members, err := s.groupMembers(id)
	if err != nil {
		return model.DeployGroup{}, err
	}
	group.Nodes = members
	group.NodeIDs = memberIDs(members)
	return group, nil
}

// groupMembers reads a group's machines out of the cluster, in the order they
// were put in it.
//
// The join is the whole point of the design: a machine deleted from the cluster
// leaves the group by itself, because the group holds ids and the cluster holds
// machines. There is no second list to go stale.
func (s *Store) groupMembers(groupID int64) ([]model.DeployMember, error) {
	rows, err := s.db.Query(`SELECT n.id, n.name, COALESCE(n.ssh_address,''),
CASE WHEN n.ssh_password IS NOT NULL AND length(n.ssh_password) > 0 THEN 1 ELSE 0 END, n.locked
FROM deploy_group_nodes g JOIN cluster_nodes n ON n.id = g.node_id
WHERE g.group_id = ? ORDER BY g.position, n.name`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []model.DeployMember{}
	for rows.Next() {
		var member model.DeployMember
		if err := rows.Scan(&member.NodeID, &member.Name, &member.SSHAddress, &member.HasPassword, &member.Locked); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func memberIDs(members []model.DeployMember) []int64 {
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.NodeID)
	}
	return ids
}

// SaveDeployGroup creates or renames a group and replaces its machines.
//
// The whole membership at once, like the machine's environment and the command
// list: it is one thing somebody edits.
func (s *Store) SaveDeployGroup(group model.DeployGroup) (model.DeployGroup, error) {
	name := strings.TrimSpace(group.Name)
	if name == "" {
		return model.DeployGroup{}, errors.New("a group needs a name")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.DeployGroup{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UnixNano()
	id := group.ID
	if id == 0 {
		result, err := tx.Exec(`INSERT INTO deploy_groups(name, webhook_id, created_ns, updated_ns) VALUES(?,?,?,?)`, name, group.WebhookID, now, now)
		if err != nil {
			return model.DeployGroup{}, groupNameError(err)
		}
		if id, err = result.LastInsertId(); err != nil {
			return model.DeployGroup{}, err
		}
	} else {
		result, err := tx.Exec(`UPDATE deploy_groups SET name = ?, webhook_id = ?, updated_ns = ? WHERE id = ?`, name, group.WebhookID, now, id)
		if err != nil {
			return model.DeployGroup{}, groupNameError(err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return model.DeployGroup{}, sql.ErrNoRows
		}
	}
	if _, err := tx.Exec(`DELETE FROM deploy_group_nodes WHERE group_id = ?`, id); err != nil {
		return model.DeployGroup{}, err
	}
	seen := map[int64]bool{}
	for position, nodeID := range group.NodeIDs {
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		// The machine has to exist. A group naming a node that is not there is
		// a deploy that fails at the last moment instead of at the save.
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM cluster_nodes WHERE id = ?`, nodeID).Scan(&exists); err != nil {
			return model.DeployGroup{}, err
		}
		if exists == 0 {
			return model.DeployGroup{}, fmt.Errorf("machine %d is not in the cluster", nodeID)
		}
		if _, err := tx.Exec(`INSERT INTO deploy_group_nodes(group_id,node_id,position) VALUES(?,?,?)`,
			id, nodeID, position); err != nil {
			return model.DeployGroup{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.DeployGroup{}, err
	}
	return s.DeployGroup(id)
}

func groupNameError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return errors.New("a group with that name already exists")
	}
	return err
}

// DeleteDeployGroup removes a group and its membership. The runs it started
// stay: they carry the group's name, not a pointer to it.
func (s *Store) DeleteDeployGroup(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM deploy_group_nodes WHERE group_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM deploy_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// DeployTemplates is the newest version of each template, with its history.
func (s *Store) DeployTemplates() ([]model.DeployTemplate, error) {
	rows, err := s.db.Query(`SELECT id, MAX(version) FROM deploy_templates GROUP BY id ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type pair struct {
		id      int64
		version int
	}
	pairs := []pair{}
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.version); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	templates := []model.DeployTemplate{}
	for _, p := range pairs {
		template, err := s.DeployTemplate(p.id, p.version)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	return templates, nil
}

// DeployTemplate reads one version. Version zero is the newest, which is what a
// page asks for; a run asks for the exact one it pinned.
func (s *Store) DeployTemplate(id int64, version int) (model.DeployTemplate, error) {
	query := `SELECT id, version, name, service_name, image, path, compose_yaml, health_path, health_port,
secret_env_id, vars, created_ns FROM deploy_templates WHERE id = ?`
	args := []any{id}
	if version > 0 {
		query += ` AND version = ?`
		args = append(args, version)
	} else {
		query += ` ORDER BY version DESC LIMIT 1`
	}
	var template model.DeployTemplate
	var vars string
	var created int64
	err := s.db.QueryRow(query, args...).Scan(&template.ID, &template.Version, &template.Name, &template.ServiceName,
		&template.Image, &template.Path, &template.ComposeYAML, &template.HealthPath, &template.HealthPort,
		&template.SecretEnvID, &vars, &created)
	if err != nil {
		return model.DeployTemplate{}, err
	}
	template.CreatedAt = time.Unix(0, created).UTC()
	if err := json.Unmarshal([]byte(vars), &template.Vars); err != nil {
		template.Vars = []model.TemplateVar{}
	}
	if template.Vars == nil {
		template.Vars = []model.TemplateVar{}
	}
	template.SecretEnvLabel = s.envLabel(template.SecretEnvID)
	versions, err := s.templateVersions(id)
	if err != nil {
		return model.DeployTemplate{}, err
	}
	template.Versions = versions
	return template, nil
}

// envLabel names a vault environment the way the secrets page does. An
// environment that has since been deleted reads as gone rather than as an id,
// because the template is still deployable — it just cannot resolve.
func (s *Store) envLabel(envID int64) string {
	if envID == 0 {
		return ""
	}
	var workspace, name string
	err := s.db.QueryRow(`SELECT w.name, e.name FROM secret_envs e
JOIN secret_workspaces w ON w.id = e.workspace_id WHERE e.id = ?`, envID).Scan(&workspace, &name)
	if err != nil {
		return "a deleted environment"
	}
	return workspace + " / " + name
}

func (s *Store) templateVersions(id int64) ([]model.DeployTemplateVersion, error) {
	rows, err := s.db.Query(`SELECT t.version, t.created_ns,
EXISTS(SELECT 1 FROM deploy_runs r WHERE r.template_id = t.id AND r.template_version = t.version)
FROM deploy_templates t WHERE t.id = ? ORDER BY t.version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := []model.DeployTemplateVersion{}
	for rows.Next() {
		var version model.DeployTemplateVersion
		var created int64
		if err := rows.Scan(&version.Version, &created, &version.InUse); err != nil {
			return nil, err
		}
		version.CreatedAt = time.Unix(0, created).UTC()
		versions = append(versions, version)
	}
	return versions, rows.Err()
}

// SaveDeployTemplate writes a new template, or the next version of one.
//
// Never an update. The row a past run points at is the answer to "what did we
// deploy", and an edit that rewrote it would make every record retroactively
// describe today's file.
func (s *Store) SaveDeployTemplate(template model.DeployTemplate) (model.DeployTemplate, error) {
	template.Name = strings.TrimSpace(template.Name)
	template.ServiceName = strings.TrimSpace(template.ServiceName)
	template.Image = strings.TrimSpace(template.Image)
	template.Path = strings.TrimSpace(template.Path)
	template.HealthPath = strings.TrimSpace(template.HealthPath)
	for i := range template.Vars {
		template.Vars[i].Key = strings.TrimSpace(template.Vars[i].Key)
		if template.Vars[i].Source == model.VarVault {
			// A vault variable never carries a value, whatever was posted. The
			// value it would carry is the one thing this design does not store.
			template.Vars[i].Value = ""
		}
	}
	// What can be worked out is worked out here, once, and then stored: the
	// service name and the directory from the template's name, the image from
	// the compose file's own ${TAG} line.
	if err := template.Normalise(); err != nil {
		return model.DeployTemplate{}, err
	}
	if err := template.Validate(); err != nil {
		return model.DeployTemplate{}, err
	}
	if template.SecretEnvID != 0 {
		if _, err := s.Env(template.SecretEnvID); err != nil {
			return model.DeployTemplate{}, errors.New("that vault environment does not exist")
		}
	}
	vars, err := json.Marshal(template.Vars)
	if err != nil {
		return model.DeployTemplate{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.DeployTemplate{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	id := template.ID
	version := 1
	if id == 0 {
		// The id is a template's identity across versions, so it cannot come
		// from AUTOINCREMENT on a table keyed by (id, version).
		if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM deploy_templates`).Scan(&id); err != nil {
			return model.DeployTemplate{}, err
		}
	} else {
		var previous int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0) FROM deploy_templates WHERE id = ?`, id).Scan(&previous); err != nil {
			return model.DeployTemplate{}, err
		}
		if previous == 0 {
			return model.DeployTemplate{}, sql.ErrNoRows
		}
		version = previous + 1
	}
	if _, err := tx.Exec(`INSERT INTO deploy_templates(id,version,name,service_name,image,path,compose_yaml,
health_path,health_port,secret_env_id,vars,created_ns) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, version, template.Name, template.ServiceName, template.Image, template.Path, template.ComposeYAML,
		template.HealthPath, template.HealthPort, template.SecretEnvID, string(vars), time.Now().UnixNano()); err != nil {
		return model.DeployTemplate{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.DeployTemplate{}, err
	}
	return s.DeployTemplate(id, version)
}

// DeleteDeployTemplate removes every version of a template. The runs stay, with
// the name and the version they recorded.
func (s *Store) DeleteDeployTemplate(id int64) error {
	result, err := s.db.Exec(`DELETE FROM deploy_templates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ResolveDeployVars turns a template's declared variables into the pairs that go
// into the .env on the machine.
//
// The vault ones are read here, at deploy time, and never stored: guard holds
// the value for as long as it takes to write the file and no longer. A key the
// environment does not have is an error rather than an empty line, because an
// application booting with DATABASE_URL= set to nothing fails in a way that
// takes an hour to trace back to here.
func (s *Store) ResolveDeployVars(template model.DeployTemplate) ([]model.NodeEnvVar, error) {
	vars := make([]model.NodeEnvVar, 0, len(template.Vars))
	for _, declared := range template.Vars {
		switch declared.Source {
		case model.VarVault:
			secret, err := s.Secret(template.SecretEnvID, declared.Key)
			if err != nil {
				return nil, fmt.Errorf("%s is not in %s", declared.Key, s.envLabel(template.SecretEnvID))
			}
			if secret.Unreadable {
				return nil, fmt.Errorf("%s cannot be decrypted with this instance's key", declared.Key)
			}
			vars = append(vars, model.NodeEnvVar{Key: declared.Key, Value: secret.Value})
		default:
			vars = append(vars, model.NodeEnvVar{Key: declared.Key, Value: declared.Value})
		}
	}
	return vars, nil
}

// DeployTarget is a machine's login with the lock applied — the only way to one
// in this package.
//
// A deploy writes two files and runs docker. That is the command line with a
// template's name on it, so the lock has to reach it: a locked machine whose
// stored commands are frozen but which anyone could deploy to is not locked.
func (s *Store) DeployTarget(nodeID int64) (SSHLogin, error) {
	node, err := s.Node(nodeID)
	if err != nil {
		return SSHLogin{}, err
	}
	if node.Locked {
		return SSHLogin{}, ErrDeployLocked
	}
	return s.SSHLoginFor(nodeID)
}

// CreateDeployRun writes the run and a row per machine, all pending.
//
// The rows exist before anything is touched, so a page opened a second later
// shows the whole plan — including the machines that have not been reached —
// rather than growing one line at a time.
func (s *Store) CreateDeployRun(run model.DeployRun) (model.DeployRun, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.DeployRun{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	started := time.Now().UnixNano()
	result, err := tx.Exec(`INSERT INTO deploy_runs(group_id,group_name,template_id,template_version,template_name,
service_name,image,tag,mode,status,rollback,started_ns) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.GroupID, run.GroupName, run.TemplateID, run.TemplateVersion, run.TemplateName,
		run.ServiceName, run.Image, run.Tag, run.Mode, model.RunRunning, run.Rollback, started)
	if err != nil {
		return model.DeployRun{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.DeployRun{}, err
	}
	for position, instance := range run.Instances {
		if _, err := tx.Exec(`INSERT INTO deploy_run_instances(run_id,node_id,node_name,position,status,previous_tag)
VALUES(?,?,?,?,?,?)`, id, instance.NodeID, instance.NodeName, position, model.InstancePending, instance.PreviousTag); err != nil {
			return model.DeployRun{}, err
		}
	}
	// Trimmed here rather than on a timer: the moment a group gains a run is
	// the only moment its history can have grown too long.
	if err := pruneDeployRuns(tx, run.GroupID); err != nil {
		return model.DeployRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.DeployRun{}, err
	}
	return s.DeployRun(id)
}

// DeployRun reads one run with its machines.
func (s *Store) DeployRun(id int64) (model.DeployRun, error) {
	var run model.DeployRun
	var started, finished, awaiting int64
	err := s.db.QueryRow(`SELECT id,group_id,group_name,template_id,template_version,template_name,service_name,
image,tag,mode,status,rollback,started_ns,finished_ns,awaiting_ns FROM deploy_runs WHERE id = ?`, id).
		Scan(&run.ID, &run.GroupID, &run.GroupName, &run.TemplateID, &run.TemplateVersion, &run.TemplateName,
			&run.ServiceName, &run.Image, &run.Tag, &run.Mode, &run.Status, &run.Rollback, &started, &finished, &awaiting)
	if err != nil {
		return model.DeployRun{}, err
	}
	run.StartedAt = time.Unix(0, started).UTC()
	if finished > 0 {
		run.FinishedAt = time.Unix(0, finished).UTC()
	}
	if awaiting > 0 {
		run.AwaitingSince = time.Unix(0, awaiting).UTC()
	}
	instances, err := s.deployInstances(id)
	if err != nil {
		return model.DeployRun{}, err
	}
	run.Instances = instances
	return run, nil
}

func (s *Store) deployInstances(runID int64) ([]model.DeployInstance, error) {
	rows, err := s.db.Query(`SELECT run_id,node_id,node_name,position,status,started_ns,finished_ns,
previous_tag,health,error,output FROM deploy_run_instances WHERE run_id = ? ORDER BY position`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	instances := []model.DeployInstance{}
	for rows.Next() {
		var instance model.DeployInstance
		var started, finished int64
		if err := rows.Scan(&instance.RunID, &instance.NodeID, &instance.NodeName, &instance.Position,
			&instance.Status, &started, &finished, &instance.PreviousTag, &instance.Health,
			&instance.Error, &instance.Output); err != nil {
			return nil, err
		}
		if started > 0 {
			instance.StartedAt = time.Unix(0, started).UTC()
		}
		if finished > 0 {
			instance.FinishedAt = time.Unix(0, finished).UTC()
		}
		instances = append(instances, instance)
	}
	return instances, rows.Err()
}

// deployRunRetention is how much history one group keeps.
//
// The same fifty the cluster's command history keeps, and for the same reason:
// enough to see a pattern — this group has failed its gate four times this week
// — and not so much that a page of deploys is a page of archaeology. Per group
// rather than overall, so a busy application cannot push a quiet one's history
// out of the table.
//
// A number beside the code that reads it rather than a setting. Nobody has ever
// wanted a different one, and the row it protects is not the expensive part of
// this database.
const deployRunRetention = 50

// CountDeployRuns is how many there are, for a page that draws a pager.
func (s *Store) CountDeployRuns() (int, error) {
	var total int
	err := s.db.QueryRow(`SELECT count(*) FROM deploy_runs`).Scan(&total)
	return total, err
}

// pruneDeployRuns keeps one group's history to the newest few.
//
// An unfinished run is never dropped, whatever its age: a run still going or
// still waiting for somebody is the one row that must not vanish from under the
// page watching it.
func pruneDeployRuns(tx *sql.Tx, groupID int64) error {
	if _, err := tx.Exec(`DELETE FROM deploy_run_instances WHERE run_id IN (
SELECT id FROM deploy_runs WHERE group_id = ? AND status NOT IN (?,?) AND id NOT IN (
SELECT id FROM deploy_runs WHERE group_id = ? ORDER BY id DESC LIMIT ?))`,
		groupID, model.RunRunning, model.RunAwaiting, groupID, deployRunRetention); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM deploy_runs WHERE group_id = ? AND status NOT IN (?,?) AND id NOT IN (
SELECT id FROM deploy_runs WHERE group_id = ? ORDER BY id DESC LIMIT ?)`,
		groupID, model.RunRunning, model.RunAwaiting, groupID, deployRunRetention)
	return err
}

// DeployRuns is the history, newest first, one page at a time.
func (s *Store) DeployRuns(limit, offset int) ([]model.DeployRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(`SELECT id FROM deploy_runs ORDER BY started_ns DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runs := []model.DeployRun{}
	for _, id := range ids {
		run, err := s.DeployRun(id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// ActiveDeployRuns is every run that is still going or still stopped at a
// failure — what the loop sweeps and what a page has to keep polling.
func (s *Store) ActiveDeployRuns() ([]model.DeployRun, error) {
	rows, err := s.db.Query(`SELECT id FROM deploy_runs WHERE status IN (?,?) ORDER BY started_ns`,
		model.RunRunning, model.RunAwaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	runs := []model.DeployRun{}
	for _, id := range ids {
		run, err := s.DeployRun(id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// SetDeployInstance writes one machine's progress.
func (s *Store) SetDeployInstance(instance model.DeployInstance) error {
	started := int64(0)
	if !instance.StartedAt.IsZero() {
		started = instance.StartedAt.UnixNano()
	}
	finished := int64(0)
	if !instance.FinishedAt.IsZero() {
		finished = instance.FinishedAt.UnixNano()
	}
	_, err := s.db.Exec(`UPDATE deploy_run_instances SET status=?,started_ns=?,finished_ns=?,health=?,error=?,output=?
WHERE run_id=? AND node_id=?`, instance.Status, started, finished, instance.Health, instance.Error,
		instance.Output, instance.RunID, instance.NodeID)
	return err
}

// SetDeployRunStatus moves the run itself. Finishing stamps the time; entering
// `awaiting` stamps when the wait started, which is what the deadline is
// measured from and why it survives a restart of the page.
func (s *Store) SetDeployRunStatus(runID int64, status string) error {
	now := time.Now().UnixNano()
	switch status {
	case model.RunAwaiting:
		_, err := s.db.Exec(`UPDATE deploy_runs SET status=?, awaiting_ns=? WHERE id=?`, status, now, runID)
		return err
	case model.RunRunning:
		_, err := s.db.Exec(`UPDATE deploy_runs SET status=?, awaiting_ns=0 WHERE id=?`, status, runID)
		return err
	default:
		_, err := s.db.Exec(`UPDATE deploy_runs SET status=?, finished_ns=? WHERE id=?`, status, now, runID)
		return err
	}
}

// RecordDeployHealthy is the one write that moves what a machine is running.
//
// What was current becomes last good, and the thing that just passed becomes
// current. That step is the whole of rollback: "last good" has to mean the
// deploy *before* this one, or the button has nothing to offer — a failed
// deploy leaves the machine on the last good thing already, and a successful
// one would otherwise name itself as its own rollback target.
//
// The pair moves only on a passed health gate, so both halves of it are things
// that actually answered. A deploy identical to the one running does not shift
// last good, because stepping back to an identical thing is not a step.
func (s *Store) RecordDeployHealthy(nodeID int64, service, tag string, templateID int64, version int) error {
	_, err := s.db.Exec(`INSERT INTO deploy_state(node_id,service_name,current_tag,current_version,
last_good_tag,last_good_version,template_id,updated_ns) VALUES(?,?,?,?,'',0,?,?)
ON CONFLICT(node_id,service_name) DO UPDATE SET
last_good_tag = CASE WHEN deploy_state.current_tag <> excluded.current_tag
  OR deploy_state.current_version <> excluded.current_version
  THEN deploy_state.current_tag ELSE deploy_state.last_good_tag END,
last_good_version = CASE WHEN deploy_state.current_tag <> excluded.current_tag
  OR deploy_state.current_version <> excluded.current_version
  THEN deploy_state.current_version ELSE deploy_state.last_good_version END,
current_tag=excluded.current_tag, current_version=excluded.current_version,
template_id=excluded.template_id, updated_ns=excluded.updated_ns`,
		nodeID, service, tag, version, templateID, time.Now().UnixNano())
	return err
}

// DeployStateFor is what one machine is running for one service. A machine that
// has never had a healthy deploy has no row, and the zero value says so.
func (s *Store) DeployStateFor(nodeID int64, service string) (model.DeployState, error) {
	var state model.DeployState
	var updated int64
	err := s.db.QueryRow(`SELECT node_id,service_name,current_tag,current_version,last_good_tag,
last_good_version,template_id,updated_ns FROM deploy_state WHERE node_id = ? AND service_name = ?`, nodeID, service).
		Scan(&state.NodeID, &state.ServiceName, &state.CurrentTag, &state.CurrentVersion,
			&state.LastGoodTag, &state.LastGoodVersion, &state.TemplateID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeployState{NodeID: nodeID, ServiceName: service}, nil
	}
	if err != nil {
		return model.DeployState{}, err
	}
	state.UpdatedAt = time.Unix(0, updated).UTC()
	return state, nil
}

// DeployStates is everything running on one machine, which is what the machine's
// own page shows.
func (s *Store) DeployStates(nodeID int64) ([]model.DeployState, error) {
	rows, err := s.db.Query(`SELECT node_id,service_name,current_tag,current_version,last_good_tag,
last_good_version,template_id,updated_ns FROM deploy_state WHERE node_id = ? ORDER BY service_name`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []model.DeployState{}
	for rows.Next() {
		var state model.DeployState
		var updated int64
		if err := rows.Scan(&state.NodeID, &state.ServiceName, &state.CurrentTag, &state.CurrentVersion,
			&state.LastGoodTag, &state.LastGoodVersion, &state.TemplateID, &updated); err != nil {
			return nil, err
		}
		state.UpdatedAt = time.Unix(0, updated).UTC()
		states = append(states, state)
	}
	return states, rows.Err()
}

// SweepDeployRuns is what a restart leaves behind, made honest.
//
// The lock a run holds is in memory — the same bargain the scheduler makes with
// its `running` map — so a restart releases every lock and loses every goroutine
// that was mid-deploy. What it must not do is leave rows saying `deploying`
// forever, or resume: guard cannot know whether `compose up` finished, and a
// resume that assumed either way would be guessing about somebody's production.
//
// So the rows say `interrupted`, which is a state with an obvious next step, and
// `current_tag` is untouched — it still names the last tag that actually proved
// itself, rather than the one that was on its way.
func (s *Store) SweepDeployRuns() (int, error) {
	now := time.Now().UnixNano()
	result, err := s.db.Exec(`UPDATE deploy_run_instances SET status=?, finished_ns=?,
error=CASE WHEN error='' THEN 'guard restarted while this machine was being deployed to' ELSE error END
WHERE status IN (?,?)`, model.InstanceInterrupted, now, model.InstanceDeploying, model.InstanceHealthCheck)
	if err != nil {
		return 0, err
	}
	touched, _ := result.RowsAffected()
	if _, err := s.db.Exec(`UPDATE deploy_run_instances SET status=? WHERE status=? AND run_id IN
(SELECT id FROM deploy_runs WHERE status IN (?,?))`, model.InstanceSkipped, model.InstancePending,
		model.RunRunning, model.RunAwaiting); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`UPDATE deploy_runs SET status=?, finished_ns=? WHERE status IN (?,?)`,
		model.RunInterrupted, now, model.RunRunning, model.RunAwaiting); err != nil {
		return 0, err
	}
	return int(touched), nil
}
