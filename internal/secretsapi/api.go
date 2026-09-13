// Package secretsapi is the writing half of the secrets surface, on guard's
// own port.
//
// Guard writes and the vault reads, and that split is a property of the build
// rather than a promise: `internal/vault` has no method that changes a secret,
// so no handler above it can grow one. Applications still needed a way to push
// a value and to clean one up — a script seeding a develop environment, a
// deploy removing the key it introduced last month — and there were two places
// to put it. Putting it in the vault would have deleted the one property that
// makes the vault worth being a second binary. So it lives here, against
// guard's own store, which is the process that already owns every write.
//
// The cost is honest and worth saying out loud: pushing and removing need
// guard to be up. Reading does not, and reading is the one an application does
// at boot.
//
// Four rules, and three of them are the vault's own:
//
//   - **The workspace and the environment come from the key, never from the
//     request.** There is no ?env= here either. A key scopes to one
//     environment for reading and for writing, so the blast radius of a leaked
//     write key is exactly the environment its own name says.
//   - **Unknown, revoked and expired are one answer.** Same as next door.
//   - **Reading is not writing.** A key carries `can_write`, off unless
//     somebody ticked it when minting, and a read key presenting itself here
//     is refused in words. The permission is a column, so revoking it is an
//     UPDATE rather than a hunt for everywhere the token was pasted.
//   - **Off unless switched on.** Guard's port is usually the published one,
//     so `GUARD_SECRETS_API` starts empty and the boot log says which way it
//     went — the same bargain `GUARD_VAULT_PROXY` makes, for the same reason,
//     and more so because this door also writes.
package secretsapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/internal/vault"
)

// maxBody bounds one write. A secret value is capped at a megabyte by
// model.Secret.Validate, and a batch is a .env file; four megabytes is well
// past anything real and well short of anything that matters to the process.
const maxBody = 4 << 20

// Config is the switch and the store behind it.
type Config struct {
	// Enabled is GUARD_SECRETS_API. Empty is off, and off is the default.
	Enabled bool
	// Store is guard's own — the writer. Not the vault's read-only one, which
	// could not serve half of this.
	Store *telemetry.Store
}

// Server answers the five routes. It holds no credential of its own: every
// request is decided by the token it carries.
type Server struct {
	Store *telemetry.Store
	// Touch is how often a read by one key is recorded. Writes are never
	// throttled — they are rare and they are the interesting half of an audit
	// log, where a container polling for changes every minute is not.
	Touch time.Duration

	mu   sync.Mutex
	seen map[int64]time.Time
}

// Apply is a batch write: a whole environment's worth in one call.
//
// It exists for the two things a script actually does — seed an environment,
// and make it match what the deployment says it should be — and `Prune` is the
// second one. Both forms of input are accepted because both are what a caller
// already has: a map if it built one, .env text if it read a file.
type Apply struct {
	// Secrets is the pairs, as a map. Empty is allowed only alongside Env.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Env is the same thing as .env text, for the caller holding a file.
	Env string `json:"env,omitempty"`
	// Prune removes the keys this call does not mention, so an environment can
	// be made to match exactly — the cleanup half. Off by default, because a
	// push of three new values that silently emptied the other forty would be
	// the last time anybody used this.
	Prune bool `json:"prune,omitempty"`
	// DryRun asks what would happen and changes nothing, so a deployment can
	// print its own diff before it does it.
	DryRun bool `json:"dry_run,omitempty"`
}

// Written is what a set answers: what it stored, and where the environment is
// up to afterwards.
//
// Its own shape rather than a model.Secret, which carries an id and a created
// date this caller has no business knowing and no way to have — a row id echoed
// back as 0 is worse than absent, because somebody will use it.
type Written struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Revision is the environment's new ETag, so a caller that has just written
	// knows what a conditional read will now compare against without making one.
	Revision  string    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Whoami is what a key says it is, which is the first thing any script wants
// to check and the only thing here that needs no permission at all.
type Whoami struct {
	Workspace string `json:"workspace"`
	Env       string `json:"env"`
	Key       string `json:"key"`
	CanWrite  bool   `json:"can_write"`
	Revision  string `json:"revision"`
	Secrets   int    `json:"secrets"`
}

