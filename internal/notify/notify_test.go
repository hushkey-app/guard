package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type capture struct {
	auth, custom, contentType string
	body                      map[string]any
}

func listener(t *testing.T, status int) (*httptest.Server, *capture) {
	t.Helper()
	got := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth = r.Header.Get("Authorization")
		got.custom = r.Header.Get("X-Api-Key")
		got.contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, got
}

func TestSendCarriesTheEventAndItsCredential(t *testing.T) {
	server, got := listener(t, 200)
	hook := &Webhook{}
	event := Event{
		Kind:    KindClusterRule,
		Subject: "DB-1/disk_percent",
		State:   StateFiring,
		Title:   "Disk / above 90%",
		Message: "DB-1: Disk / is 94%",
		Fields:  map[string]any{"value": 94.0, "node": "DB-1"},
	}
	if err := hook.Send(context.Background(), Destination{Name: "ops", URL: server.URL, Token: "s3cret"}, event); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got.auth)
	}
	if got.contentType != "application/json" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	// text is filled from the message, so one URL serves a chat hook that
	// renders it and a handler that reads the fields properly.
	if got.body["text"] != event.Message || got.body["state"] != StateFiring {
		t.Fatalf("payload = %v", got.body)
	}
	fields, _ := got.body["fields"].(map[string]any)
	if fields["node"] != "DB-1" {
		t.Fatalf("fields = %v", fields)
	}
	// The timestamp is filled in when the caller left it zero, because a
	// receiver ordering events by "at" should not have to guess.
	if got.body["at"] == "0001-01-01T00:00:00Z" || got.body["at"] == nil {
		t.Fatalf("at = %v", got.body["at"])
	}
}

func TestTokenSchemes(t *testing.T) {
	server, got := listener(t, 200)
	hook := &Webhook{}
	event := Event{Message: "hello"}

	// A token that names its own scheme is sent as written: "Bot xxx" is what
	// a bot API wants, and "Bearer Bot xxx" is what nothing wants.
	if err := hook.Send(context.Background(), Destination{URL: server.URL, Token: "Bot abc"}, event); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bot abc" {
		t.Fatalf("Authorization = %q", got.auth)
	}

	// A custom header takes the token verbatim — an X-Api-Key with "Bearer "
	// glued to the front is a key that does not work.
	if err := hook.Send(context.Background(), Destination{URL: server.URL, Token: "k-1", Header: "X-Api-Key"}, event); err != nil {
		t.Fatal(err)
	}
	if got.custom != "k-1" || got.auth != "" {
		t.Fatalf("X-Api-Key = %q, Authorization = %q", got.custom, got.auth)
	}

	// And no token at all is the Slack case: the URL is the secret.
	if err := hook.Send(context.Background(), Destination{URL: server.URL}, event); err != nil {
		t.Fatal(err)
	}
	if got.auth != "" {
		t.Fatalf("Authorization = %q, want none", got.auth)
	}
}

func TestARefusalIsAnError(t *testing.T) {
	server, _ := listener(t, http.StatusUnauthorized)
	// Callers leave their "already told them" flag unset on an error, so this
	// is what stops a 401 from swallowing an outage.
	if err := (&Webhook{}).Send(context.Background(), Destination{Name: "ops", URL: server.URL}, Event{}); err == nil {
		t.Fatal("a 401 is not a delivery")
	}
}

func TestADestinationWithNoURLIsAnError(t *testing.T) {
	if err := (&Webhook{Timeout: time.Second}).Send(context.Background(), Destination{Name: "ops"}, Event{}); err == nil {
		t.Fatal("nowhere is not somewhere")
	}
}
