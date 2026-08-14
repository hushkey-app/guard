package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	keeper, err := New([]byte("a passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keeper.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if string(sealed) == "hunter2" {
		t.Fatal("the password is stored as itself")
	}
	plain, err := keeper.Open(sealed)
	if err != nil || plain != "hunter2" {
		t.Fatalf("opened %q, %v", plain, err)
	}

	// Nothing seals to nothing: "no password" has to be tellable from an
	// encrypted empty string, because the column is what says which it is.
	empty, err := keeper.Seal("")
	if err != nil || empty != nil {
		t.Fatalf("an empty secret sealed to %v, %v", empty, err)
	}

	// The same plaintext twice must not produce the same bytes, or the database
	// would quietly report which machines share a password.
	again, err := keeper.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) == string(sealed) {
		t.Error("two seals of one secret are identical")
	}

	other, err := New([]byte("a different passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed); err != ErrUnreadable {
		t.Errorf("the wrong key opened it: %v", err)
	}
	if _, err := keeper.Open([]byte("not a ciphertext")); err != ErrUnreadable {
		t.Errorf("rubbish decrypted: %v", err)
	}
}

func TestKeyFileIsCreatedOnceAndReused(t *testing.T) {
	t.Setenv("GUARD_SECRET_KEY", "")
	dir := t.TempDir()
	db := filepath.Join(dir, "guard.db")

	first := Open(db)
	if first.Ephemeral {
		t.Fatal("a database on disk got an ephemeral key")
	}
	sealed, err := first.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(db + ".key")
	if err != nil {
		t.Fatal(err)
	}
	// The file is the whole protection: a stolen database is useless without
	// it, so it must not be world-readable.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the key file is %v, want 0600", mode)
	}

	// A restart reads the same key back, which is the point of writing one.
	second := Open(db)
	plain, err := second.Open(sealed)
	if err != nil || plain != "hunter2" {
		t.Fatalf("after a restart: %q, %v", plain, err)
	}
}

func TestEnvironmentKeyWins(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "guard.db")
	t.Setenv("GUARD_SECRET_KEY", "a passphrase from the secret manager")

	keeper := Open(db)
	if _, err := os.Stat(db + ".key"); !os.IsNotExist(err) {
		t.Error("a key file was written even though the key was supplied")
	}
	sealed, err := keeper.Seal("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	same, _ := New([]byte("a passphrase from the secret manager"))
	if plain, err := same.Open(sealed); err != nil || plain != "hunter2" {
		t.Fatalf("the environment key is not the key that was used: %q, %v", plain, err)
	}
}
