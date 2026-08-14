package telemetry

// The members: who is allowed to sign in.
//
// This is the whole authorization model, and it is a list of email addresses on
// purpose. An OAuth provider answers "is this person who they say they are",
// which is not the question a private dashboard is asking — Google will prove
// that a stranger owns their own account all day. The question is whether the
// address is on the list, and only this table answers it.
//
// One address that is never in the table: GUARD_ADMIN_EMAIL. It is checked
// beside this list rather than seeded into it, because a row can be deleted and
// an environment variable is the way back in when somebody deletes the wrong
// one.

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func migrateMembers(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS auth_members (
  email TEXT PRIMARY KEY,
  role TEXT NOT NULL DEFAULT 'member',
  name TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  added_by TEXT NOT NULL DEFAULT '',
  added_at_ns INTEGER NOT NULL,
  last_seen_ns INTEGER NOT NULL DEFAULT 0
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate members: %w", err)
	}
	return nil
}

// Members lists everyone on the list, admins first and then alphabetically —
// the order somebody reads it in when the question is "who can get in".
func (s *Store) Members() ([]model.Member, error) {
	rows, err := s.db.Query(`SELECT email, role, name, provider, added_by, added_at_ns, last_seen_ns
FROM auth_members ORDER BY role = 'admin' DESC, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []model.Member{}
	for rows.Next() {
		member, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

// Member reads one by address, matched the way every address in guard is.
func (s *Store) Member(email string) (model.Member, error) {
	row := s.db.QueryRow(`SELECT email, role, name, provider, added_by, added_at_ns, last_seen_ns
FROM auth_members WHERE email = ?`, model.NormalizeEmail(email))
	return scanMember(row)
}

func scanMember(row scanner) (model.Member, error) {
	var member model.Member
	var added, seen int64
	if err := row.Scan(&member.Email, &member.Role, &member.Name, &member.Provider,
		&member.AddedBy, &added, &seen); err != nil {
		return model.Member{}, err
	}
	member.AddedAt = time.Unix(0, added).UTC()
	if seen > 0 {
		at := time.Unix(0, seen).UTC()
		member.LastSeen = &at
	}
	return member, nil
}

// SaveMember adds an address to the list, or changes the role of one already on
// it. Both are the same press on the page — "add leo@example.com as an admin"
// when he is already a member is a promotion, not an error somebody has to
// resolve by removing him first.
func (s *Store) SaveMember(member model.Member) (model.Member, error) {
	member.Email = model.NormalizeEmail(member.Email)
	if member.Role == "" {
		member.Role = model.RoleMember
	}
	if err := member.Validate(); err != nil {
		return model.Member{}, err
	}
	now := time.Now().UTC().UnixNano()
	if _, err := s.db.Exec(`INSERT INTO auth_members(email, role, added_by, added_at_ns)
VALUES (?,?,?,?)
ON CONFLICT(email) DO UPDATE SET role = excluded.role`,
		member.Email, member.Role, model.NormalizeEmail(member.AddedBy), now); err != nil {
		return model.Member{}, err
	}
	return s.Member(member.Email)
}

// RemoveMember takes an address off the list. It does not end that person's
// session — SessionsForEmail does, and the endpoint calls both, because a
// removal that leaves an open tab working is not a removal.
func (s *Store) RemoveMember(email string) error {
	result, err := s.db.Exec(`DELETE FROM auth_members WHERE email = ?`, model.NormalizeEmail(email))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkMemberSeen records a sign-in: when, from which provider, and under what
// name. Nothing depends on it — it is what makes the members page readable
// rather than a list of addresses somebody typed once and cannot audit.
//
// A sign-in by an address that is not in the table (the GUARD_ADMIN_EMAIL
// owner) updates nothing and is not an error.
func (s *Store) MarkMemberSeen(email, provider, name string) error {
	_, err := s.db.Exec(`UPDATE auth_members SET last_seen_ns = ?, provider = ?,
name = CASE WHEN ? <> '' THEN ? ELSE name END WHERE email = ?`,
		time.Now().UTC().UnixNano(), provider, name, name, model.NormalizeEmail(email))
	return err
}

// DeleteSessionsFor ends every session belonging to an address, and answers how
// many. Removing somebody from the list has to reach the browser they left
// open, and this is the reach.
func (s *Store) DeleteSessionsFor(email string) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM auth_sessions WHERE email = ?`, model.NormalizeEmail(email))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
