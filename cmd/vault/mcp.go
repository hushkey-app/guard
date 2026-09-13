package main

// `guard-vault mcp` is the secrets store as tools an agent can call.
//
// It is here rather than in a third binary because this is already the binary a
// developer has on their machine: `guard-vault fetch` is how a checkout gets a
// .env, and "let the agent do it" is the same job asked differently. It shares
// nothing else with the server half — no database, no key file, no schema. It
// opens a socket and presents a bearer token, exactly as the application would.
//
// Everything it can do is what the token can do. There is no configuration
// naming a workspace or an environment, and there could not be: the scope comes
// from the key, so an agent handed a develop key cannot be talked into writing
// production by any prompt, and the refusal comes from the server rather than
// from a check in this file that somebody could argue with.
//
//	GUARD_SECRETS_URL=http://guard.internal:4318
//	GUARD_VAULT_KEY=gsk_hushkey_develop_…
//	guard-vault mcp
//
// One URL, and it is guard's rather than the vault's: reading works against
// either, and writing only ever works against guard, whose store is the one
// with methods that change things.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/build"
)

// protocolVersion is the revision of MCP this speaks. Answered rather than
// negotiated: a client asking for an older one still gets working tools, and
// there is nothing here that a version changes.
const protocolVersion = "2025-06-18"

// mcpTimeout bounds one call to guard. Everything behind it is an indexed read
// and a decrypt, or one small write; an agent waiting longer than this is an
// agent that should be told the server is not answering.
const mcpTimeout = 15 * time.Second

// mcp runs the server: JSON-RPC 2.0 over stdin and stdout, one message per
// line, until the pipe closes.
func mcp(args []string) error {
	flags := flag.NewFlagSet("guard-vault mcp", flag.ExitOnError)
	addr := flags.String("url", firstSet("GUARD_SECRETS_URL", "GUARD_VAULT_URL"),
		"where guard answers — its base URL, not the vault's, because only guard writes")
	key := flags.String("key", os.Getenv("GUARD_VAULT_KEY"), "the gsk_ key this agent may act as")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*addr) == "" {
		return errors.New("set GUARD_SECRETS_URL to where guard answers, or pass -url")
	}
	if strings.TrimSpace(*key) == "" {
		return errors.New("set GUARD_VAULT_KEY to a secrets key, or pass -key")
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(*addr), "/"))
	if err != nil || base.Host == "" {
		return fmt.Errorf("that is not a URL guard could be answering on: %s", *addr)
	}
	// Said on stderr because stdout is the protocol: one stray line there and
	// the client drops the connection on a parse error.
	if os.Getenv("GUARD_SECRETS_URL") == "" && os.Getenv("GUARD_VAULT_URL") != "" {
		fmt.Fprintln(os.Stderr,
			"using GUARD_VAULT_URL — if that is guard-vault on :4319 then reading works and writing will not; "+
				"set GUARD_SECRETS_URL to guard's own port")
	}
	client := &secretsClient{base: base, key: strings.TrimSpace(*key),
		http: &http.Client{Timeout: mcpTimeout}}
	return serveMCP(os.Stdin, os.Stdout, client)
}

func firstSet(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The protocol
// ---------------------------------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serveMCP reads messages until the pipe closes.
//
// A request with no id is a notification and gets no answer — `initialized` is
// the one that matters, and a server that replied to it would be one some
// clients drop.
func serveMCP(in io.Reader, out io.Writer, client *secretsClient) error {
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	for {
		var request rpcRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(request.ID) == 0 {
			continue
		}
		result, failure := dispatch(request, client)
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
		if failure != nil {
			response.Error = failure
		} else {
			response.Result = result
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
}

func dispatch(request rpcRequest, client *secretsClient) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "guard-secrets", "version": build.Tag()},
			"instructions": "Secrets for one environment of one application, scoped by the key this server holds. " +
				"Call secrets_whoami first: it says which workspace and environment you are acting on and whether " +
				"you may change anything. Values are masked unless you ask for them.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolList()}, nil
	case "tools/call":
		return callTool(request.Params, client)
	default:
		return nil, &rpcError{Code: -32601, Message: "no such method: " + request.Method}
	}
}

// ---------------------------------------------------------------------------
// The tools
// ---------------------------------------------------------------------------

type tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func object(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	return schema
}

func field(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}

