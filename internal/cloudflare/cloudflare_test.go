package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/cloud"
)

const account = "2f8d88f16ef3c99b0f298d16b466fcb3"

var good = cloud.Credentials{Key: "good-token", Account: account}

// fake stands in for the account API, answering the way the real one does:
// everything wrapped in an envelope, a cursor for the second page, and the
// bucket listing deliberately thinner than the bucket read — which is the
// reason the client reads each bucket again.
func fake(t *testing.T) (*httptest.Server, *Client, map[string]int) {
	t.Helper()
	calls := map[string]int{}
	mux := http.NewServeMux()
	base := "/accounts/" + account

	ok := func(w http.ResponseWriter, result any, cursor string) {
		answer := map[string]any{"success": true, "errors": []any{}, "result": result}
		if cursor != "" {
			answer["result_info"] = map[string]any{"cursor": cursor, "per_page": 100}
		}
		json.NewEncoder(w).Encode(answer)
	}
	fail := func(w http.ResponseWriter, status, code int, message string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{
			"success": false, "result": nil,
			"errors": []map[string]any{{"code": code, "message": message}},
		})
	}

	mux.HandleFunc("GET "+base+"/r2/buckets", func(w http.ResponseWriter, r *http.Request) {
		calls["list"]++
		if r.Header.Get("Authorization") != "Bearer good-token" {
			fail(w, http.StatusBadRequest, 9106, "Authentication failed (status: 400)")
			return
		}
		// Two pages, so the cursor is exercised rather than assumed.
		if r.URL.Query().Get("cursor") == "" {
			ok(w, map[string]any{"buckets": []map[string]any{
				{"name": "pack-dev", "creation_date": "2026-07-07T02:29:32.555Z"},
			}}, "page-2")
			return
		}
		ok(w, map[string]any{"buckets": []map[string]any{
			{"name": "hushkey-dev", "creation_date": "2026-03-10T15:33:26.175Z"},
		}}, "")
	})
	mux.HandleFunc("GET "+base+"/r2/buckets/{name}", func(w http.ResponseWriter, r *http.Request) {
		calls["read"]++
		name := r.PathValue("name")
		if name == "gone" {
			fail(w, http.StatusNotFound, bucketMissing, "The specified bucket does not exist.")
			return
		}
		ok(w, map[string]any{
			"name": name, "creation_date": "2026-07-07T02:29:32.555Z",
			"location": "APAC", "storage_class": "Standard", "jurisdiction": "default",
		}, "")
	})
	mux.HandleFunc("GET "+base+"/r2/buckets/{name}/usage", func(w http.ResponseWriter, r *http.Request) {
		calls["usage"]++
		ok(w, map[string]any{"payloadSize": "20616785", "objectCount": "17"}, "")
	})
	mux.HandleFunc("POST "+base+"/r2/buckets", func(w http.ResponseWriter, r *http.Request) {
		calls["create"]++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		// The request spells them camelCase and the answer spells them with
		// underscores. Both are the provider's, and getting either wrong is
		// a bucket in the wrong place.
		if body["locationHint"] != "oc" || body["storageClass"] != "Standard" {
			fail(w, http.StatusBadRequest, 10001, "bad create body")
			return
		}
		ok(w, map[string]any{
			"name": body["name"], "creation_date": "2026-08-15T01:00:00.000Z",
			"location": "OC", "storage_class": "Standard",
		}, "")
	})
	mux.HandleFunc("DELETE "+base+"/r2/buckets/{name}", func(w http.ResponseWriter, r *http.Request) {
		calls["delete"]++
		ok(w, map[string]any{}, "")
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, NewFor(server.URL, "registry.invalid", server.Client()), calls
}

func TestBucketsReadEveryPageAndEnrichEachBucket(t *testing.T) {
	_, client, calls := fake(t)
	got, err := client.Buckets(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, wanted both pages", len(got))
	}
	// Sorted by label, so the second page's bucket comes first.
	if got[0].ID != "hushkey-dev" || got[1].ID != "pack-dev" {
		t.Fatalf("buckets came back as %q, %q", got[0].ID, got[1].ID)
	}
	// The listing carries neither of these; only the per-bucket read does.
	if got[0].Region != "APAC" || got[0].Class != "Standard" {
		t.Fatalf("bucket was not enriched: %+v", got[0])
	}
	if got[0].UsedBytes != 20616785 || got[0].Objects != 17 {
		t.Fatalf("usage did not arrive: %+v", got[0])
	}
	if got[0].Hostname != "https://"+account+".r2.cloudflarestorage.com" {
		t.Fatalf("endpoint was %q", got[0].Hostname)
	}
	// Nothing here can carry a credential, and the card must not draw dots
	// over a pair that is never coming.
	if got[0].HasKeys {
		t.Fatal("an R2 bucket claimed to have keys")
	}
	if calls["list"] != 2 || calls["read"] != 2 || calls["usage"] != 2 {
		t.Fatalf("calls were %v", calls)
	}
}

func TestABucketThatCannotBeReadKeepsItsName(t *testing.T) {
	_, client, _ := fake(t)
	if _, err := client.Bucket(context.Background(), good, "gone"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("a missing bucket said %v, wanted ErrNotFound", err)
	}
}

func TestCreateSendsTheProvidersOwnSpelling(t *testing.T) {
	_, client, calls := fake(t)
	made, err := client.CreateBucket(context.Background(), good, cloud.StorageSpec{
		Label: "guard-test", Region: "oc", Class: "Standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if made.ID != "guard-test" || made.Region != "OC" {
		t.Fatalf("created %+v", made)
	}
	if calls["create"] != 1 {
		t.Fatalf("calls were %v", calls)
	}
}

func TestDeleteAsksOnce(t *testing.T) {
	_, client, calls := fake(t)
	if err := client.DeleteBucket(context.Background(), good, "guard-test"); err != nil {
		t.Fatal(err)
	}
	if calls["delete"] != 1 {
		t.Fatalf("calls were %v", calls)
	}
}

// A refused token arrives as an HTTP 400 with a code inside, so the status
// alone would have called it a bad request. The sentence has to say what is
// actually wrong, because it is the sentence on the card.
func TestARefusedTokenSaysSo(t *testing.T) {
	_, client, _ := fake(t)
	_, err := client.Buckets(context.Background(), cloud.Credentials{Key: "wrong", Account: account})
	if err == nil {
		t.Fatal("a wrong token was accepted")
	}
	if !strings.Contains(err.Error(), "refused the api token") {
		t.Fatalf("error was %q", err)
	}
}

// The account id is not a secret, but nothing works without it, and the
// failure should name the missing thing rather than a URL with a hole in it.
func TestNoAccountIDIsItsOwnSentence(t *testing.T) {
	_, client, _ := fake(t)
	_, err := client.Buckets(context.Background(), cloud.Credentials{Key: "good-token"})
	if err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("error was %v", err)
	}
}

// Cloudflare implements the storage half and nothing else. The dashboard
// reads that off the capabilities, so it is worth a test of its own: a
// Cloudflare account must never be offered a power switch or a reveal button.
func TestCapabilitiesAreDerivedFromWhatIsImplemented(t *testing.T) {
	_, client, _ := fake(t)
	described := cloud.Describe(Provider(client))
	if !described.Capabilities.Storage {
		t.Fatal("cloudflare should do storage")
	}
	for name, claimed := range map[string]bool{
		"registries":     described.Capabilities.Registries,
		"registry maker": described.Capabilities.RegistryMaker,
		"storage keys":   described.Capabilities.StorageKeys,
		"storage rename": described.Capabilities.StorageRename,
		"compute":        described.Capabilities.Compute,
	} {
		if claimed {
			t.Fatalf("cloudflare claimed %s, which it cannot do", name)
		}
	}
	// Browsing is a half it does implement — with a stored pair.
	if !described.Capabilities.StorageObjects {
		t.Fatal("cloudflare can look inside a bucket and should say so")
	}
	if !described.Capabilities.NeedsAccountID {
		t.Fatal("cloudflare needs an account id and should say so")
	}
}

// An account with no stored pair still lists buckets and simply cannot open
// one. The sentence has to say which credential is missing — "forbidden" would
// send somebody to rotate the wrong secret.
func TestOpeningABucketWithoutAStoredPairSaysWhichKeyIsMissing(t *testing.T) {
	_, client, _ := fake(t)
	browser := Provider(client).(cloud.StorageObjects)
	_, err := browser.Objects(context.Background(), good, cloud.ObjectRef{Storage: "pack-dev"})
	if err == nil || !strings.Contains(err.Error(), "R2 access key") {
		t.Fatalf("error was %v", err)
	}
	if err := browser.VerifyObjects(context.Background(), good); err == nil {
		t.Fatal("a missing pair proved itself")
	}
}

// R2's storage is itself the bucket, so there is no level in between — and
// the page reads that off an empty answer rather than off a provider name.
func TestAnR2StorageHasNoContainersInside(t *testing.T) {
	_, client, _ := fake(t)
	containers, err := Provider(client).(cloud.StorageObjects).
		Containers(context.Background(), good, "pack-dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 0 {
		t.Fatalf("containers: %+v", containers)
	}
}
