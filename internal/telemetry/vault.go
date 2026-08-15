package telemetry

// The secrets store: environments, the values in them, and the keys that read
// them back.
//
// Storage only, like every other file in this package — the HTTP that hands a
// value to an application lives in internal/vault, and it is a separate binary
// on purpose, so an application's configuration does not go down with guard's
// dashboard.
//
// A value is sealed with the same keeper the SSH passwords use, which is what
// makes the database file safe to copy and the key file the thing to protect.
// A key is stored as a SHA-256 of the token and nothing else: the table proves
// a token is genuine and grants nothing to anybody who reads it.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/secrets"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateVault(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS secret_envs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  note TEXT NOT NULL DEFAULT '',
  created_ns INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  env_id INTEGER NOT NULL REFERENCES secret_envs(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value BLOB,
  note TEXT NOT NULL DEFAULT '',
  created_ns INTEGER NOT NULL,
  updated_ns INTEGER NOT NULL,
  UNIQUE(env_id, key)
);
CREATE INDEX IF NOT EXISTS idx_secrets_env ON secrets(env_id, key);
-- The tokens. The hash is the key: a lookup is one index hit, and the column
-- it hits is worth nothing to whoever reads the file.
CREATE TABLE IF NOT EXISTS secret_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  env_id INTEGER NOT NULL REFERENCES secret_envs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  hash BLOB NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  created_ns INTEGER NOT NULL,
  expires_ns INTEGER NOT NULL DEFAULT 0,
  last_used_ns INTEGER NOT NULL DEFAULT 0,
  revoked_ns INTEGER NOT NULL DEFAULT 0
);
-- Who fetched what, and when. Written by the vault rather than by guard,
-- capped per key, and never allowed to fail a fetch: this is the answer to
-- "was that key used after it leaked", and it is worth a write nobody waits on.
CREATE TABLE IF NOT EXISTS secret_reads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id INTEGER NOT NULL,
  env_id INTEGER NOT NULL,
  at_ns INTEGER NOT NULL,
  ip TEXT NOT NULL DEFAULT '',
  count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_secret_reads_key ON secret_reads(key_id, at_ns DESC);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate secrets: %w", err)
	}
	// The four groups everybody was going to type anyway. Seeded once — a
	// deleted environment stays deleted, because this runs INSERT OR IGNORE
	// against a name that is still taken by nothing.
	var envs int
	if err := db.QueryRow(`SELECT count(*) FROM secret_envs`).Scan(&envs); err != nil {
		return fmt.Errorf("migrate secrets: %w", err)
	}
	if envs == 0 {
		now := time.Now().UTC().UnixNano()
		for _, name := range model.DefaultEnvs {
			if _, err := db.Exec(`INSERT OR IGNORE INTO secret_envs(name, created_ns) VALUES(?,?)`, name, now); err != nil {
				return fmt.Errorf("seed secret environments: %w", err)
			}
		}
	}
	return nil
}

