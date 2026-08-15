package vultr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hushkey-app/guard/internal/cloud"
)

// fake stands in for both halves of the provider: the account API and the
// Harbor answering as the registry itself. One server, because the account
// API's answer points at the registry's base URL and the test wants that
// pointing to be exercised, not stubbed.
func fake(t *testing.T) (*httptest.Server, *Client, map[string]int) {
	t.Helper()
	calls := map[string]int{}
	mux := http.NewServeMux()
	var server *httptest.Server

	mux.HandleFunc("GET /v2/registries", func(w http.ResponseWriter, r *http.Request) {
		calls["registries"]++
		if r.Header.Get("Authorization") != "Bearer good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"registries": []map[string]any{{
				"id": "reg-1", "name": "hushkey", "urn": "syd.example/hushkey",
				"date_created": "2026-07-08 14:07:15", "public": false,
				"storage": map[string]any{
					"used":    map[string]any{"bytes": 1024},
					"allowed": map[string]any{"bytes": 2048},
				},
				"root_user": map[string]any{"username": "robot", "password": "hunter2"},
				"metadata": map[string]any{"region": map[string]any{
					"name": "syd", "base_url": server.URL,
				}},
			}},
			"meta": map[string]any{"links": map[string]any{"next": ""}},
		})
	})
	mux.HandleFunc("GET /v2/registry/reg-1/repositories", func(w http.ResponseWriter, r *http.Request) {
		calls["repositories"]++
		json.NewEncoder(w).Encode(map[string]any{
			"repositories": []map[string]any{{
				"name": "hushkey/pack", "image": "cGFjaw", "pull_count": 34, "artifact_count": 2,
			}},
			"meta": map[string]any{"links": map[string]any{"next": ""}},
		})
	})
	mux.HandleFunc("DELETE /v2/registry/reg-1/repository/cGFjaw", func(w http.ResponseWriter, r *http.Request) {
		calls["repo-delete"]++
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /v2/registry", func(w http.ResponseWriter, r *http.Request) {
		calls["create"]++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] == "" || body["region"] != "syd" || body["plan"] != "start_up" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// The single-object endpoints wrap their answer; the list endpoint
		// uses a different key. Both are decoded, so the test uses the one
		// the provider actually sends here.
		json.NewEncoder(w).Encode(map[string]any{"registry": map[string]any{
			"id": "reg-2", "name": body["name"], "urn": "syd.example/" + body["name"].(string),
			"date_created": "2026-08-15 02:00:00", "public": body["public"],
			"metadata": map[string]any{"region": map[string]any{"name": "syd"}},
		}})
	})
	mux.HandleFunc("DELETE /v2/registry/reg-2", func(w http.ResponseWriter, r *http.Request) {
		calls["registry-delete"]++
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v2/registry/region/list", func(w http.ResponseWriter, r *http.Request) {
		calls["regions"]++
		json.NewEncoder(w).Encode(map[string]any{"regions": []map[string]any{{
			"id": 1, "name": "syd", "urn": "syd.vultrcr.com", "base_url": "https://syd.vultrcr.com",
			"data_center": map[string]any{"city": "Sydney", "country": "AU"},
		}}})
	})
	mux.HandleFunc("GET /v2/registry/plan/list", func(w http.ResponseWriter, r *http.Request) {
		calls["plans"]++
		json.NewEncoder(w).Encode(map[string]any{"plans": map[string]any{
			"business":   map[string]any{"vanity_name": "Business", "max_storage_mb": 102400, "monthly_price": 25},
			"start_up":   map[string]any{"vanity_name": "Start Up", "max_storage_mb": 10240, "monthly_price": 5},
			"enterprise": map[string]any{"vanity_name": "Enterprise", "max_storage_mb": 512000, "monthly_price": 100},
		}})
	})
	mux.HandleFunc("GET /service/token", func(w http.ResponseWriter, r *http.Request) {
		calls["token"]++
		user, password, _ := r.BasicAuth()
		if user != "robot" || password != "hunter2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls["scope:"+r.URL.Query().Get("scope")]++
		json.NewEncoder(w).Encode(map[string]string{"token": "harbor-token"})
	})
	mux.HandleFunc("GET /v2/hushkey/pack/tags/list", func(w http.ResponseWriter, r *http.Request) {
		calls["tags"]++
		if r.Header.Get("Authorization") != "Bearer harbor-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "hushkey/pack", "tags": []string{"latest", "v1"}})
	})
	mux.HandleFunc("GET /v2/hushkey/pack/manifests/", func(w http.ResponseWriter, r *http.Request) {
		calls["manifest"]++
		w.Header().Set("Docker-Content-Digest", "sha256:feed")
		json.NewEncoder(w).Encode(map[string]any{
			"config": map[string]any{"size": 100},
			"layers": []map[string]any{{"size": 400}, {"size": 500}},
		})
	})
	mux.HandleFunc("DELETE /v2/hushkey/pack/manifests/sha256:feed", func(w http.ResponseWriter, r *http.Request) {
		calls["manifest-delete"]++
		w.WriteHeader(http.StatusAccepted)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, NewFor(server.URL, server.Client()), calls
}

func TestRegistriesAndRepositories(t *testing.T) {
	_, client, _ := fake(t)
	ctx := context.Background()

	registries, err := client.Registries(ctx, "good-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(registries) != 1 || registries[0].Name != "hushkey" || registries[0].StorageAllowedBytes != 2048 {
		t.Fatalf("registries: %+v", registries)
	}

	repos, err := client.Repositories(ctx, "good-key", "reg-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "hushkey/pack" || repos[0].Image != "cGFjaw" {
		t.Fatalf("repositories: %+v", repos)
	}
}

func TestABadKeyIsRefusedInWords(t *testing.T) {
	_, client, _ := fake(t)
	if _, err := client.Registries(context.Background(), "wrong"); err == nil {
		t.Fatal("a refused key did not error")
	}
}

func TestTagsRunTheTokenFlow(t *testing.T) {
	_, client, calls := fake(t)
	ctx := context.Background()
	registries, err := client.Registries(ctx, "good-key")
	if err != nil {
		t.Fatal(err)
	}

	tags, err := client.Tags(ctx, registries[0], "hushkey/pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "latest" || tags[0].Digest != "sha256:feed" || tags[0].SizeBytes != 1000 {
		t.Fatalf("tags: %+v", tags)
	}
	if calls["scope:repository:hushkey/pack:pull"] == 0 {
		t.Fatal("the tag listing did not ask for a pull-scoped token")
	}
}

func TestDeletesAddressTheRightThings(t *testing.T) {
	_, client, calls := fake(t)
	ctx := context.Background()
	registries, err := client.Registries(ctx, "good-key")
	if err != nil {
		t.Fatal(err)
	}

	// The repository goes through the account API, by image token.
	if err := client.DeleteRepository(ctx, "good-key", "reg-1", "cGFjaw"); err != nil {
		t.Fatal(err)
	}
	if calls["repo-delete"] != 1 {
		t.Fatal("the repository delete did not reach the provider")
	}

	// The tag goes through the registry: resolve the digest, delete it,
	// with a delete-scoped token.
	if err := client.DeleteTag(ctx, registries[0], "hushkey/pack", "latest"); err != nil {
		t.Fatal(err)
	}
	if calls["manifest-delete"] != 1 {
		t.Fatal("the manifest delete did not reach the registry")
	}
	if calls["scope:repository:hushkey/pack:pull,delete"] == 0 {
		t.Fatal("the tag delete did not ask for a delete-scoped token")
	}
}

func TestCreateAndDeleteARegistry(t *testing.T) {
	_, client, calls := fake(t)
	ctx := context.Background()

	made, err := client.CreateRegistry(ctx, "good-key", "hushkey-2", "syd", "start_up", false)
	if err != nil {
		t.Fatal(err)
	}
	if made.ID != "reg-2" || made.Name != "hushkey-2" || made.Region != "syd" {
		t.Fatalf("created %+v", made)
	}
	if calls["create"] != 1 {
		t.Fatalf("calls were %v", calls)
	}

	if err := client.DeleteRegistry(ctx, "good-key", "reg-2"); err != nil {
		t.Fatal(err)
	}
	if calls["registry-delete"] != 1 {
		t.Fatalf("calls were %v", calls)
	}
}

// The plans come back as an object keyed by name rather than a list, so the
// order is imposed here — and it has to be cheapest first, because that is
// the order somebody reads a price list in.
func TestRegistryOptionsArePricedCheapestFirst(t *testing.T) {
	_, client, _ := fake(t)
	options, err := Provider(client).(cloud.RegistryMaker).RegistryOptions(
		context.Background(), cloud.Credentials{Key: "good-key"})
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Regions) != 1 || options.Regions[0].ID != "syd" {
		t.Fatalf("regions: %+v", options.Regions)
	}
	// The code is what a URN carries; the city is what a person is choosing.
	if options.Regions[0].Name != "syd — Sydney" {
		t.Fatalf("region was labelled %q", options.Regions[0].Name)
	}
	got := make([]string, 0, len(options.Plans))
	for _, plan := range options.Plans {
		got = append(got, plan.ID)
	}
	if len(got) != 3 || got[0] != "start_up" || got[2] != "enterprise" {
		t.Fatalf("plans came back as %v", got)
	}
}