// toolList is six verbs, and the split between them is the point: reading is
// safe and says so, writing is marked destructive so a client that asks before
// destructive calls gets to ask.
func toolList() []tool {
	return []tool{
		{
			Name:  "secrets_whoami",
			Title: "Which environment am I acting on",
			Description: "Answers the workspace and environment this key is scoped to, whether it may write, " +
				"and how many secrets are there. Call this before anything else: the scope comes from the key, " +
				"so it cannot be chosen and there is no point guessing it.",
			InputSchema: object(map[string]any{}),
			Annotations: map[string]any{"readOnlyHint": true},
		},
		{
			Name:  "secrets_list",
			Title: "List the keys",
			Description: "The names of every secret in this environment. Values are left out unless reveal is true — " +
				"a list of names is usually the question, and forty live credentials in a transcript is not something " +
				"to do by default.",
			InputSchema: object(map[string]any{
				"reveal": field("boolean", "Include the values. Only ask when you need them."),
			}),
			Annotations: map[string]any{"readOnlyHint": true},
		},
		{
			Name:        "secrets_get",
			Title:       "Read one value",
			Description: "The value of one secret in this environment.",
			InputSchema: object(map[string]any{
				"key": field("string", "The secret's name, e.g. DATABASE_URL."),
			}, "key"),
			Annotations: map[string]any{"readOnlyHint": true},
		},
		{
			Name:  "secrets_set",
			Title: "Set one value",
			Description: "Writes one secret, whether or not it was already there. Needs a key minted with permission " +
				"to write. Overwrites silently, so read it first if the old value mattered.",
			InputSchema: object(map[string]any{
				"key":   field("string", "The secret's name: letters, digits and underscores, not starting with a digit."),
				"value": field("string", "The value. Stored encrypted; it comes back to whoever holds a key for this environment."),
			}, "key", "value"),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true},
		},
		{
			Name:  "secrets_delete",
			Title: "Remove one secret",
			Description: "Deletes one secret from this environment. It is gone — there is no history to restore it from. " +
				"A key that is not there is an error rather than a shrug, because a cleanup naming a key that has " +
				"already gone has usually named the wrong one.",
			InputSchema: object(map[string]any{
				"key": field("string", "The secret's name."),
			}, "key"),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false},
		},
		{
			Name:  "secrets_apply",
			Title: "Write several, and optionally remove the rest",
			Description: "Writes a whole set in one call, and with prune makes the environment match it exactly — " +
				"which is the cleanup a deployment wants. Always run it with dry_run first: the answer says what " +
				"would be added, changed, left alone and pruned, and the same call without dry_run does exactly that.",
			InputSchema: object(map[string]any{
				"secrets": map[string]any{
					"type":                 "object",
					"description":          "The pairs to write, as names to values.",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"prune":   field("boolean", "Delete every key this call does not mention. This empties what it does not name — dry_run first."),
				"dry_run": field("boolean", "Report what would happen and change nothing."),
			}, "secrets"),
			Annotations: map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true},
		},
	}
}

type toolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// callTool runs one and answers in MCP's shape.
//
// A failed call is a result with isError rather than a JSON-RPC error, because
// the two mean different things to a client: the protocol did not fail, the
// tool did, and the model is supposed to read why and try something else.
func callTool(params json.RawMessage, client *secretsClient) (any, *rpcError) {
	var call toolCall
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: -32602, Message: "could not read those arguments"}
	}
	body, err := runTool(call, client)
	if err != nil {
		return map[string]any{
			"content": []any{map[string]any{"type": "text", "text": err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": body}}}, nil
}

func runTool(call toolCall, client *secretsClient) (string, error) {
	switch call.Name {
	case "secrets_whoami":
		return client.text(http.MethodGet, "/v1/whoami", nil)
	case "secrets_list":
		return client.list(boolArg(call.Arguments, "reveal"))
	case "secrets_get":
		key, err := stringArg(call.Arguments, "key")
		if err != nil {
			return "", err
		}
		return client.text(http.MethodGet, "/v1/secrets/"+url.PathEscape(key), nil)
	case "secrets_set":
		key, err := stringArg(call.Arguments, "key")
		if err != nil {
			return "", err
		}
		value, err := stringArg(call.Arguments, "value")
		if err != nil {
			return "", err
		}
		return client.text(http.MethodPut, "/v1/secrets/"+url.PathEscape(key),
			map[string]string{"value": value})
	case "secrets_delete":
		key, err := stringArg(call.Arguments, "key")
		if err != nil {
			return "", err
		}
		return client.text(http.MethodDelete, "/v1/secrets/"+url.PathEscape(key), nil)
	case "secrets_apply":
		pairs, err := mapArg(call.Arguments, "secrets")
		if err != nil {
			return "", err
		}
		return client.text(http.MethodPost, "/v1/secrets", map[string]any{
			"secrets": pairs,
			"prune":   boolArg(call.Arguments, "prune"),
			"dry_run": boolArg(call.Arguments, "dry_run"),
		})
	default:
		return "", fmt.Errorf("no such tool: %s", call.Name)
	}
}

func stringArg(args map[string]any, name string) (string, error) {
	value, ok := args[name].(string)
	if !ok || value == "" {
		if name == "value" {
			// An empty value is a legitimate thing to store — a flag turned
			// off is often "" — so only a missing one is an error.
			if _, present := args[name]; present {
				return "", nil
			}
		}
		return "", fmt.Errorf("%s is needed, as a string", name)
	}
	return value, nil
}

func boolArg(args map[string]any, name string) bool {
	value, _ := args[name].(bool)
	return value
}

func mapArg(args map[string]any, name string) (map[string]string, error) {
	raw, ok := args[name].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("%s is needed, as an object of names to values", name)
	}
	pairs := make(map[string]string, len(raw))
	for key, value := range raw {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string — %s is not", key, key)
		}
		pairs[key] = text
	}
	return pairs, nil
}

// ---------------------------------------------------------------------------
// The client
// ---------------------------------------------------------------------------

type secretsClient struct {
	base *url.URL
	key  string
	http *http.Client
}

// list is the one tool that reshapes an answer rather than passing it through,
// because the masking is the point: the names are what an agent almost always
// wants, and a transcript full of credentials is the thing this makes easy to
// avoid rather than easy to do.
func (c *secretsClient) list(reveal bool) (string, error) {
	body, err := c.text(http.MethodGet, "/v1/secrets", nil)
	if err != nil {
		return "", err
	}
	if reveal {
		return body, nil
	}
	var answer struct {
		Workspace  string            `json:"workspace"`
		Env        string            `json:"env"`
		Revision   string            `json:"revision"`
		Secrets    map[string]string `json:"secrets"`
		Unreadable []string          `json:"unreadable"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		return body, nil
	}
	names := make([]string, 0, len(answer.Secrets))
	for name := range answer.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	masked := map[string]any{
		"workspace": answer.Workspace,
		"env":       answer.Env,
		"revision":  answer.Revision,
		"keys":      names,
		"note":      "values withheld — call again with reveal: true if you need them",
	}
	// Only when there are any: a null beside every listing is a field a model
	// has to decide to ignore every time it reads one.
	if len(answer.Unreadable) > 0 {
		masked["unreadable"] = answer.Unreadable
	}
	return encode(masked)
}

// text makes one request and returns the body, or an error that says what the
// server said. The server's own sentence is kept: "this key reads
// hushkey/develop and cannot change it" is the whole answer, and rewording it
// here would only make it vaguer.
func (c *secretsClient) text(method, path string, body any) (string, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base.String()+path, payload)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", fmt.Errorf("could not reach guard at %s: %w", c.base, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode == http.StatusNotFound && method != http.MethodGet {
		// The likely cause is not a missing secret: it is a URL pointing at
		// guard-vault, which has no write routes at all. Worth saying, because
		// the two look identical from here and only one is a five-second fix.
		return "", fmt.Errorf("guard answered 404 for %s %s — if %s is guard-vault on :4319 it cannot write; "+
			"point GUARD_SECRETS_URL at guard, and check GUARD_SECRETS_API is on there", method, path, c.base)
	}
	if response.StatusCode >= 400 {
		return "", errors.New(serverSaid(answer, response.Status))
	}
	return string(answer), nil
}

// serverSaid unwraps {"error":"…"} when that is what came back, and falls back
// to the status line when it did not.
func serverSaid(body []byte, status string) string {
	var wrapped struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil && wrapped.Error != "" {
		return wrapped.Error
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		return status + ": " + trimmed
	}
	return status
}

func encode(value any) (string, error) {
	out, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}
