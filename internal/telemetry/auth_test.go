package telemetry

// The storage side of signing in: sessions that expire, states that are worth
// exactly one use, and the members list.

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func TestSessionsExpire(t *testing.T) {
	store := NewStore(10)
	defer store.Close()

	live := model.Session{
		Hash: []byte("live"), Provider: model.ProviderGoogle, Subject: "s1",
		Email: "ana@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}
	dead := model.Session{
		Hash: []byte("dead"), Provider: model.ProviderGoogle, Subject: "s2",
		Email: "bo@example.com", ExpiresAt: time.Now().Add(-time.Minute),
	}
	for _, session := range []model.Session{live, dead} {
		if err := store.CreateSession(session); err != nil {
			t.Fatal(err)
		}
	}

	found, err := store.Session([]byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	if found.Email != "ana@example.com" || found.Viewer().Display() != "ana@example.com" {
		t.Fatalf("session = %#v", found)
	}
	// An expired session is gone, not merely rejected: reading one is what
	// collects it, so a table of dead rows never accumulates.
	if _, err := store.Session([]byte("dead")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired session err = %v, want no rows", err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE token_hash = ?`, []byte("dead")).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("the expired row should have been deleted on the way past")
	}

	if err := store.DeleteSession([]byte("live")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Session([]byte("live")); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("after signing out = %v", err)
	}
}

func TestLoginStateIsClaimedOnce(t *testing.T) {
	store := NewStore(10)
	defer store.Close()

	pending := model.LoginState{
		State: "state-1", Provider: model.ProviderApple, Nonce: "nonce-1",
		Redirect: "https://guard.test/auth/apple/callback", Next: "/logs",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := store.StartLogin(pending); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimLogin("state-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Nonce != "nonce-1" || claimed.Next != "/logs" || claimed.Redirect != pending.Redirect {
		t.Fatalf("claimed = %#v", claimed)
	}
	if _, err := store.ClaimLogin("state-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second claim = %v, want no rows", err)
	}

	// An expired state is refused even though the row is there to be read.
	if err := store.StartLogin(model.LoginState{
		State: "old", Provider: model.ProviderGoogle, ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimLogin("old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired state = %v, want no rows", err)
	}
}

func TestMembersList(t *testing.T) {
	store := NewStore(10)
	defer store.Close()

	if _, err := store.SaveMember(model.Member{Email: "  Bo@Example.com "}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMember(model.Member{Email: "ana@example.com", Role: model.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMember(model.Member{Email: "nonsense"}); err == nil {
		t.Fatal("an address that is not one should not be stored")
	}

	// Addresses are compared lowercased, everywhere, or a member is somebody
	// who cannot sign in because of how they typed their own address.
	member, err := store.Member("BO@EXAMPLE.COM")
	if err != nil {
		t.Fatal(err)
	}
	if member.Email != "bo@example.com" || member.Role != model.RoleMember || member.LastSeen != nil {
		t.Fatalf("member = %#v", member)
	}

	// Admins first: the list is read to answer "who can get in".
	list, err := store.Members()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Email != "ana@example.com" {
		t.Fatalf("members = %#v", list)
	}

	// Adding an existing member with a different role is a promotion.
	promoted, err := store.SaveMember(model.Member{Email: "bo@example.com", Role: model.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !promoted.IsAdmin() {
		t.Fatalf("promoted = %#v", promoted)
	}

	if err := store.MarkMemberSeen("bo@example.com", model.ProviderApple, "Bo Chen"); err != nil {
		t.Fatal(err)
	}
	member, err = store.Member("bo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if member.LastSeen == nil || member.Name != "Bo Chen" || member.Provider != model.ProviderApple {
		t.Fatalf("after signing in = %#v", member)
	}

	if err := store.RemoveMember("ana@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveMember("ana@example.com"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("removing twice = %v", err)
	}
}

// Removing somebody has to reach the browser they left open.
func TestDeleteSessionsForAnAddress(t *testing.T) {
	store := NewStore(10)
	defer store.Close()

	for _, hash := range [][]byte{[]byte("one"), []byte("two")} {
		if err := store.CreateSession(model.Session{
			Hash: hash, Provider: model.ProviderGoogle, Subject: "s", Email: "bo@example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CreateSession(model.Session{
		Hash: []byte("other"), Provider: model.ProviderGoogle, Subject: "s2", Email: "ana@example.com",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	ended, err := store.DeleteSessionsFor("BO@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if ended != 2 {
		t.Fatalf("ended %d sessions, want 2", ended)
	}
	if _, err := store.Session([]byte("other")); err != nil {
		t.Fatalf("somebody else's session was ended too: %v", err)
	}
}
