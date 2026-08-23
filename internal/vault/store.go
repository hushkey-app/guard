// Package vault serves stored secrets to the applications that need them.
//
// It is a second process on the same database, and that is the whole point of
// it. Guard is a dashboard: it gets deployed, it gets a bad release, it gets
// restarted while somebody edits a page. The thing an application asks for its
// database password at boot must not be that. So this package has no pages, no
// ingest, no cluster loops and no dependency on any of them — it opens the
// file, reads three things, and answers.
//
// Read-only by construction rather than by intent. The store below has no
// method that changes a secret, an environment or a key, so no handler above
// it can grow one by accident; the only writes it makes are the two lines of
// bookkeeping that say a token was used, and neither is allowed to fail a
// fetch.
package vault

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/secrets"
	"github.com/hushkey-app/guard/internal/telemetry/model"

	_ "modernc.org/sqlite"
)

// Store is the vault's view of guard's database.
type Store struct {
	// db is the writer and it is one connection, rdb is the reader pool — the
	// same split guard's own store keeps, and for the same reason. The vault
	// shares a file with a process that runs retention sweeps and restores, so
	// the three small writes here (last-used, the read log, its trim) are the
	// ones most likely to meet a held write lock. `_txlock=immediate` on the
	// writer means they wait for it rather than being refused: a deferred
	// transaction upgrading a read lock is the one case SQLite fails without
	// consulting `busy_timeout` at all.
	db      *sql.DB
	rdb     *sql.DB
	secrets *secrets.Keeper
}

// ErrNoSchema is what Open returns when the tables are not there: this database
// has never been opened by a guard that knows about secrets. Named, because the
// answer is "start guard once", not "something is broken".
var ErrNoSchema = errors.New("this database has no secrets tables — start guard against it once first")

// Open reads the database and the key that seals it.
//
// The key is deliberately not generated here, unlike in guard. A vault that
// wrote itself a fresh key file would come up perfectly healthy and answer
// every fetch with garbage it could not decrypt — the failure would look like
// corrupted secrets rather than like a missing mount, which is what it is.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = "guard.db"
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("no database at %s: %w", abs, err)
	}
	if os.Getenv("GUARD_SECRET_KEY") == "" {
		if _, err := os.Stat(abs + ".key"); err != nil {
			return nil, fmt.Errorf("no GUARD_SECRET_KEY and no %s.key — the vault needs the key guard sealed these with", abs)
		}
	}
	db, err := sql.Open("sqlite", vaultDSN(abs, "immediate"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	rdb, err := sql.Open("sqlite", vaultDSN(abs, ""))
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(4)
	var name string
	err = rdb.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='secret_keys'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		db.Close()
		rdb.Close()
		return nil, ErrNoSchema
	}
	if err != nil {
		db.Close()
		rdb.Close()
		return nil, err
	}
	return &Store{db: db, rdb: rdb, secrets: secrets.Open(abs)}, nil
}

// vaultDSN is guard's own, minus foreign keys — this process only ever reads
// across them. The timeout is the same ten seconds, because what it waits for
// is the same other process.
func vaultDSN(abs, txlock string) string {
	u := &url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if txlock != "" {
		q.Add("_txlock", txlock)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Store) Close() error {
	err := s.db.Close()
	if readErr := s.rdb.Close(); err == nil {
		err = readErr
	}
	return err
}

// Config is guard's stored configuration, opened.
//
// The vault reads two of those settings — where it listens, and how often it
// records a key's use — so a form on the dashboard that showed them while this
// process kept reading only the unit file would be a form that lies about half
// its rows. It is a read like every other one here: this store has no method
// that changes a setting, and the table it reads is guard's to write.
//
// A row that will not decrypt is skipped rather than failing the read, for the
// same reason guard skips it: one unreadable timeout must not stop the process
// that hands out database passwords at boot.
func (s *Store) Config() (map[string]string, error) {
	// A database written by a guard older than this table is not an error: the
	// vault's whole promise is that it keeps serving while guard is somewhere
	// else in its release cycle, which includes being one version behind.
	var table string
	if err := s.rdb.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='config'`).Scan(&table); err != nil {
		return map[string]string{}, nil
	}
	rows, err := s.rdb.Query(`SELECT name, value FROM config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var name string
		var sealed []byte
		if err := rows.Scan(&name, &sealed); err != nil {
			return nil, err
		}
		value, err := s.secrets.Open(sealed)
		if err != nil {
			continue
		}
		values[name] = value
	}
	return values, rows.Err()
}

// A Holder is what a token turned out to be: which key, and which environment
// it may read. Nothing about the token itself, because nothing needs it again.
type Holder struct {
	KeyID     int64
	Name      string
	EnvID     int64
	EnvName   string
	Workspace string
}

