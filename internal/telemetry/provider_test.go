package telemetry

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func key(value string) *string { return &value }

func TestRegistryAccountLifecycle(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	saved, err := store.SaveProviderAccount(model.ProviderAccount{Name: "  Vultr production  ", APIKey: key("VZ-test-key")})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "Vultr production" || saved.Provider != "vultr" {
		t.Fatalf("stored %q under %q", saved.Name, saved.Provider)
	}
	// The write answer must not echo the secret, and must say one exists.
	if saved.APIKey != nil || !saved.HasKey {
		t.Fatalf("the answer carried the key back: %+v", saved)
	}

	accounts, err := store.ProviderAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].HasKey || accounts[0].APIKey != nil {
		t.Fatalf("listed %+v", accounts)
	}

	// The one named read path gives the key back, decrypted.
	opened, err := store.ProviderKeyFor(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened != "VZ-test-key" {
		t.Fatalf("opened %q", opened)
	}

	if err := store.DeleteProviderAccount(saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProviderAccount(saved.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleting a gone account: %v", err)
	}
	if _, err := store.ProviderKeyFor(saved.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("key for a gone account: %v", err)
	}
}

func TestRegistryAccountRequiresAKey(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	if _, err := store.SaveProviderAccount(model.ProviderAccount{Name: "no key"}); err == nil {
		t.Fatal("an account without a key was stored")
	}
	if _, err := store.SaveProviderAccount(model.ProviderAccount{Name: "blank key", APIKey: key("   ")}); err == nil {
		t.Fatal("an account with a blank key was stored")
	}
	if _, err := store.SaveProviderAccount(model.ProviderAccount{Name: "wrong provider", Provider: "aws", APIKey: key("k")}); err == nil {
		t.Fatal("an unknown provider was stored")
	}
}
