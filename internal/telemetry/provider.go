package telemetry

// Cloud accounts: the stored keys that unlock a provider.
//
// Storage only, the same split as the cluster: talking to Vultr lives in
// internal/vultr, because outbound HTTP is not this package's job. What is
// stored is deliberately small — a name and a sealed key — since the
// registries, the instances and the object storage behind it are live state
// the provider answers for.
//
// One key, every surface. It arrived as a registry credential and it is the
// same account key the cluster page needs to read an instance's power state
// and the storage page needs to list buckets, so it is stored once: typed
// once, proved once, revoked once.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateProviders(db *sql.DB) error {
	// The table began life as registry_accounts, when a registry was all a key
	// unlocked. Renamed rather than re-created: it holds somebody's sealed
	// key, and a migration that made them type it again would be a migration
	// that lost it.
	var legacy int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type='table' AND name='registry_accounts'`).Scan(&legacy); err != nil {
		return fmt.Errorf("look for registry accounts: %w", err)
	}
	if legacy > 0 {
		var current int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type='table' AND name='provider_accounts'`).Scan(&current); err != nil {
			return fmt.Errorf("look for provider accounts: %w", err)
		}
		if current == 0 {
			if _, err := db.Exec(`ALTER TABLE registry_accounts RENAME TO provider_accounts`); err != nil {
				return fmt.Errorf("rename registry accounts: %w", err)
			}
		}
	}
	const schema = `
CREATE TABLE IF NOT EXISTS provider_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'vultr',
  api_key BLOB,
  created_at_ns INTEGER NOT NULL,
  updated_at_ns INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate provider accounts: %w", err)
	}
	// One ALTER per column guard has grown since the table was first written.
	// Added rather than folded into the schema above, because the table
	// predates them and an existing database must not be asked to start over.
	for _, column := range []string{
		`external_id TEXT NOT NULL DEFAULT ''`,
		`s3_access_key TEXT NOT NULL DEFAULT ''`,
		`s3_secret BLOB`,
	} {
		if _, err := db.Exec(`ALTER TABLE provider_accounts ADD COLUMN ` + column); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("migrate provider accounts: %w", err)
		}
	}
	return nil
}

// ProviderAccounts lists the stored keys, without the keys.
func (s *Store) ProviderAccounts() ([]model.ProviderAccount, error) {
	rows, err := s.db.Query(`SELECT id, name, provider, external_id, s3_access_key,
