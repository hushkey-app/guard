package vultr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCompute is the account API's compute and storage halves: enough of the
// real shapes that the decoding, the paging and the credential handling are
// exercised rather than described.
func fakeCompute(t *testing.T) (*Client, map[string]int, map[string]any) {
	t.Helper()
	calls := map[string]int{}
	bodies := map[string]any{}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v2/instances", func(w http.ResponseWriter, r *http.Request) {
		calls["instances"]++
		// Two pages, so a client that forgets the cursor shows half a fleet.
		if r.URL.Query().Get("cursor") == "" {
			json.NewEncoder(w).Encode(map[string]any{
				"instances": []map[string]any{{
					"id": "i-1", "label": "vps-1", "os": "Ubuntu 24.04", "region": "syd",
					"plan": "vc2-1c-1gb", "main_ip": "203.0.113.7", "internal_ip": "0.0.0.0",
					"vcpu_count": 1, "ram": 1024, "disk": 25, "allowed_bandwidth": 1000,
					"status": "active", "power_status": "running", "server_status": "ok",
					"date_created": "2026-07-08T14:07:15+00:00", "tags": []string{"api"},
				}},
				"meta": map[string]any{"links": map[string]any{"next": "page-2"}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"instances": []map[string]any{{
				"id": "i-2", "label": "alpha", "region": "ewr", "plan": "vc2-2c-4gb",
				"main_ip": "0.0.0.0", "status": "pending", "power_status": "running",
				"server_status": "installingbooting",
			}},
			"meta": map[string]any{"links": map[string]any{"next": ""}},
		})
	})
	mux.HandleFunc("GET /v2/instances/i-1", func(w http.ResponseWriter, r *http.Request) {
		calls["instance"]++
		json.NewEncoder(w).Encode(map[string]any{"instance": map[string]any{
			"id": "i-1", "label": "vps-1", "power_status": "running", "allowed_bandwidth": 1000,
		}})
	})
	mux.HandleFunc("POST /v2/instances/i-1/halt", func(w http.ResponseWriter, r *http.Request) {
		calls["halt"]++
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v2/instances/i-1/bandwidth", func(w http.ResponseWriter, r *http.Request) {
		calls["bandwidth"]++
		json.NewEncoder(w).Encode(map[string]any{"bandwidth": map[string]any{
			"2026-08-02": map[string]any{"incoming_bytes": 20, "outgoing_bytes": 2},
			"2026-08-01": map[string]any{"incoming_bytes": 10, "outgoing_bytes": 1},
		}})
	})
	mux.HandleFunc("GET /v2/instances/gone", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	mux.HandleFunc("GET /v2/snapshots", func(w http.ResponseWriter, r *http.Request) {
		calls["snapshots"]++
		json.NewEncoder(w).Encode(map[string]any{
			"snapshots": []map[string]any{
				{"id": "s-old", "description": "last week", "date_created": "2026-08-01T00:00:00+00:00", "size": 100, "status": "complete"},
				{"id": "s-new", "description": "before the deploy", "date_created": "2026-08-13T00:00:00+00:00", "size": 200, "status": "pending"},
			},
			"meta": map[string]any{"links": map[string]any{"next": ""}},
		})
	})
	mux.HandleFunc("POST /v2/snapshots", func(w http.ResponseWriter, r *http.Request) {
		calls["snapshot-create"]++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies["snapshot-create"] = body
		json.NewEncoder(w).Encode(map[string]any{"snapshot": map[string]any{
			"id": "s-fresh", "description": body["description"], "status": "pending",
		}})
	})
	mux.HandleFunc("PATCH /v2/snapshots/s-old", func(w http.ResponseWriter, r *http.Request) {
		calls["snapshot-update"]++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies["snapshot-update"] = body
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v2/instances/i-1/restore", func(w http.ResponseWriter, r *http.Request) {
		calls["restore"]++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies["restore"] = body
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("GET /v2/object-storage", func(w http.ResponseWriter, r *http.Request) {
		calls["storage"]++
		json.NewEncoder(w).Encode(map[string]any{
			"object_storages": []map[string]any{{
				"id": "os-1", "label": "backups", "cluster_id": 2, "region": "syd",
				"status": "active", "s3_hostname": "syd1.vultrobjects.com",
				"s3_access_key": "AKIA", "s3_secret_key": "shhh",
				"date_created": "2026-07-08 14:07:15",
			}},
			"meta": map[string]any{"links": map[string]any{"next": ""}},
		})
	})
	mux.HandleFunc("POST /v2/object-storage/os-1/regenerate-keys", func(w http.ResponseWriter, r *http.Request) {
		calls["regenerate"]++
		json.NewEncoder(w).Encode(map[string]any{"s3_credentials": map[string]any{
			"s3_hostname": "syd1.vultrobjects.com", "s3_access_key": "AKIA2", "s3_secret_key": "shhh2",
		}})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return NewFor(server.URL, server.Client()), calls, bodies
}

func TestInstancesPageAndNormalise(t *testing.T) {
	client, calls, _ := fakeCompute(t)
	instances, err := client.Instances(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("wanted both pages, got %d instances", len(instances))
	}
	if calls["instances"] != 2 {
		t.Fatalf("the cursor was not followed: %d calls", calls["instances"])
	}
	// Sorted by label, so a picker does not reshuffle between openings.
	if instances[0].Label != "alpha" {
		t.Fatalf("unsorted: %s first", instances[0].Label)
	}
	// A machine still installing reports 0.0.0.0, which is not an address
	// anybody can use and must not be rendered as one.
	if instances[0].MainIP != "" {
		t.Fatalf("0.0.0.0 survived as %q", instances[0].MainIP)
	}
	vps := instances[1]
	if vps.RAMMB != 1024 || vps.AllowedBandwidthGB != 1000 || vps.Created.IsZero() || len(vps.Tags) != 1 {
		t.Fatalf("decoded oddly: %#v", vps)
	}
}

func TestBandwidthIsSummedAndOrdered(t *testing.T) {
	client, _, _ := fakeCompute(t)
	transfer, err := client.BandwidthFor(context.Background(), "key", "i-1")
	if err != nil {
		t.Fatal(err)
	}
	if transfer.In != 30 || transfer.Out != 3 {
		t.Fatalf("totals are %d/%d", transfer.In, transfer.Out)
	}
	// The provider answers a map, which has no order; a chart needs one.
	if len(transfer.Days) != 2 || transfer.Days[0].Date != "2026-08-01" {
		t.Fatalf("days out of order: %#v", transfer.Days)
	}
}

func TestPowerIsAClosedList(t *testing.T) {
	client, calls, _ := fakeCompute(t)
	if err := client.Power(context.Background(), "key", "i-1", "halt"); err != nil {
		t.Fatal(err)
	}
	if calls["halt"] != 1 {
		t.Fatal("halt did not reach the provider")
	}
	// The action is pasted into a URL path, so anything not on the list is
	// refused here rather than sent.
	if err := client.Power(context.Background(), "key", "i-1", "../../delete"); err == nil {
		t.Fatal("an invented power action was sent")
	}
}

func TestSnapshotsAreNewestFirstAndCreateCarriesTheInstance(t *testing.T) {
	client, calls, bodies := fakeCompute(t)
	ctx := context.Background()
	list, err := client.Snapshots(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "s-new" {
		t.Fatalf("not newest first: %#v", list)
	}
	if list[0].Status != "pending" {
		t.Fatalf("status lost: %#v", list[0])
	}
	fresh, err := client.CreateSnapshot(ctx, "key", "i-1", "before the deploy")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID != "s-fresh" {
		t.Fatalf("create answered %#v", fresh)
	}
	body, _ := bodies["snapshot-create"].(map[string]any)
	if body["instance_id"] != "i-1" || body["description"] != "before the deploy" {
		t.Fatalf("create sent %#v", body)
	}
	if err := client.UpdateSnapshot(ctx, "key", "s-old", "vps-1_25-08-2026"); err != nil {
		t.Fatal(err)
	}
	if calls["snapshot-update"] != 1 {
		t.Fatal("snapshot update did not reach the provider")
	}
	updated, _ := bodies["snapshot-update"].(map[string]any)
	if updated["description"] != "vps-1_25-08-2026" {
		t.Fatalf("update sent %#v", updated)
	}
}

func TestRestoreNamesTheSnapshot(t *testing.T) {
	client, calls, bodies := fakeCompute(t)
	if err := client.Restore(context.Background(), "key", "i-1", "s-old"); err != nil {
		t.Fatal(err)
	}
	if calls["restore"] != 1 {
		t.Fatal("restore did not reach the provider")
	}
	body, _ := bodies["restore"].(map[string]any)
	if body["snapshot_id"] != "s-old" {
		t.Fatalf("restore sent %#v", body)
	}
}

// A gone instance and an unhappy provider are different sentences, and the
// endpoint layer turns only the first into a 404.
func TestMissingThingsAreNamedAsMissing(t *testing.T) {
	client, _, _ := fakeCompute(t)
	_, err := client.Instance(context.Background(), "key", "gone")
	if err != ErrNotFound {
		t.Fatalf("a missing instance answered %v", err)
	}
}

// The S3 secret is in every listing the provider answers. It must not be in
// anything guard hands out by default — only through Credentials, which is
// one method with one caller.
func TestObjectStorageKeepsItsKeysUnexported(t *testing.T) {
	client, _, _ := fakeCompute(t)
	ctx := context.Background()
	list, err := client.ObjectStorages(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].HasKeys || list[0].Hostname == "" {
		t.Fatalf("storage decoded oddly: %#v", list)
	}
	encoded, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"AKIA", "shhh"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("a credential rode along in the JSON: %s", encoded)
		}
	}
	access, secret := list[0].Credentials()
	if access != "AKIA" || secret != "shhh" {
		t.Fatalf("Credentials answered %q/%q", access, secret)
	}

	rotated, err := client.RegenerateObjectStorageKeys(ctx, "key", "os-1")
	if err != nil {
		t.Fatal(err)
	}
	if access, secret := rotated.Credentials(); access != "AKIA2" || secret != "shhh2" {
		t.Fatalf("rotation answered %q/%q", access, secret)
	}
}
