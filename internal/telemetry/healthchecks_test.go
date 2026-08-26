package telemetry

import (
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
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

func TestHealthCheckDowntimeClosesOnNextProbeAndSplitsDays(t *testing.T) {
	store := NewStore(100)
	defer store.Close()

	check, err := store.SaveHealthCheck(HealthCheck{
		Name: "API", URL: "https://api.example.com/health", Enabled: true,
		Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 2, 23, 59, 30, 0, time.UTC)
	if err := store.RecordHealthCheck(check.ID, Check{OK: false, CheckedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHealthCheck(check.ID, Check{OK: true, CheckedAt: start.Add(93 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	type rollup struct {
		day, seconds int64
	}
	rows, err := store.rdb.Query(`SELECT day,downtime_seconds FROM health_check_uptime_days
WHERE check_id=? ORDER BY day`, check.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []rollup
	for rows.Next() {
		var row rollup
		if err := rows.Scan(&row.day, &row.seconds); err != nil {
			t.Fatal(err)
		}
		got = append(got, row)
	}
	if len(got) != 2 || got[0].seconds != 30 || got[1].seconds != 63 {
		t.Fatalf("downtime rollups = %#v", got)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	incidents, err := store.HealthIncidents(check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents) != 1 || incidents[0].DurationSeconds != 93 || len(incidents[0].Events) != 1 {
		t.Fatalf("completed incidents = %#v", incidents)
	}
	if err := store.SaveHealthIncident(incidents[0].ID, model.HealthIncidentReport{
		Comment: "Elevated errors on Claude Opus 4.8", Severity: "major", AllocatedMinutes: 1, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}
	incidentID := incidents[0].ID
	if _, err := store.AddHealthIncidentUpdate(model.HealthIncidentUpdateCreate{
		IncidentID: incidentID, Status: "investigating", Message: "We are investigating elevated errors.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddHealthIncidentUpdate(model.HealthIncidentUpdateCreate{
		IncidentID: incidentID, Status: "resolved", Message: "This issue has been resolved.",
	}); err != nil {
		t.Fatal(err)
	}
	status, err := store.PublicStatus()
	if err != nil {
		t.Fatal(err)
	}
	var published int
	var publicUpdates []model.PublicIncidentUpdate
	for _, day := range status.Services[0].Days {
		published += len(day.Incidents)
		if len(day.Incidents) > 0 {
			publicUpdates = day.Incidents[0].Updates
		}
	}
	if published != 1 {
		t.Fatalf("published incident appears on %d days, want 1", published)
	}
	if len(publicUpdates) != 2 || publicUpdates[0].Status != "resolved" || publicUpdates[1].Status != "investigating" {
		t.Fatalf("public incident updates = %#v", publicUpdates)
	}
	incidents, err = store.HealthIncidents(check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents[0].Updates) != 2 || incidents[0].Updates[0].Message != "This issue has been resolved." {
		t.Fatalf("admin incident updates = %#v", incidents[0].Updates)
	}
	resolved := incidents[0].Updates[0]
	if err := store.SaveHealthIncidentUpdate(resolved.ID, model.HealthIncidentUpdateEdit{Status: "monitoring", Message: "Recovery is stable."}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteHealthIncidentUpdate(resolved.ID); err != nil {
		t.Fatal(err)
	}
	incidents, err = store.HealthIncidents(check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(incidents[0].Updates) != 1 || incidents[0].Updates[0].Status != "investigating" {
		t.Fatalf("updates after edit and removal = %#v", incidents[0].Updates)
	}
}

func TestManualHealthIncidentIsADraftForItsDay(t *testing.T) {
	store := NewStore(100)
	defer store.Close()
	check, err := store.SaveHealthCheck(HealthCheck{
		Name: "API", URL: "https://api.example.com/health", Enabled: true, Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(time.Hour)
	if err := store.RecordHealthCheck(check.ID, Check{OK: false, CheckedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordHealthCheck(check.ID, Check{OK: true, CheckedAt: start.Add(40 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	incident, err := store.CreateHealthIncident(model.HealthIncidentCreate{CheckID: check.ID, Day: day})
	if err != nil {
		t.Fatal(err)
	}
	if incident.Confirmed || incident.StartedAt.Format("2006-01-02") != day || !incident.EndedAt.Equal(incident.StartedAt) {
		t.Fatalf("manual incident = %#v", incident)
	}
	if err := store.SaveHealthIncident(incident.ID, model.HealthIncidentReport{Comment: "Provider disruption", Severity: "major", AllocatedMinutes: 18, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateHealthIncident(model.HealthIncidentCreate{CheckID: check.ID, Day: day})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHealthIncident(second.ID, model.HealthIncidentReport{Comment: "Elevated errors", Severity: "partial", AllocatedMinutes: 22, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHealthIncident(incident.ID, model.HealthIncidentReport{Comment: "Provider disruption", Severity: "major", AllocatedMinutes: 23, Confirmed: true}); err == nil {
		t.Fatal("allocated 45 minutes from 40 available")
	}
	status, err := store.PublicStatus()
	if err != nil {
		t.Fatal(err)
	}
	today := status.Services[0].Days[len(status.Services[0].Days)-1]
	if len(today.Incidents) != 2 || today.Incidents[0].Minutes+today.Incidents[1].Minutes != 40 {
		t.Fatalf("public manual incident = %#v", today.Incidents)
	}
	if err := store.DeleteHealthIncident(incident.ID); err != nil {
		t.Fatalf("delete published manual incident: %v", err)
	}
	remaining, err := store.HealthIncidents(check.ID)
	if err != nil {
		t.Fatal(err)
	}
	var foundDeleted, foundSecond bool
	for _, item := range remaining {
		foundDeleted = foundDeleted || item.ID == incident.ID
		foundSecond = foundSecond || item.ID == second.ID
	}
	if foundDeleted || !foundSecond {
		t.Fatalf("incidents after removing published manual row = %#v", remaining)
	}
}