// Register puts the routes on the mux, or explains why it did not.
//
// It reports whether it took `GET /v1/secrets`, because the vault proxy claims
// the same two patterns and a ServeMux panics on a duplicate. When this door is
// on, it answers the reads from guard's own store and the proxy has nothing
// left to forward.
func Register(mux *http.ServeMux, cfg Config) (bool, error) {
	if !cfg.Enabled {
		slog.Info("guard's secrets API is off — /v1/secrets cannot be written here",
			slog.String("fix", "GUARD_SECRETS_API=1, and mint a key that may write"))
		return false, nil
	}
	if cfg.Store == nil {
		return false, errors.New("the secrets API needs guard's store")
	}
	server := &Server{Store: cfg.Store}
	mux.HandleFunc("GET /v1/secrets", server.list)
	mux.HandleFunc("GET /v1/secrets/{key}", server.get)
	mux.HandleFunc("PUT /v1/secrets/{key}", server.put)
	mux.HandleFunc("DELETE /v1/secrets/{key}", server.remove)
	mux.HandleFunc("POST /v1/secrets", server.apply)
	mux.HandleFunc("GET /v1/whoami", server.whoami)
	slog.Info("guard's secrets API is on — /v1/secrets reads and writes here",
		slog.String("note", "a write key leaked from here can rewrite its environment from wherever this port is reachable"))
	return true, nil
}

// whoami answers what the presented key is scoped to: the first thing a script
// checks, and the only call here that needs no permission beyond being a key.
//
// Outside /v1/secrets rather than under it, because `whoami` is a name a secret
// may legitimately have — letters and underscores is the whole rule — and a
// route that quietly made one key of an environment unreadable would be a
// wonderful afternoon for somebody.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return
	}
	revision, err := s.Store.SecretsRevision(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pairs, err := s.Store.Secrets(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, Whoami{Workspace: holder.Workspace, Env: holder.EnvName, Key: holder.Name,
		CanWrite: holder.CanWrite, Revision: strconv.FormatInt(revision, 10), Secrets: len(pairs)})
}

