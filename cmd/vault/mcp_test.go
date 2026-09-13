package main

// The protocol half, against a stand-in guard. What matters here is not the
// JSON-RPC — it is that a tool call is a request with the key on it, that
// nothing in an argument can choose an environment, and that values are not
// handed out unless somebody asked for them.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type seen struct {
	method, path, auth, body string
}

// stand builds a guard that records what it was asked and answers a fixture.
func stand(t *testing.T, log *[]seen, answers map[string]string) *secretsClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body) //nolint:errcheck
		*log = append(*log, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), body.String()})
		answer, found := answers[r.Method+" "+r.URL.Path]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such thing"}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(answer)) //nolint:errcheck
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &secretsClient{base: base, key: "gsk_hushkey_develop_test", http: server.Client()}
}

func call(t *testing.T, client *secretsClient, name string, args map[string]any) (string, bool) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	result, failure := callTool(params, client)
	if failure != nil {
		t.Fatalf("%s: %s", name, failure.Message)
	}
	shaped, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s answered %T", name, result)
	}
	content := shaped["content"].([]any)[0].(map[string]any)
	isError, _ := shaped["isError"].(bool)
	return content["text"].(string), isError
}

// Every call carries the key and nothing else. This is the whole of the
// server's authority: it holds a token and presents it, so the scope is the
// server's answer rather than a check in here that a prompt could argue with.
func TestEveryCallCarriesTheKey(t *testing.T) {
	var log []seen
	client := stand(t, &log, map[string]string{
		"GET /v1/whoami":    `{"workspace":"hushkey","env":"develop","can_write":true}`,
		"GET /v1/secrets":   `{"workspace":"hushkey","env":"develop","secrets":{"A":"1"}}`,
		"PUT /v1/secrets/A": `{"key":"A","value":"2"}`,
	})
	call(t, client, "secrets_whoami", nil)
	call(t, client, "secrets_list", nil)
	call(t, client, "secrets_set", map[string]any{"key": "A", "value": "2"})
	if len(log) != 3 {
		t.Fatalf("made %d requests", len(log))
	}
	for _, request := range log {
		if request.auth != "Bearer gsk_hushkey_develop_test" {
			t.Fatalf("%s %s went without the key: %q", request.method, request.path, request.auth)
		}
	}
}

// No argument reaches the URL as a workspace or an environment. There is
// nothing in the tool schemas that could, and this is what keeps it that way
// when somebody adds a convenience.
func TestNoArgumentCanChooseAnEnvironment(t *testing.T) {
	var log []seen
	client := stand(t, &log, map[string]string{
		"GET /v1/secrets": `{"workspace":"hushkey","env":"develop","secrets":{"A":"1"}}`,
	})
	call(t, client, "secrets_list", map[string]any{
		"env": "production", "workspace": "other", "env_id": 4, "reveal": true,
	})
	if len(log) != 1 || log[0].path != "/v1/secrets" {
		t.Fatalf("the request carried something: %+v", log)
	}
	// The parameter names themselves, not the prose around them: a description
	// may well mention the environment, and a *field* called one is the thing
	// that must never appear.
	forbidden := map[string]bool{"workspace": true, "env": true, "env_id": true, "environment": true}
	for _, tool := range toolList() {
		properties, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no properties", tool.Name)
		}
		for name := range properties {
			if forbidden[name] {
				t.Fatalf("%s takes %s — the scope comes from the key", tool.Name, name)
			}
		}
	}
}

// Listing withholds the values. Forty live credentials in a transcript is the
// thing that should take an extra word, not the thing that happens by default.
func TestListingWithholdsValuesUnlessAsked(t *testing.T) {
	var log []seen
	client := stand(t, &log, map[string]string{
		"GET /v1/secrets": `{"workspace":"hushkey","env":"develop","revision":"7",` +
			`"secrets":{"DATABASE_URL":"postgres://secret","API_TOKEN":"sk-live-123"}}`,
	})
	masked, isError := call(t, client, "secrets_list", nil)
	if isError {
		t.Fatal(masked)
	}
	for _, value := range []string{"postgres://secret", "sk-live-123"} {
		if strings.Contains(masked, value) {
			t.Fatalf("a plain list leaked a value:\n%s", masked)
		}
	}
	for _, name := range []string{"DATABASE_URL", "API_TOKEN"} {
		if !strings.Contains(masked, name) {
			t.Fatalf("the list is missing %s:\n%s", name, masked)
		}
	}
	revealed, _ := call(t, client, "secrets_list", map[string]any{"reveal": true})
	if !strings.Contains(revealed, "sk-live-123") {
		t.Fatalf("reveal did not:\n%s", revealed)
	}
}