// Vultr implements every half of the provider vocabulary. This is the test
// that fails if one is dropped, rather than a page quietly losing a button.
func TestVultrClaimsEverythingItImplements(t *testing.T) {
	_, client, _ := fake(t)
	described := cloud.Describe(Provider(client))
	for name, claimed := range map[string]bool{
		"registries":     described.Capabilities.Registries,
		"registry maker": described.Capabilities.RegistryMaker,
		"storage":        described.Capabilities.Storage,
		"storage rename": described.Capabilities.StorageRename,
		"storage keys":   described.Capabilities.StorageKeys,
		"compute":        described.Capabilities.Compute,
	} {
		if !claimed {
			t.Fatalf("vultr should do %s", name)
		}
	}
	if described.Capabilities.NeedsAccountID {
		t.Fatal("a vultr key names its own account — nothing else should be asked for")
	}
}

// The adapter addresses a registry by id and looks its docker credentials up
// again on the way through, so that a caller never holds one.
func TestTheAdapterResolvesTheRegistryItself(t *testing.T) {
	_, client, calls := fake(t)
	tags, err := Provider(client).(cloud.Registries).Tags(
		context.Background(), cloud.Credentials{Key: "good-key"}, "reg-1", "hushkey/pack")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags: %+v", tags)
	}
	if calls["registries"] == 0 {
		t.Fatal("the adapter did not resolve the registry for itself")
	}
}