// Holder finds the key a token hashes to, if it is one that may still be used.
//
// Unknown, revoked and expired are one answer on purpose: a caller told which
// of the three it is learns whether a token it guessed exists.
func (s *Store) Holder(hash []byte) (Holder, error) {
	var holder Holder
	var expires, revoked int64
	err := s.rdb.QueryRow(`SELECT k.id, k.name, k.env_id, e.name, w.name, k.expires_ns, k.revoked_ns
FROM secret_keys k
JOIN secret_envs e ON e.id = k.env_id
JOIN secret_workspaces w ON w.id = e.workspace_id
WHERE k.hash = ?`, hash).
		Scan(&holder.KeyID, &holder.Name, &holder.EnvID, &holder.EnvName, &holder.Workspace, &expires, &revoked)
	if err != nil {
		return Holder{}, err
	}
	if revoked != 0 || (expires != 0 && expires < time.Now().UTC().UnixNano()) {
		return Holder{}, sql.ErrNoRows
	}
	return holder, nil
}

// Values reads one environment's secrets, decrypted, with the revision they
// are at — the newest change in the group, which is what the ETag is.
func (s *Store) Values(envID int64) ([]model.Secret, int64, error) {
	rows, err := s.rdb.Query(`SELECT key, COALESCE(value, x''), updated_ns
FROM secrets WHERE env_id = ? ORDER BY key COLLATE NOCASE`, envID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []model.Secret{}
	var revision int64
	for rows.Next() {
		var secret model.Secret
		var sealed []byte
		var updated int64
		if err := rows.Scan(&secret.Key, &sealed, &updated); err != nil {
			return nil, 0, err
		}
		value, err := s.secrets.Open(sealed)
		if err != nil {
			// One unreadable value must not take the other forty with it: an
			// application missing one variable fails with a name in the
			// message, where a fetch that returns 500 fails with nothing.
			secret.Unreadable = true
		}
		secret.Value = value
		if updated > revision {
			revision = updated
		}
		out = append(out, secret)
	}
	return out, revision, rows.Err()
}

// Revision is the newest change in an environment, read without decrypting
// anything — what a conditional request costs when nothing has moved.
func (s *Store) Revision(envID int64) (int64, error) {
	var revision int64
	err := s.rdb.QueryRow(`SELECT COALESCE(max(updated_ns), 0) FROM secrets WHERE env_id = ?`, envID).Scan(&revision)
	return revision, err
}

// EnvByName is for the command line: `guard-vault fetch -workspace hushkey -env
// local` reads the file directly rather than asking a server that may not be
// running, which is the whole reason somebody reaches for it.
//
// An empty workspace is only allowed to mean "the only one there is". Picking
// the first of several would be a command that prints production's values on a
// laptop because the workspaces happened to sort that way.
func (s *Store) EnvByName(workspace, name string) (int64, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		var spaces int
		if err := s.rdb.QueryRow(`SELECT count(*) FROM secret_workspaces`).Scan(&spaces); err != nil {
			return 0, err
		}
		if spaces > 1 {
			return 0, errors.New("this database has several workspaces — name one with -workspace")
		}
		var id int64
		err := s.rdb.QueryRow(`SELECT e.id FROM secret_envs e WHERE e.name = ? COLLATE NOCASE`,
			strings.TrimSpace(name)).Scan(&id)
		return id, err
	}
	var id int64
	err := s.rdb.QueryRow(`SELECT e.id FROM secret_envs e JOIN secret_workspaces w ON w.id = e.workspace_id
WHERE w.name = ? COLLATE NOCASE AND e.name = ? COLLATE NOCASE`, workspace, strings.TrimSpace(name)).Scan(&id)
	return id, err
}

// Used records that a key fetched something.
//
// The only write this package makes, and the caller ignores what it returns:
// bookkeeping that could fail a fetch would mean an application that will not
// boot because an audit row would not fit. It is still worth having — "was that
// key used after it leaked" has no other answer.
func (s *Store) Used(holder Holder, ip string, count int) error {
	now := time.Now().UTC().UnixNano()
	if _, err := s.db.Exec(`UPDATE secret_keys SET last_used_ns = ? WHERE id = ?`, now, holder.KeyID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO secret_reads(key_id, env_id, at_ns, ip, count) VALUES(?,?,?,?,?)`,
		holder.KeyID, holder.EnvID, now, ip, count); err != nil {
		return err
	}
	// Fifty per key, the same depth the command runs keep. A read log that
	// grows without bound is a database file that eventually is the log.
	_, err := s.db.Exec(`DELETE FROM secret_reads WHERE key_id = ? AND id NOT IN (
SELECT id FROM secret_reads WHERE key_id = ? ORDER BY at_ns DESC LIMIT 50)`, holder.KeyID, holder.KeyID)
	return err
}