api_key IS NOT NULL AND length(api_key) > 0, s3_secret IS NOT NULL AND length(s3_secret) > 0
FROM provider_accounts ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []model.ProviderAccount{}
	for rows.Next() {
		var account model.ProviderAccount
		if err := rows.Scan(&account.ID, &account.Name, &account.Provider, &account.ExternalID,
			&account.S3Access, &account.HasKey, &account.HasS3); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// SaveProviderAccount stores a new account with its key sealed. Only creation
// exists on purpose: a key is proved before it is stored, and "change the key"
// is delete-and-add so the proof can never be skipped.
func (s *Store) SaveProviderAccount(account model.ProviderAccount) (model.ProviderAccount, error) {
	account.Name = strings.TrimSpace(account.Name)
	account.ExternalID = strings.TrimSpace(account.ExternalID)
	account.S3Access = strings.TrimSpace(account.S3Access)
	if account.Provider == "" {
		account.Provider = model.ProviderVultr
	}
	if err := account.Validate(); err != nil {
		return model.ProviderAccount{}, err
	}
	if account.APIKey == nil || strings.TrimSpace(*account.APIKey) == "" {
		return model.ProviderAccount{}, fmt.Errorf("an api key is required")
	}
	sealed, err := s.seal(account.APIKey)
	if err != nil {
		return model.ProviderAccount{}, err
	}
	sealedS3, err := s.seal(account.S3Secret)
	if err != nil {
		return model.ProviderAccount{}, err
	}
	now := time.Now().UTC().UnixNano()
	result, err := s.db.Exec(`INSERT INTO provider_accounts
(name, provider, external_id, s3_access_key, api_key, s3_secret, created_at_ns, updated_at_ns)
VALUES (?,?,?,?,?,?,?,?)`, account.Name, account.Provider, account.ExternalID,
		account.S3Access, sealed, sealedS3, now, now)
	if err != nil {
		return model.ProviderAccount{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ProviderAccount{}, err
	}
	account.ID = id
	account.HasS3 = account.S3Secret != nil && *account.S3Secret != ""
	account.APIKey = nil
	account.S3Secret = nil
	account.HasKey = true
	return account, nil
}

// DeleteProviderAccount forgets an account and its key — and unlinks every
// machine that pointed into it, because a link to an account guard can no
// longer open is a provider strip that can only ever say "the stored key
// could not be opened".
func (s *Store) DeleteProviderAccount(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM cluster_snapshots WHERE node_id IN
(SELECT id FROM cluster_nodes WHERE provider_account_id = ?)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE cluster_nodes SET provider='',provider_account_id=0,provider_instance_id=''
WHERE provider_account_id = ?`, id); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM provider_accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ProviderAccount reads one account without its key — the name, the provider
// it speaks to and the provider's own id for it. Every caller pairs this with
// ProviderKeyFor: which API to talk to is a different question from what the
// secret is, and only one of them returns something worth guarding.
func (s *Store) ProviderAccount(id int64) (model.ProviderAccount, error) {
	var account model.ProviderAccount
	err := s.db.QueryRow(`SELECT id, name, provider, external_id, s3_access_key,
api_key IS NOT NULL AND length(api_key) > 0, s3_secret IS NOT NULL AND length(s3_secret) > 0
FROM provider_accounts WHERE id = ?`, id).
		Scan(&account.ID, &account.Name, &account.Provider, &account.ExternalID,
			&account.S3Access, &account.HasKey, &account.HasS3)
	return account, err
}

// SetProviderS3 stores or replaces one account's S3 pair, sealed.
//
// This is the one thing about an account that can be changed in place, and it
// is worth saying why the API key cannot. The key is what the account *is*:
// changing it is delete-and-add, so the proof it once worked can never be
// skipped. The S3 pair is a second, optional credential for one feature, and
// making somebody delete the account to add it would mean unlinking every
// machine that pointed into it to gain a Browse button. The proof is kept —
// the endpoint above proves the pair before calling this — and only the
// destruction is dropped.
//
// An empty access key clears the pair, which is how browsing is turned off
// again without touching anything else.
func (s *Store) SetProviderS3(id int64, access string, secret *string) error {
	access = strings.TrimSpace(access)
	sealed, err := s.seal(secret)
	if err != nil {
		return err
	}
	if access == "" || len(sealed) == 0 {
		access, sealed = "", nil
	}
	result, err := s.db.Exec(`UPDATE provider_accounts
SET s3_access_key = ?, s3_secret = ?, updated_at_ns = ? WHERE id = ?`,
		access, sealed, time.Now().UTC().UnixNano(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ProviderS3For decrypts one account's stored S3 pair. Named for what it
// returns, like ProviderKeyFor and SSHLoginFor: this is the credential that
// reads somebody's objects, and the one call that can return it says so.
//
// An account without one is not an error here — most have none — so it
// answers an empty pair and lets the provider say what that means.
func (s *Store) ProviderS3For(id int64) (access, secret string, err error) {
	var sealed []byte
	if err := s.db.QueryRow(`SELECT s3_access_key, COALESCE(s3_secret, x'')
FROM provider_accounts WHERE id = ?`, id).Scan(&access, &sealed); err != nil {
		return "", "", err
	}
	if access == "" || len(sealed) == 0 {
		return "", "", nil
	}
	secret, err = s.secrets.Open(sealed)
	if err != nil {
		return "", "", err
	}
	return access, secret, nil
}

// ProviderKeyFor decrypts one account's key. The same contract as
// SSHLoginFor: every other read path cannot return the secret even by
// mistake, this one can, and it is named for it.
func (s *Store) ProviderKeyFor(id int64) (string, error) {
	var sealed []byte
	if err := s.db.QueryRow(`SELECT COALESCE(api_key, x'') FROM provider_accounts WHERE id = ?`, id).Scan(&sealed); err != nil {
		return "", err
	}
	key, err := s.secrets.Open(sealed)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", fmt.Errorf("this account has no stored key")
	}
	return key, nil
}
