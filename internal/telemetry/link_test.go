package telemetry

import (
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// account stores one cloud account and returns its id, so the link tests read
// as being about links rather than about keys.
func account(t *testing.T, store *Store, name string) int64 {
	t.Helper()
	key := "secret-" + name
	saved, err := store.SaveProviderAccount(model.ProviderAccount{Name: name, APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	return saved.ID
}

func TestLinkingAMachineToAnInstance(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	id := account(t, store, "vultr")

	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if node.Linked() {
		t.Fatal("a machine nobody linked came back linked")
	}

	linked, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !linked.Linked() || linked.ProviderInstanceID != "i-1" || linked.Provider != model.ProviderVultr {
		t.Fatalf("link did not stick: %#v", linked)
	}

	// An ordinary save carries the whole node back from a form. It must not be
	// able to drop the link by leaving the fields out.
	linked.ProviderAccountID = 0
	linked.ProviderInstanceID = ""
	linked.Name = "VPS-1 (eu)"
	saved, err := store.SaveNode(linked)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Linked() {
		t.Fatal("a rename dropped the cloud link")
	}

	found, name, err := store.NodeForInstance(id, "i-1")
	if err != nil || found != node.ID || name != "VPS-1 (eu)" {
		t.Fatalf("NodeForInstance answered %d %q (%v)", found, name, err)
	}

	if _, err := store.LinkNode(node.ID, model.ProviderLink{}); err != nil {
		t.Fatal(err)
	}
	after, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Linked() {
		t.Fatal("unlinking left the link in place")
	}
}

func TestLinkingRefusesWhatItCannotResolve(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: 999, InstanceID: "i-1"}); err == nil {
		t.Fatal("linked into an account that does not exist")
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: 1, InstanceID: strings.Repeat("x", 200)}); err == nil {
		t.Fatal("accepted an instance id that is not one")
	}
}

// The lock is the machine's dangerous half being finished. A link is a new way
// to act on it — the switch, the rollback — so it cannot be added afterwards,
// and the destructive calls read as refused rather than as unlinked.
func TestALockedMachineKeepsItsLinkAndRefusesToChange(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	id := account(t, store, "vultr")

	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-1"}); err != nil {
		t.Fatal(err)
	}
	node, err = store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	node.Locked = true
	if _, err := store.SaveNode(node); err != nil {
		t.Fatal(err)
	}

	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-2"}); err == nil {
		t.Fatal("a locked machine was repointed at another instance")
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{}); err == nil {
		t.Fatal("a locked machine was unlinked")
	}

	// Reading is still allowed: a locked machine is still a machine somebody
	// wants the power state of.
	if _, err := store.ProviderTargetFor(node.ID, false); err != nil {
		t.Fatalf("a locked machine refused a read: %v", err)
	}
	if _, err := store.ProviderTargetFor(node.ID, true); err == nil {
		t.Fatal("a locked machine accepted a change at the provider")
	}
}

func TestProviderTargetNeedsALink(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProviderTargetFor(node.ID, false); err == nil {
		t.Fatal("an unlinked machine answered with an instance")
	}
}

// The provider's snapshot carries no instance, so the association is guard's
// to keep — and to forget when the link goes, or the machine, or the account.
func TestSnapshotsAreRememberedPerMachine(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	id := account(t, store, "vultr")
	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSnapshot(node.ID, "snap-1", "before the deploy"); err != nil {
		t.Fatal(err)
	}
	ours, err := store.NodeSnapshots(node.ID)
	if err != nil || !ours["snap-1"] {
		t.Fatalf("snapshot not remembered: %v %v", ours, err)
	}
	if err := store.ForgetSnapshot("snap-1"); err != nil {
		t.Fatal(err)
	}
	if ours, _ := store.NodeSnapshots(node.ID); ours["snap-1"] {
		t.Fatal("a forgotten snapshot is still claimed")
	}

	// Unlinking drops the claims: guard can no longer say which machine those
	// images were of, and a claim it cannot stand behind is worse than none.
	if err := store.RecordSnapshot(node.ID, "snap-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{}); err != nil {
		t.Fatal(err)
	}
	if ours, _ := store.NodeSnapshots(node.ID); len(ours) != 0 {
		t.Fatalf("unlinking kept %d snapshot claims", len(ours))
	}
}

// Removing an account cannot leave machines pointing into a key guard can no
// longer open: the strip would only ever say "the stored key could not be
// opened", which is not a thing anybody can act on.
func TestRemovingAnAccountUnlinksItsMachines(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	id := account(t, store, "vultr")
	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSnapshot(node.ID, "snap-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProviderAccount(id); err != nil {
		t.Fatal(err)
	}
	after, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Linked() {
		t.Fatal("the machine still points into a deleted account")
	}
	if ours, _ := store.NodeSnapshots(node.ID); len(ours) != 0 {
		t.Fatal("snapshot claims survived the account")
	}
}

// A copy is meant to become a different machine. Carrying the link across
// would give two rows one power switch, and one of them would be aimed at the
// wrong box.
func TestDuplicateDoesNotCopyTheLink(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	id := account(t, store, "vultr")
	node, err := store.SaveNode(Node{Name: "VPS-1", Domain: "http://10.0.0.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LinkNode(node.ID, model.ProviderLink{AccountID: id, InstanceID: "i-1"}); err != nil {
		t.Fatal(err)
	}
	copied, err := store.DuplicateNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Linked() {
		t.Fatalf("the copy came with a link to %s", copied.ProviderInstanceID)
	}
}
