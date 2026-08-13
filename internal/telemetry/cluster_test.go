package telemetry

import (
	"strings"
	"testing"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

func TestNodeLifecycle(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "  VPS-1  ", URL: " https://vps-1.example.com/api/health ", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Trimmed, because a trailing space in a URL is a check that fails for a
	// reason nobody can see on screen.
	if node.Name != "VPS-1" || node.URL != "https://vps-1.example.com/api/health" {
		t.Fatalf("stored %q at %q", node.Name, node.URL)
	}
	if node.Status != model.StatusUnknown {
		t.Errorf("a node nobody has checked is %q, want unknown", node.Status)
	}

	node.Name = "VPS-1 (eu)"
	node.Enabled = false
	updated, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "VPS-1 (eu)" || updated.Enabled {
		t.Fatalf("update did not stick: %#v", updated)
	}

	if err := store.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(node.ID); err == nil {
		t.Error("deleting a missing node reported success")
	}
}

// Guard fetches these URLs on a timer from inside whatever network it runs in.
// The allowlist is the whole safety story, so it is worth a test of its own.
func TestNodeURLValidation(t *testing.T) {
	for _, bad := range []string{
		"", "   ", "not-a-url", "/api/health", "//example.com/health",
		"file:///etc/passwd", "ftp://example.com", "gopher://example.com",
		"javascript:alert(1)", "http://", strings.Repeat("https://example.com/", 200),
	} {
		if err := model.ValidateNodeURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{
		"http://localhost:8000/api/health",
		"https://vps-1.example.com/health",
		"https://10.0.0.4:9000/",
	} {
		if err := model.ValidateNodeURL(good); err != nil {
			t.Errorf("%q was rejected: %v", good, err)
		}
	}
	if err := (Node{Name: "", URL: "https://example.com"}).Validate(); err == nil {
		t.Error("a node with no name was accepted")
	}
}

func TestChecksDriveStatusAndUptime(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, err := store.SaveNode(Node{Name: "VPS-1", URL: "https://example.com/health", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	checks := []Check{
		{OK: true, StatusCode: 200, LatencyMS: 12, CheckedAt: now.Add(-3 * time.Minute)},
		{OK: false, StatusCode: 502, LatencyMS: 30, Error: "502 Bad Gateway", CheckedAt: now.Add(-2 * time.Minute)},
		{OK: true, StatusCode: 200, LatencyMS: 9, CheckedAt: now.Add(-time.Minute)},
		{OK: true, StatusCode: 200, LatencyMS: 11, CheckedAt: now},
	}
	for _, check := range checks {
		if err := store.RecordCheck(node.ID, check); err != nil {
			t.Fatal(err)
		}
	}

	read, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The latest check decides the status, not the majority.
	if read.Status != model.StatusUp || read.StatusCode != 200 || read.LatencyMS != 11 {
		t.Fatalf("status = %#v", read)
	}
	if read.Checks != 4 || read.Uptime != 75 {
		t.Fatalf("uptime = %.1f%% over %d checks, want 75%% over 4", read.Uptime, read.Checks)
	}
	// Oldest first: the strip is read left to right like everything else.
	if len(read.History) != 4 || read.History[0] != 1 || read.History[1] != 0 {
		t.Fatalf("history = %v", read.History)
	}
	// A failing check keeps its reason, or the dashboard says "down" and
	// nothing else.
	store.RecordCheck(node.ID, Check{OK: false, Error: "connection refused — nothing is listening", CheckedAt: now.Add(time.Minute)}) //nolint:errcheck
	read, _ = store.Node(node.ID)
	if read.Status != model.StatusDown || !strings.Contains(read.Error, "refused") {
		t.Fatalf("down node = %#v", read)
	}
}

// Checks are meaningless without the node they were of, and rows nothing can
// read are rows that only grow.
func TestDeletingANodeForgetsItsChecks(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, _ := store.SaveNode(Node{Name: "VPS-1", URL: "https://example.com/health", Enabled: true})
	for range 5 {
		store.RecordCheck(node.ID, Check{OK: true, StatusCode: 200}) //nolint:errcheck
	}
	if err := store.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cluster_checks WHERE node_id = ?`, node.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d checks outlived their node", left)
	}
}

func TestClusterSummary(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	up, _ := store.SaveNode(Node{Name: "A-up", URL: "https://example.com/a", Enabled: true})
	down, _ := store.SaveNode(Node{Name: "B-down", URL: "https://example.com/b", Enabled: true})
	store.SaveNode(Node{Name: "C-new", URL: "https://example.com/c", Enabled: true}) //nolint:errcheck
	store.RecordCheck(up.ID, Check{OK: true, StatusCode: 200})                       //nolint:errcheck
	store.RecordCheck(down.ID, Check{OK: false, Error: "timed out"})                 //nolint:errcheck

	summary, err := store.ClusterSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Nodes != 3 || summary.Up != 1 || summary.Down != 1 || summary.Unknown != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Worst != "B-down" {
		t.Errorf("worst = %q, want the failing node", summary.Worst)
	}
}
