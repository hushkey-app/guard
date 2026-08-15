package apis_test

// The members endpoints: the guest list, over HTTP.
//
// These run through the generated table with the same Authorize the rest of the
// API tests use, so what is exercised is the real path and the real roles —
// every one of these endpoints declares admin, and the token is what stands in
// for one here.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	apimembers "github.com/hushkey-app/guard/server/apis/members"
	apisignin "github.com/hushkey-app/guard/server/apis/signin"
)

func TestMembersAreAddedListedAndRemoved(t *testing.T) {
	_, srv := server(t, "token")

	if code := post(t, srv.URL+"/api/members", []byte(`{"email":"ana@example.com"}`), ""); code != http.StatusUnauthorized {
		t.Fatalf("without a token = %d, want 401 — the guest list is not public", code)
	}
	if code := post(t, srv.URL+"/api/members", []byte(`{"email":"ana@example.com"}`), "token"); code != http.StatusOK {
		t.Fatalf("add = %d", code)
	}
	// Adding somebody who is already there with a different role is a
	// promotion, not a duplicate.
	if code := post(t, srv.URL+"/api/members", []byte(`{"email":"ANA@example.com","role":"admin"}`), "token"); code != http.StatusOK {
		t.Fatalf("promote = %d", code)
	}
	if code := post(t, srv.URL+"/api/members", []byte(`{"email":"not-an-address"}`), "token"); code != http.StatusBadRequest {
		t.Fatalf("a bad address = %d, want 400", code)
	}

	var roster apimembers.Roster
	if code := getWith(t, srv.URL+"/api/members", "token", &roster); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(roster.Members) != 1 {
		t.Fatalf("members = %#v", roster.Members)
	}
	if roster.Members[0].Email != "ana@example.com" || roster.Members[0].Role != model.RoleAdmin {
		t.Fatalf("member = %#v", roster.Members[0])
	}
	if roster.Enabled {
		t.Fatal("no provider is configured in this test, so the list is enforced by nothing yet")
	}

	if code := send(t, http.MethodDelete, srv.URL+"/api/members/ana@example.com", nil, "token"); code != http.StatusOK {
		t.Fatalf("remove = %d", code)
	}
	if code := send(t, http.MethodDelete, srv.URL+"/api/members/ana@example.com", nil, "token"); code != http.StatusNotFound {
		t.Fatalf("removing twice = %d, want 404", code)
	}
}

// An address from GUARD_ADMIN_EMAIL is not a row and must not become one: a row
// for it would look removable and would change nothing when removed.
func TestEnvironmentAdminsCannotBeEdited(t *testing.T) {
	store, srv := server(t, "token")
	service, err := auth.New(store, auth.Config{
		Google: auth.Google{ClientID: "id", ClientSecret: "secret"},
		Admins: []string{"leo@hushkey.app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	apisignin.Use(service)
	t.Cleanup(func() { apisignin.Use(nil) })

	if code := post(t, srv.URL+"/api/members", []byte(`{"email":"leo@hushkey.app"}`), "token"); code != http.StatusBadRequest {
		t.Fatalf("adding the environment's admin = %d, want 400", code)
	}
	if code := send(t, http.MethodDelete, srv.URL+"/api/members/leo@hushkey.app", nil, "token"); code != http.StatusBadRequest {
		t.Fatalf("removing the environment's admin = %d, want 400", code)
	}

	// They are listed all the same — the page has to be able to say who can
	// get in, including the person who cannot be taken off.
	var roster apimembers.Roster
	if code := getWith(t, srv.URL+"/api/members", "token", &roster); code != http.StatusOK {
		t.Fatalf("list = %d", code)
	}
	if len(roster.Members) != 1 || !roster.Members[0].Fixed {
		t.Fatalf("members = %#v", roster.Members)
	}
	if !roster.Enabled {
		t.Fatal("a configured provider should show as enabled")
	}
}

func getWith(t *testing.T, url, token string, into any) int {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if into != nil {
		json.NewDecoder(response.Body).Decode(into) //nolint:errcheck
	}
	return response.StatusCode
}