// list answers the whole environment, in the vault's own shape.
//
// vault.Answer rather than a struct of the same fields: two doors that describe
// the same environment differently is a caller that has to know which one it
// reached, and the type is the cheapest way to make that impossible.
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return
	}
	revision, err := s.Store.SecretsRevision(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tag := `"` + strconv.FormatInt(revision, 10) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-store")
	if match := r.Header.Get("If-None-Match"); match != "" && match == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	pairs, err := s.Store.Secrets(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.used(holder, r, "read", len(pairs))
	if r.URL.Query().Get("format") == "env" {
		readable := make([]model.Secret, 0, len(pairs))
		for _, pair := range pairs {
			if !pair.Unreadable {
				readable = append(readable, pair)
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(model.FormatEnv(readable))) //nolint:errcheck
		return
	}
	answer := vault.Answer{Workspace: holder.Workspace, Env: holder.EnvName,
		Revision: strconv.FormatInt(revision, 10), Secrets: map[string]string{}}
	for _, pair := range pairs {
		if pair.Unreadable {
			answer.Unreadable = append(answer.Unreadable, pair.Key)
			continue
		}
		answer.Secrets[pair.Key] = pair.Value
	}
	writeJSON(w, answer)
}

// get answers one key.
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return
	}
	wanted := r.PathValue("key")
	secret, err := s.Store.Secret(holder.EnvID, wanted)
	if errors.Is(err, sql.ErrNoRows) {
		s.deny(w, r, http.StatusNotFound, "no such secret in "+holder.Workspace+"/"+holder.EnvName)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if secret.Unreadable {
		s.deny(w, r, http.StatusConflict, "that secret was sealed with a different key — set it again")
		return
	}
	s.used(holder, r, "read", 1)
	writeJSON(w, model.Secret{Key: secret.Key, Value: secret.Value})
}

// put sets one value, whether or not it was already there.
//
// By key rather than by id, exactly like the dashboard's own save — a caller
// holding a token has no business knowing a row id, and setting a value is the
// same operation either way.
func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorizeWrite(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	value, err := readValue(r)
	if err != nil {
		s.deny(w, r, http.StatusBadRequest, err.Error())
		return
	}
	saved, err := s.Store.SaveSecret(model.Secret{EnvID: holder.EnvID, Key: key, Value: value})
	if err != nil {
		s.deny(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.used(holder, r, "write", 1)
	slog.Info("secret written", slog.String("key", holder.Name), slog.String("name", key),
		slog.String("workspace", holder.Workspace), slog.String("env", holder.EnvName),
		slog.String("ip", callerIP(r)))
	revision, err := s.Store.SecretsRevision(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, Written{Key: saved.Key, Value: saved.Value,
		Revision: strconv.FormatInt(revision, 10), UpdatedAt: saved.UpdatedAt})
}

// readValue takes the value out of the body, in either of the two shapes a
// caller already has it in.
//
// JSON is the documented one; a `text/plain` body is the whole value, because
// `curl --data-binary @key.pem` is how a certificate gets into one of these and
// asking somebody to JSON-escape a PEM by hand is asking for a broken key.
func readValue(r *http.Request) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return "", errors.New("could not read that request")
	}
	if len(body) > maxBody {
		return "", errors.New("that value is too large")
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		return string(body), nil
	}
	var wrapped struct {
		Value *string `json:"value"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return "", errors.New(`the body should be {"value":"…"}, or text/plain for the value itself`)
	}
	if wrapped.Value == nil {
		return "", errors.New(`no value in that — the body should be {"value":"…"}`)
	}
	return *wrapped.Value, nil
}

// remove deletes one pair.
//
// A key that is not there is a 404 rather than a shrug: a cleanup script naming
// a key that has already gone has usually named the wrong one, and the
// idempotent way to make an environment match something is the batch below,
// with Prune.
func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorizeWrite(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	secret, err := s.Store.Secret(holder.EnvID, key)
	if errors.Is(err, sql.ErrNoRows) {
		s.deny(w, r, http.StatusNotFound, "no such secret in "+holder.Workspace+"/"+holder.EnvName)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Store.DeleteSecret(secret.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.used(holder, r, "delete", 1)
	slog.Info("secret removed", slog.String("key", holder.Name), slog.String("name", key),
		slog.String("workspace", holder.Workspace), slog.String("env", holder.EnvName),
		slog.String("ip", callerIP(r)))
	writeJSON(w, map[string]string{"deleted": secret.Key})
}

// apply writes a batch, and optionally makes the environment match it exactly.
//
// It is Store.ImportSecrets underneath — the same call the dashboard's import
// dialog makes — so there is one place that decides what a bulk write does,
// one parser for .env text, and one report describing it. A map is turned into
// that text rather than into a second loop over SaveSecret, which is what keeps
// the dry run honest: what this call describes is what the same function does
// with DryRun off.
func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorizeWrite(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		s.deny(w, r, http.StatusBadRequest, "could not read that request, or it is too large")
		return
	}
	var request Apply
	if err := json.Unmarshal(body, &request); err != nil {
		s.deny(w, r, http.StatusBadRequest, `the body should be {"secrets":{…}} or {"env":"KEY=value\n…"}`)
		return
	}
	text, err := request.text()
	if err != nil {
		s.deny(w, r, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.Store.ImportSecrets(model.Import{EnvID: holder.EnvID, Text: text,
		Prune: request.Prune, DryRun: request.DryRun})
	if err != nil {
		s.deny(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if !request.DryRun {
		s.used(holder, r, "write", len(result.Added)+len(result.Changed)+len(result.Pruned))
		slog.Info("secrets applied", slog.String("key", holder.Name),
			slog.String("workspace", holder.Workspace), slog.String("env", holder.EnvName),
			slog.Int("added", len(result.Added)), slog.Int("changed", len(result.Changed)),
			slog.Int("pruned", len(result.Pruned)), slog.String("ip", callerIP(r)))
	}
	writeJSON(w, result)
}

// text turns whichever form arrived into the one .env dialect the store parses.
//
// Sorted, so a dry run and the write it describes report their keys in the same
// order — a Go map ranged twice is two different answers, and a diff that
// reshuffles itself is a diff nobody reads twice.
func (a Apply) text() (string, error) {
	if len(a.Secrets) == 0 && strings.TrimSpace(a.Env) == "" {
		if a.Prune {
			// An empty body with Prune is the one case where nothing to write
			// is still a request: empty this environment. Said explicitly
			// rather than by accident, because the accident is somebody's
			// production.
			return "", errors.New("to empty an environment, send the keys you want left — pruning against nothing is refused")
		}
		return "", errors.New("nothing to write: send secrets or env")
	}
	if len(a.Secrets) == 0 {
		return a.Env, nil
	}
	pairs := make([]model.Secret, 0, len(a.Secrets))
	for key, value := range a.Secrets {
		pairs = append(pairs, model.Secret{Key: key, Value: value})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	// Both forms may arrive together; the file is appended, so a later line
	// wins the same way it would in a shell reading them in that order.
	return model.FormatEnv(pairs) + a.Env, nil
}

// authorize turns a bearer token into the environment it may read.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (telemetry.KeyHolder, bool) {
	token := bearer(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="guard secrets"`)
		s.deny(w, r, http.StatusUnauthorized, "this endpoint needs a secrets key")
		return telemetry.KeyHolder{}, false
	}
	sum := sha256.Sum256([]byte(token))
	holder, err := s.Store.SecretKeyHolder(sum[:])
	if errors.Is(err, sql.ErrNoRows) {
		s.deny(w, r, http.StatusUnauthorized, "that key is not one guard knows, or it has been revoked")
		return telemetry.KeyHolder{}, false
	}
	if err != nil {
		s.fail(w, r, err)
		return telemetry.KeyHolder{}, false
	}
	return holder, true
}

