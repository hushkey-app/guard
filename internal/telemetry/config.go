package telemetry

// Guard's own configuration, in guard's own database.
//
// Everything guard is configured with arrives from the environment, and the
// environment arrives from a file on the box — so changing any of it has always
// meant an SSH session. These rows are the same values, kept where the
// dashboard can edit them, and applied to the process environment at startup:
// the code above still reads os.Getenv and knows nothing about this table.
//
// Sealed like the SSH passwords and for the same reason. Half of what is here
// is a credential — an OAuth client secret, an alert token, the operator's
// bearer token — and this database gets copied to laptops and attached to bug
// reports. The other half is a timeout, sealed anyway: a schema where some
// values are readable and some are not is one where somebody has to decide
// which, per row, forever.
//
// Two names are deliberately not storable, and the rule is in internal/config
// rather than here: anything needed to *open and decrypt this database* cannot
// live inside it.

import (
	"database/sql"
	"fmt"
	"time"
)

func migrateConfig(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS config (
  name TEXT PRIMARY KEY,
  value BLOB NOT NULL,
  updated_ns INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate config: %w", err)
	}
	return nil
}

// Config is every stored setting, opened.
//
// A row that will not decrypt is skipped rather than failing the read: the key
// changed or the file came from another instance, and a guard that refused to
// start over one unreadable timeout would be a guard that cannot be fixed from
// the dashboard that stores it.
func (s *Store) Config() (map[string]string, error) {
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

// SetConfig writes the names given and removes the ones set to empty.
//
// Empty is a removal rather than an empty value on purpose: the fallback is the
// environment, and "" stored would shadow a name set in the unit file with
// nothing at all — which is how somebody turns sign-in off by clearing a field
// they meant to reset. One transaction, because a half-applied OAuth pair is a
// guard that will not start.
func (s *Store) SetConfig(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UnixNano()
	for name, value := range values {
		if value == "" {
			if _, err := tx.Exec(`DELETE FROM config WHERE name = ?`, name); err != nil {
				return err
			}
			continue
		}
		sealed, err := s.secrets.Seal(value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO config(name,value,updated_ns) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET value=excluded.value, updated_ns=excluded.updated_ns`,
			name, sealed, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
