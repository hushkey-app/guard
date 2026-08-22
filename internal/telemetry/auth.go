package telemetry

// Sign-in storage: the sessions guard has issued, and the sign-ins in flight.
//
// The same split as everywhere else in this package — talking to Google and
// Apple lives in internal/auth, because outbound HTTP is not this package's
// job. What is stored is two short-lived tables and nothing about a person that
// the provider did not just say.
//
// Neither table holds a secret. A session is keyed by the SHA-256 of the cookie
// value, so the database proves who signed in and grants nothing; a login state
// is a random string that is worthless once claimed, which happens the first
// time it is presented.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateAuth(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash BLOB PRIMARY KEY,
  provider TEXT NOT NULL,
  subject TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  picture TEXT NOT NULL DEFAULT '',
  created_at_ns INTEGER NOT NULL,
  expires_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_expiry ON auth_sessions(expires_at_ns);
CREATE TABLE IF NOT EXISTS auth_states (
  state TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  nonce TEXT NOT NULL,
  redirect_uri TEXT NOT NULL DEFAULT '',
  next TEXT NOT NULL DEFAULT '',
  expires_at_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_states_expiry ON auth_states(expires_at_ns);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate sign-in: %w", err)
	}
	return nil
}

// StartLogin remembers one sign-in in flight, and sweeps the ones that were
// never finished. The sweep lives here rather than in a goroutine because a
// state row is only ever created by this call: an instance nobody signs in to
// has nothing to collect.
func (s *Store) StartLogin(state model.LoginState) error {
	state.State = strings.TrimSpace(state.State)
	if state.State == "" || state.Provider == "" {
		return fmt.Errorf("a sign-in needs a state and a provider")
	}
	if state.ExpiresAt.IsZero() {
		state.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	}
	if _, err := s.db.Exec(`DELETE FROM auth_states WHERE expires_at_ns < ?`, time.Now().UTC().UnixNano()); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO auth_states(state, provider, nonce, redirect_uri, next, expires_at_ns)
VALUES (?,?,?,?,?,?)`, state.State, state.Provider, state.Nonce, state.Redirect, state.Next,
		state.ExpiresAt.UTC().UnixNano())
	return err
}

// ClaimLogin reads a sign-in in flight and deletes it in the same transaction.
// Single use is the point: a callback replayed with the same state finds
// nothing, which is what makes the state parameter worth checking at all.
//
// sql.ErrNoRows means unknown, already used, or expired. The caller cannot tell
// those apart and should not try — all three end the same way.
func (s *Store) ClaimLogin(state string) (model.LoginState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.LoginState{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var out model.LoginState
	var expires int64
	err = tx.QueryRow(`SELECT state, provider, nonce, redirect_uri, next, expires_at_ns
FROM auth_states WHERE state = ?`, state).
		Scan(&out.State, &out.Provider, &out.Nonce, &out.Redirect, &out.Next, &expires)
	if err != nil {
		return model.LoginState{}, err
	}
	if _, err := tx.Exec(`DELETE FROM auth_states WHERE state = ?`, state); err != nil {
		return model.LoginState{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.LoginState{}, err
	}
	out.ExpiresAt = time.Unix(0, expires).UTC()
	if out.ExpiresAt.Before(time.Now().UTC()) {
		return model.LoginState{}, sql.ErrNoRows
	}
	return out, nil
}

// CreateSession stores one signed-in browser. The hash is the key, so signing
// in twice from two browsers is two rows and signing out of one leaves the
// other alone.
func (s *Store) CreateSession(session model.Session) error {
	if len(session.Hash) == 0 || session.Subject == "" {
		return fmt.Errorf("a session needs a token and a subject")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT OR REPLACE INTO auth_sessions
(token_hash, provider, subject, email, name, picture, created_at_ns, expires_at_ns)
VALUES (?,?,?,?,?,?,?,?)`, session.Hash, session.Provider, session.Subject, session.Email,
		session.Name, session.Picture, session.CreatedAt.UTC().UnixNano(), session.ExpiresAt.UTC().UnixNano())
	return err
}

// Session reads one by the hash of its cookie. An expired row is deleted on the
// way past and reported as missing: the alternative is a table that only grows
// for as long as people keep closing tabs instead of signing out.
func (s *Store) Session(hash []byte) (model.Session, error) {
	var session model.Session
	var created, expires int64
	err := s.rdb.QueryRow(`SELECT token_hash, provider, subject, email, name, picture, created_at_ns, expires_at_ns
FROM auth_sessions WHERE token_hash = ?`, hash).
		Scan(&session.Hash, &session.Provider, &session.Subject, &session.Email,
			&session.Name, &session.Picture, &created, &expires)
	if err != nil {
		return model.Session{}, err
	}
	session.CreatedAt = time.Unix(0, created).UTC()
	session.ExpiresAt = time.Unix(0, expires).UTC()
	if session.ExpiresAt.Before(time.Now().UTC()) {
		if _, err := s.db.Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, hash); err != nil {
			return model.Session{}, err
		}
		return model.Session{}, sql.ErrNoRows
	}
	return session, nil
}

// DeleteSession is signing out. Deleting a session that is already gone is not
// an error — the browser pressed the button twice, or the row expired between
// the page rendering and the press.
func (s *Store) DeleteSession(hash []byte) error {
	_, err := s.db.Exec(`DELETE FROM auth_sessions WHERE token_hash = ?`, hash)
	return err
}

// PurgeSessions removes every expired session and returns how many. Called on
// startup, so an instance that was down for a month does not come back holding
// a month of dead rows.
func (s *Store) PurgeSessions() (int64, error) {
	now := time.Now().UTC().UnixNano()
	result, err := s.db.Exec(`DELETE FROM auth_sessions WHERE expires_at_ns < ?`, now)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`DELETE FROM auth_states WHERE expires_at_ns < ?`, now); err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