// authorizeWrite is the same, and then the permission.
//
// Refused in words rather than as a 404: the caller holds this key and already
// knows which environment it names, so there is nothing to learn from being
// told it cannot write — and "403, mint one that can" is the difference between
// a five-minute fix and an afternoon.
func (s *Server) authorizeWrite(w http.ResponseWriter, r *http.Request) (telemetry.KeyHolder, bool) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return holder, false
	}
	if !holder.CanWrite {
		s.deny(w, r, http.StatusForbidden,
			"this key reads "+holder.Workspace+"/"+holder.EnvName+" and cannot change it — mint one that may write")
		return telemetry.KeyHolder{}, false
	}
	return holder, true
}

// bearer reads the token, and only from the Authorization header — the vault's
// rule, and the reason is the same: a query parameter ends up in an access log,
// a proxy log and a browser history at once.
func bearer(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	if scheme, token, found := strings.Cut(header, " "); found {
		if strings.EqualFold(scheme, "bearer") {
			return strings.TrimSpace(token)
		}
		return ""
	}
	return header
}

// used records what the key did. Never in the way of the answer.
func (s *Server) used(holder telemetry.KeyHolder, r *http.Request, action string, count int) {
	if action == "read" {
		window := s.Touch
		if window <= 0 {
			window = time.Minute
		}
		s.mu.Lock()
		if s.seen == nil {
			s.seen = map[int64]time.Time{}
		}
		last, known := s.seen[holder.KeyID]
		now := time.Now()
		if known && now.Sub(last) < window {
			s.mu.Unlock()
			return
		}
		s.seen[holder.KeyID] = now
		s.mu.Unlock()
	}
	ip := callerIP(r)
	// Off the request path for the same reason the vault's is: a write against
	// a locked database waits rather than failing, and bookkeeping that held a
	// deploy open for fifteen seconds would be bookkeeping nobody kept. Nothing
	// reads the row back, so nothing is waiting for it.
	go func() {
		if err := s.Store.SecretKeyUsed(holder, action, ip, count); err != nil {
			slog.Warn("could not record a secrets call", slog.String("key", holder.Name), slog.Any("err", err))
		}
	}()
}

func callerIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, found := strings.Cut(forwarded, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, code int, message string) {
	slog.Info("secrets refused", slog.String("path", r.URL.Path), slog.String("method", r.Method),
		slog.String("ip", callerIP(r)), slog.String("why", message))
	writeStatus(w, code, map[string]string{"error": message})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("secrets failed", slog.String("path", r.URL.Path), slog.Any("err", err))
	writeStatus(w, http.StatusInternalServerError, map[string]string{"error": "guard could not do that"})
}

func writeJSON(w http.ResponseWriter, body any) { writeStatus(w, http.StatusOK, body) }

func writeStatus(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}
