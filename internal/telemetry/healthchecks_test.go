package telemetry

import (
	"testing"
	"time"
)

func TestHealthChecksAreIndependentAndPrivateByDefault(t *testing.T) {
	store := NewStore(100)
	defer store.Close()

	check, err := store.SaveHealthCheck(HealthCheck{
		Name: "Load balancer", URL: "https://lb.example.com/health", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if check.NodeID != 0 || check.Public {
		t.Fatalf("standalone check = node %d public %v", check.NodeID, check.Public)
	}
	if err := store.RecordHealthCheck(check.ID, Check{OK: true, StatusCode: 204, CheckedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	read, err := store.HealthCheck(check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != "up" || read.StatusCode != 204 || read.Checks != 1 {
		t.Fatalf("check after result = %#v", read)
	}
	status, err := store.PublicStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 0 {
		t.Fatalf("private check appeared publicly: %#v", status.Services)
	}

	read.Public = true
	read.PublicName = "API"
	if _, err := store.SaveHealthCheck(read); err != nil {
		t.Fatal(err)
	}
	status, err = store.PublicStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Services) != 1 || status.Services[0].Name != "API" || status.Services[0].State != "operational" {
		t.Fatalf("published status = %#v", status.Services)
	}
}

func TestExistingMachineProbeIsLiftedIntoChecks(t *testing.T) {
	store := NewStore(100)
	defer store.Close()

	node, err := store.SaveNode(Node{
		Name: "VPS-1", Domain: "https://api.example.com", HealthPath: "/health",
		Enabled: true, Public: true, PublicName: "API",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCheck(node.ID, Check{OK: true, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	// Model a database from before dedicated checks existed. NewStore has already
	// run current migrations, so remove only the new tables from this in-memory
	// fixture before exercising the upgrade.
	if _, err := store.db.Exec(`
DROP TABLE health_check_uptime_days;
DROP TABLE health_check_results;
DROP TABLE health_checks;`); err != nil {
		t.Fatal(err)
	}
	if err := migrateHealthChecks(store.db); err != nil {
		t.Fatal(err)
	}
	checks, err := store.HealthChecks()
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].NodeID != node.ID || checks[0].URL != "https://api.example.com/health" || !checks[0].Public {
		t.Fatalf("lifted checks = %#v", checks)
	}
	if checks[0].Checks != 1 || checks[0].Status != "up" {
		t.Fatalf("lifted history = %#v", checks[0])
	}

	// Once the dedicated table exists, deletion is durable. This is the restart
	// path that used to import the legacy machine URL again.
	if err := store.DeleteHealthCheck(checks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := migrateHealthChecks(store.db); err != nil {
		t.Fatal(err)
	}
	checks, err = store.HealthChecks()
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Fatalf("deleted check returned after migration: %#v", checks)
	}
}

func TestSeveralHealthChecksCanUseTheSameMachine(t *testing.T) {
	store := NewStore(100)
	defer store.Close()

	node, err := store.SaveNode(Node{Name: "VPS-1", SSHAddress: "root@10.0.0.12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []struct {
		name string
		url  string
	}{
		{name: "API", url: "http://10.0.0.12:8080/health"},
		{name: "Worker", url: "http://10.0.0.12:9090/ready"},
	} {
		check, err := store.SaveHealthCheck(HealthCheck{
			Name: service.name, URL: service.url, NodeID: node.ID, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if check.NodeID != node.ID {
			t.Fatalf("%s machine = %d, want %d", service.name, check.NodeID, node.ID)
		}
	}
}
