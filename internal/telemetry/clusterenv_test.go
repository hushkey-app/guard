package telemetry

import (
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func machine(t *testing.T, store *Store) Node {
	t.Helper()
	password := "hunter2"
	node, err := store.SaveNode(Node{
		Name: "VPS-1", URL: "http://localhost:8000", Enabled: true,
		SSHAddress: "guard@10.0.0.5:22", Password: &password,
	})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSavingAMachineEnvironment(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	saved, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{
		{Key: " DATABASE_URL ", Value: "postgres://localhost/app"},
		{Key: "TIMEOUT", Value: "90s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].Key != "DATABASE_URL" {
		t.Fatalf("stored %+v", saved)
	}
	// Order is the order they were typed in, so the file guard writes reads the
	// way somebody wrote it.
	if saved[1].Key != "TIMEOUT" {
		t.Fatalf("the order changed: %+v", saved)
	}

	// The machine list carries the count and the dates, never the values: it is
	// read three times a second, and a fleet's passwords do not belong in it.
	loaded, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Env.Count != 2 || loaded.Env.SavedAt.IsZero() {
		t.Fatalf("the node says %+v", loaded.Env)
	}
	if !loaded.Env.Pending() {
		t.Fatal("saved and never injected is pending")
	}

	// Saving replaces the set, because that is what editing a box of lines is.
	saved, err = store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "TIMEOUT", Value: "5s"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Value != "5s" {
		t.Fatalf("after the second save: %+v", saved)
	}
}

func TestBadVariablesAreRefusedWhole(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "ok", Value: "1"}, {Key: "not a key", Value: "2"}}); err == nil {
		t.Fatal("a key with a space in it is not an environment variable")
	}
	_, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "1"}, {Key: "A", Value: "2"}})
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a refusal for the same name twice, got %v", err)
	}
	// And nothing was stored by the attempt.
	vars, err := store.NodeEnv(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 0 {
		t.Fatalf("a refused save wrote %+v", vars)
	}
}

// The lock closes what can reach the machine. Saving is guard-side and stays open;
// injecting is the press that writes to the box, and that is refused.
func TestALockedMachineTakesNoInjection(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnvTargetFor(node.ID); err != nil {
		t.Fatalf("an unlocked machine should have a target: %v", err)
	}

	node.Locked = true
	if _, err := store.SaveNode(node); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "2"}}); err != nil {
		t.Fatalf("editing guard's own copy changes nothing on the box: %v", err)
	}
	if _, err := store.EnvTargetFor(node.ID); err == nil {
		t.Fatal("a locked machine must refuse an injection")
	}
}

func TestThereIsNothingToInjectUntilSomethingIsSaved(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	_, err := store.EnvTargetFor(node.ID)
	if err == nil || !strings.Contains(err.Error(), "save some variables") {
		t.Fatalf("want an answer that says what to do, got %v", err)
	}
}

// The target carries its own machine, so a caller cannot aim one machine's
// variables at another box.
func TestInjectingReadsTheMachineOffTheNode(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	target, err := store.EnvTargetFor(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Login.Address != "10.0.0.5:22" || target.Name != "VPS-1" || len(target.Vars) != 1 {
		t.Fatalf("the target resolved to %+v", target)
	}
}

func TestInjectingIsRecordedSoThePageCanSayTheBoxIsBehind(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnvInjected(node.ID); err != nil {
		t.Fatal(err)
	}
	after, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Env.InjectedAt.IsZero() {
		t.Fatal("the injection should be recorded")
	}
	if after.Env.Pending() {
		t.Fatal("what was just injected is not pending")
	}
	// Save again and the box is behind, which is the whole point of the pair.
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "A", Value: "2"}}); err != nil {
		t.Fatal(err)
	}
	if after, _ = store.Node(node.ID); !after.Env.Pending() {
		t.Fatal("saved after injecting is pending")
	}
}