// A refusal from guard is passed through in guard's own words. "this key reads
// hushkey/develop and cannot change it" is the whole answer, and rewording it
// here would only make it vaguer.
func TestGuardsRefusalReachesTheModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"this key reads hushkey/develop and cannot change it — mint one that may write"}`)) //nolint:errcheck
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &secretsClient{base: base, key: "k", http: server.Client()}

	text, isError := call(t, client, "secrets_set", map[string]any{"key": "A", "value": "1"})
	if !isError {
		t.Fatal("a refused write was reported as a success")
	}
	if !strings.Contains(text, "cannot change it") {
		t.Fatalf("the reason did not survive: %s", text)
	}
}

// A write against the vault's port is a 404, and that reads as "no such
// secret" unless it is explained. The two look identical from here and only one
// of them is a five-second fix.
func TestWritingAtTheVaultSaysWhichPortToUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &secretsClient{base: base, key: "k", http: server.Client()}

	text, isError := call(t, client, "secrets_set", map[string]any{"key": "A", "value": "1"})
	if !isError || !strings.Contains(text, "GUARD_SECRETS_URL") {
		t.Fatalf("unhelpful: %s", text)
	}
}

// An empty value is a value. A flag turned off is "" far more often than it is
// a mistake, and refusing it would send somebody looking for a delete.
func TestAnEmptyValueIsStored(t *testing.T) {
	var log []seen
	client := stand(t, &log, map[string]string{"PUT /v1/secrets/FEATURE_X": `{"key":"FEATURE_X","value":""}`})
	if text, isError := call(t, client, "secrets_set", map[string]any{"key": "FEATURE_X", "value": ""}); isError {
		t.Fatal(text)
	}
	if len(log) != 1 || !strings.Contains(log[0].body, `"value":""`) {
		t.Fatalf("sent %+v", log)
	}
}

// initialize, tools/list and an unknown method — the handshake a client makes
// before it will call anything.
func TestTheHandshake(t *testing.T) {
	var log []seen
	client := stand(t, &log, nil)

	result, failure := dispatch(rpcRequest{Method: "initialize"}, client)
	if failure != nil {
		t.Fatal(failure.Message)
	}
	if result.(map[string]any)["protocolVersion"] != protocolVersion {
		t.Fatalf("answered %+v", result)
	}
	listed, failure := dispatch(rpcRequest{Method: "tools/list"}, client)
	if failure != nil {
		t.Fatal(failure.Message)
	}
	tools := listed.(map[string]any)["tools"].([]tool)
	if len(tools) != 6 {
		t.Fatalf("%d tools", len(tools))
	}
	// Every tool that changes something is marked, so a client that asks before
	// destructive calls gets the chance to.
	for _, one := range tools {
		writes := strings.Contains(one.Name, "set") || strings.Contains(one.Name, "delete") ||
			strings.Contains(one.Name, "apply")
		readOnly, _ := one.Annotations["readOnlyHint"].(bool)
		if writes == readOnly {
			t.Fatalf("%s is marked readOnlyHint=%v", one.Name, readOnly)
		}
	}
	if _, failure := dispatch(rpcRequest{Method: "nonsense"}, client); failure == nil {
		t.Fatal("an unknown method was answered")
	}
}

// A notification gets no reply. A server that answered `initialized` is one
// some clients drop.
func TestNotificationsAreNotAnswered(t *testing.T) {
	var log []seen
	client := stand(t, &log, nil)
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	out := new(bytes.Buffer)
	if err := serveMCP(in, out, client); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"id":1`) {
		t.Fatalf("answered:\n%s", out)
	}
}