// Envs lists the groups with their counts and their revision — everything the
// page's left column shows, in one query, because a count per group done from
// the browser would be one request per group.
func (s *Store) Envs() ([]model.Env, error) {
	rows, err := s.db.Query(`SELECT e.id, e.name, e.note, e.created_ns,
(SELECT count(*) FROM secrets WHERE env_id = e.id),
(SELECT count(*) FROM secret_keys WHERE env_id = e.id AND revoked_ns = 0),
COALESCE((SELECT max(updated_ns) FROM secrets WHERE env_id = e.id), 0)
FROM secret_envs e ORDER BY e.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	envs := []model.Env{}
	for rows.Next() {
		var env model.Env
		var created int64
		if err := rows.Scan(&env.ID, &env.Name, &env.Note, &created, &env.Secrets, &env.Keys, &env.Revision); err != nil {
			return nil, err
		}
		if created > 0 {
			env.CreatedAt = time.Unix(0, created).UTC()
		}
		envs = append(envs, env)
	}
	return envs, rows.Err()
}

// Env reads one group by id.
func (s *Store) Env(id int64) (model.Env, error) {
	envs, err := s.Envs()
	if err != nil {
		return model.Env{}, err
	}
	for _, env := range envs {
		if env.ID == id {
			return env, nil
		}
	}
	return model.Env{}, sql.ErrNoRows
}

// SaveEnv adds a group or renames one.
func (s *Store) SaveEnv(env model.Env) (model.Env, error) {
	env.Name = strings.TrimSpace(env.Name)
	env.Note = strings.TrimSpace(env.Note)
	if err := env.Validate(); err != nil {
		return model.Env{}, err
	}
	if env.ID == 0 {
		result, err := s.db.Exec(`INSERT INTO secret_envs(name, note, created_ns) VALUES(?,?,?)`,
			env.Name, env.Note, time.Now().UTC().UnixNano())
		if err != nil {
			return model.Env{}, envError(err, env.Name)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return model.Env{}, err
		}
		return s.Env(id)
	}
	if _, err := s.db.Exec(`UPDATE secret_envs SET name = ?, note = ? WHERE id = ?`, env.Name, env.Note, env.ID); err != nil {
		return model.Env{}, envError(err, env.Name)
	}
	return s.Env(env.ID)
}

// DeleteEnv removes a group, its secrets and its keys.
//
// All three together, in one statement each rather than on a foreign key,
// because SQLite only enforces those when the pragma is on and this is not a
// thing to leave to a connection setting: an environment whose keys outlived it
// would be tokens pointing at nothing, and a token pointing at nothing is a
// token nobody thinks to revoke.
func (s *Store) DeleteEnv(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, statement := range []string{
		`DELETE FROM secrets WHERE env_id = ?`,
		`DELETE FROM secret_keys WHERE env_id = ?`,
		`DELETE FROM secret_envs WHERE id = ?`,
	} {
		if _, err := tx.Exec(statement, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func envError(err error, name string) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("there is already an environment called %q", name)
	}
	return err
}

// Secrets reads one group's pairs, decrypted.
//
// Named for what it does and called from the two places that should: the page
// an admin opened, and the vault answering a key. A value that will not decrypt
// comes back marked rather than as an empty string — an application handed ""
// for a database password fails somewhere far away from the reason.
func (s *Store) Secrets(envID int64) ([]model.Secret, error) {
	rows, err := s.db.Query(`SELECT id, env_id, key, COALESCE(value, x''), note, created_ns, updated_ns
FROM secrets WHERE env_id = ? ORDER BY key COLLATE NOCASE`, envID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Secret{}
	for rows.Next() {
		var secret model.Secret
		var sealed []byte
		var created, updated int64
		if err := rows.Scan(&secret.ID, &secret.EnvID, &secret.Key, &sealed, &secret.Note, &created, &updated); err != nil {
			return nil, err
		}
		value, err := s.secrets.Open(sealed)
		if errors.Is(err, secrets.ErrUnreadable) {
			secret.Unreadable = true
		} else if err != nil {
			return nil, err
		}
		secret.Value = value
		if created > 0 {
			secret.CreatedAt = time.Unix(0, created).UTC()
		}
		if updated > 0 {
			secret.UpdatedAt = time.Unix(0, updated).UTC()
		}
		out = append(out, secret)
	}
	return out, rows.Err()
}

// SaveSecret writes one pair, by key rather than by id: setting a value is the
// same operation whether or not it was already there, which is also what makes
// an import a loop over this.
func (s *Store) SaveSecret(secret model.Secret) (model.Secret, error) {
	secret.Key = strings.TrimSpace(secret.Key)
	secret.Note = strings.TrimSpace(secret.Note)
	if err := secret.Validate(); err != nil {
		return model.Secret{}, err
	}
	if secret.EnvID == 0 {
		return model.Secret{}, errors.New("a secret belongs to one environment")
	}
	sealed, err := s.secrets.Seal(secret.Value)
	if err != nil {
		return model.Secret{}, err
	}
	now := time.Now().UTC().UnixNano()
	_, err = s.db.Exec(`INSERT INTO secrets(env_id, key, value, note, created_ns, updated_ns)
VALUES(?,?,?,?,?,?)
ON CONFLICT(env_id, key) DO UPDATE SET value = excluded.value, note = excluded.note, updated_ns = excluded.updated_ns`,
		secret.EnvID, secret.Key, sealed, secret.Note, now, now)
	if err != nil {
		return model.Secret{}, err
	}
	return s.Secret(secret.EnvID, secret.Key)
}

// Secret reads one pair by its name in its group.
func (s *Store) Secret(envID int64, key string) (model.Secret, error) {
	var secret model.Secret
	var sealed []byte
	var created, updated int64
	err := s.db.QueryRow(`SELECT id, env_id, key, COALESCE(value, x''), note, created_ns, updated_ns
FROM secrets WHERE env_id = ? AND key = ?`, envID, key).
		Scan(&secret.ID, &secret.EnvID, &secret.Key, &sealed, &secret.Note, &created, &updated)
	if err != nil {
		return model.Secret{}, err
	}
	value, err := s.secrets.Open(sealed)
	if errors.Is(err, secrets.ErrUnreadable) {
		secret.Unreadable = true
	} else if err != nil {
		return model.Secret{}, err
	}
	secret.Value = value
	if created > 0 {
		secret.CreatedAt = time.Unix(0, created).UTC()
	}
	if updated > 0 {
		secret.UpdatedAt = time.Unix(0, updated).UTC()
	}
	return secret, nil
}

// DeleteSecret removes one pair.
func (s *Store) DeleteSecret(id int64) error {
	_, err := s.db.Exec(`DELETE FROM secrets WHERE id = ?`, id)
	return err
}

// ImportSecrets applies a paste of .env text to one group.
//
// The interesting half is that it can be asked what it *would* do. A dry run
// reads, compares and reports; the page shows those counts on the confirm, and
// the same call with DryRun off does exactly what was described — the two
// passes share this function rather than agreeing with each other by hand.
func (s *Store) ImportSecrets(request model.Import) (model.ImportResult, error) {
	if request.EnvID == 0 {
		return model.ImportResult{}, errors.New("an import belongs to one environment")
	}
	pairs, skipped := model.ParseEnv(request.Text)
	result := model.ImportResult{Skipped: skipped, DryRun: request.DryRun,
		Added: []string{}, Changed: []string{}, Unchanged: []string{}, Pruned: []string{}}
	if len(pairs) == 0 && len(skipped) == 0 {
		return result, errors.New("there is nothing that looks like KEY=value in that")
	}
	existing, err := s.Secrets(request.EnvID)
	if err != nil {
		return model.ImportResult{}, err
	}
	current := make(map[string]model.Secret, len(existing))
	for _, secret := range existing {
		current[secret.Key] = secret
	}
	// A later line wins over an earlier one with the same key, the way a shell
	// reading the same file would end up.
	seen := make(map[string]bool, len(pairs))
	for i := len(pairs) - 1; i >= 0; i-- {
		if seen[pairs[i].Key] {
			pairs = append(pairs[:i], pairs[i+1:]...)
			continue
		}
		seen[pairs[i].Key] = true
	}
	for _, pair := range pairs {
		was, had := current[pair.Key]
		switch {
		case !had:
			result.Added = append(result.Added, pair.Key)
		// An unreadable value counts as changed: it cannot be compared, and
		// leaving it because the ciphertext is "the same" would mean a paste
		// that visibly does nothing about the one row that is broken.
		case was.Value == pair.Value && !was.Unreadable:
			result.Unchanged = append(result.Unchanged, pair.Key)
			continue
		default:
			result.Changed = append(result.Changed, pair.Key)
		}
		if request.DryRun {
			continue
		}
		if _, err := s.SaveSecret(model.Secret{EnvID: request.EnvID, Key: pair.Key, Value: pair.Value, Note: was.Note}); err != nil {
			return model.ImportResult{}, err
		}
	}
	if request.Prune {
		for _, secret := range existing {
			if seen[secret.Key] {
				continue
			}
			result.Pruned = append(result.Pruned, secret.Key)
			if request.DryRun {
				continue
			}
			if err := s.DeleteSecret(secret.ID); err != nil {
				return model.ImportResult{}, err
			}
		}
	}
	return result, nil
}

// ---- the keys ----

// tokenPrefix is on every minted token. It is what makes a leaked one findable:
// a scan of a repository, a log or a paste for "gsk_" says a guard secrets key
// is in there, without knowing anything about this installation.
const tokenPrefix = "gsk_"

// APIKeys lists the tokens, with the environment each belongs to.
func (s *Store) APIKeys() ([]model.APIKey, error) {
	rows, err := s.db.Query(`SELECT k.id, k.env_id, e.name, k.name, k.prefix,
k.created_ns, k.expires_ns, k.last_used_ns, k.revoked_ns
FROM secret_keys k JOIN secret_envs e ON e.id = k.env_id
ORDER BY k.revoked_ns, e.name COLLATE NOCASE, k.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []model.APIKey{}
	for rows.Next() {
		var key model.APIKey
		var created, expires, used, revoked int64
		if err := rows.Scan(&key.ID, &key.EnvID, &key.EnvName, &key.Name, &key.Prefix,
			&created, &expires, &used, &revoked); err != nil {
			return nil, err
		}
		key.CreatedAt = stamp(created)
		key.ExpiresAt = stamp(expires)
		key.LastUsedAt = stamp(used)
		key.RevokedAt = stamp(revoked)
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func stamp(ns int64) time.Time {
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

// CreateAPIKey mints one token, stores its hash, and returns the token once.
//
// Once is the whole contract: nothing else in guard can produce this string
// again, so a key that was not written down is a key that gets replaced rather
// than recovered. That is a worse afternoon for one person and a much better
// property for everybody else.
func (s *Store) CreateAPIKey(key model.APIKey) (model.APIKey, error) {
	key.Name = strings.TrimSpace(key.Name)
	if err := key.Validate(); err != nil {
		return model.APIKey{}, err
	}
	env, err := s.Env(key.EnvID)
	if err != nil {
		return model.APIKey{}, errors.New("that environment does not exist")
	}
	token, err := mintToken(env.Name)
	if err != nil {
		return model.APIKey{}, err
	}
	sum := sha256.Sum256([]byte(token))
	var expires int64
	if !key.ExpiresAt.IsZero() {
		expires = key.ExpiresAt.UTC().UnixNano()
	}
	result, err := s.db.Exec(`INSERT INTO secret_keys(env_id, name, hash, prefix, created_ns, expires_ns)
VALUES(?,?,?,?,?,?)`, key.EnvID, key.Name, sum[:], token[:len(tokenPrefix)+8], time.Now().UTC().UnixNano(), expires)
	if err != nil {
		return model.APIKey{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.APIKey{}, err
	}
	keys, err := s.APIKeys()
	if err != nil {
		return model.APIKey{}, err
	}
	for _, stored := range keys {
		if stored.ID == id {
			stored.Token = token
			return stored, nil
		}
	}
	return model.APIKey{}, sql.ErrNoRows
}

// mintToken builds `gsk_<env>_<random>`. The environment is in the token
// because these end up pasted into deployment configuration, where telling
// staging's key from production's at a glance is worth more than the two bytes
// of entropy it does not cost.
func mintToken(env string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return -1
		}
	}, env)
	if len(slug) > 12 {
		slug = slug[:12]
	}
	if slug == "" {
		slug = "env"
	}
	return tokenPrefix + slug + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

// RevokeAPIKey stops a token without forgetting it.
//
// Kept rather than deleted, because the row is the only record that the key
// existed: "this token was revoked in March" is the answer to a question
// somebody asks when they find it in an old deployment file, and a deleted row
// answers it with silence.
func (s *Store) RevokeAPIKey(id int64) error {
	_, err := s.db.Exec(`UPDATE secret_keys SET revoked_ns = ? WHERE id = ? AND revoked_ns = 0`,
		time.Now().UTC().UnixNano(), id)
	return err
}
