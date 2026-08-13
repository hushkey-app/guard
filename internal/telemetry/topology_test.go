package telemetry

import (
	"testing"
	"time"
)

func telemetryFrom(t *testing.T, store *Store, service, instance string, attributes map[string]any) {
	t.Helper()
	if err := store.Add(Event{
		Signal: "traces", Service: service, Instance: instance, Name: "GET /api/health",
		Timestamp: time.Now().UTC(), DurationMS: 5, Attributes: attributes,
	}); err != nil {
		t.Fatal(err)
	}
}

// The join is on hosts found in the telemetry: a span served from
// vps-1:8000 belongs to the node watching vps-1:8000.
func TestTopologyGroupsInstancesByHost(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })

	store.SaveNode(Node{Name: "VPS-1", URL: "http://vps-1:8000/api/health", Enabled: true}) //nolint:errcheck
	store.SaveNode(Node{Name: "VPS-2", URL: "https://vps-2.example.com/health", Enabled: true}) //nolint:errcheck

	telemetryFrom(t, store, "web", "web-1", map[string]any{"url.full": "http://vps-1:8000/api/orders"})
	telemetryFrom(t, store, "api", "api-1", map[string]any{"server.address": "vps-2.example.com"})
	// A background worker with no HTTP surface has nothing to match on.
	telemetryFrom(t, store, "worker", "worker-1", map[string]any{"queue.name": "emails"})

	topology, err := store.ClusterTopology()
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Groups) != 2 {
		t.Fatalf("%d groups, want one per node", len(topology.Groups))
	}
	if len(topology.Groups[0].Instances) != 1 || topology.Groups[0].Instances[0].Service != "web" {
		t.Errorf("VPS-1 got %#v", topology.Groups[0].Instances)
	}
	if len(topology.Groups[1].Instances) != 1 || topology.Groups[1].Instances[0].Service != "api" {
		t.Errorf("VPS-2 got %#v", topology.Groups[1].Instances)
	}
	// Unplaced, not hidden: a service quietly filed under the wrong machine is
	// worse than one openly filed under none.
	if len(topology.Unassigned) != 1 || topology.Unassigned[0].Service != "worker" {
		t.Errorf("unassigned = %#v", topology.Unassigned)
	}
	// The evidence travels with the answer so a wrong grouping can be argued
	// with rather than only disbelieved.
	if len(topology.Groups[0].Hosts) == 0 {
		t.Error("no hosts reported for the match")
	}
}

// Two nodes on the same host and different ports are two machines as far as
// anyone watching them is concerned.
func TestTopologyPrefersTheExactHostAndPort(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	store.SaveNode(Node{Name: "app", URL: "http://localhost:8000/health", Enabled: true})   //nolint:errcheck
	store.SaveNode(Node{Name: "admin", URL: "http://localhost:9000/health", Enabled: true}) //nolint:errcheck

	telemetryFrom(t, store, "app-svc", "a1", map[string]any{"url.full": "http://localhost:8000/api/x"})
	telemetryFrom(t, store, "admin-svc", "b1", map[string]any{"url.full": "http://localhost:9000/api/y"})

	topology, err := store.ClusterTopology()
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[string][]string{}
	for _, group := range topology.Groups {
		for _, instance := range group.Instances {
			byNode[group.Node.Name] = append(byNode[group.Node.Name], instance.Service)
		}
	}
	if len(byNode["admin"]) != 1 || byNode["admin"][0] != "admin-svc" {
		t.Fatalf("admin got %v — the port has to decide", byNode["admin"])
	}
	if len(byNode["app"]) != 1 || byNode["app"][0] != "app-svc" {
		t.Fatalf("app got %v", byNode["app"])
	}
}

// A node URL written without a port still matches telemetry that records the
// default one.
func TestTopologyMatchesWhenOnlyOneSideHasAPort(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	store.SaveNode(Node{Name: "VPS-1", URL: "https://vps-1.example.com/health", Enabled: true}) //nolint:errcheck
	telemetryFrom(t, store, "web", "web-1", map[string]any{"url.full": "https://vps-1.example.com:443/api/x"})

	topology, err := store.ClusterTopology()
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Groups[0].Instances) != 1 {
		t.Fatalf("port-only difference left it unassigned: %#v", topology)
	}
}

// With nothing declared, everything is unassigned — and says so rather than
// inventing a machine to hang it on.
func TestTopologyWithoutNodes(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	telemetryFrom(t, store, "web", "web-1", map[string]any{"url.full": "http://localhost:8000/x"})

	topology, err := store.ClusterTopology()
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Groups) != 0 || len(topology.Unassigned) != 1 {
		t.Fatalf("topology = %#v", topology)
	}
}

func TestHostForms(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		exact, loose string
	}{
		{"http://localhost:8000/api/health", "localhost:8000", "localhost"},
		{"https://vps-1.example.com/health", "vps-1.example.com", ""},
		{"http://VPS-1.example.com:9000/", "vps-1.example.com:9000", "vps-1.example.com"},
		{"http://[::1]:8000/health", "[::1]:8000", "[::1]"},
	} {
		exact, loose := hostsOfURL(tc.raw)
		if !exact[tc.exact] {
			t.Errorf("%s: exact = %v, want %q", tc.raw, exact, tc.exact)
		}
		if tc.loose != "" && !loose[tc.loose] {
			t.Errorf("%s: loose = %v, want %q", tc.raw, loose, tc.loose)
		}
	}
}
