package vault

// The door an application knocks on: one bearer token in, one environment's
// worth of secrets out.
//
// Three rules carry the design, and they are the same three that carry the rest
// of guard's outward-facing surfaces:
//
//   - **The environment comes from the key, never from the request.** There is
//     no ?env= parameter and there never can be one. A token is a scope, so a
//     leaked staging key cannot be pointed at production by editing a URL.
//   - **Unknown, revoked and expired are one answer.** A caller that learns
//     which of the three it hit has learned something about a token it does not
//     hold.
//   - **Bookkeeping never fails a fetch.** An application that will not boot
//     because an audit row would not fit is worse than an audit row that is
//     missing.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Server answers the fetches. Zero value is not useful — it needs a Store.
type Server struct {
	Store *Store
	// Touch is how often one key's use is recorded. A container that polls for
	// changes every minute does not need sixty rows an hour to prove it is
	// alive; what the log is for is the key that starts being used from
	// somewhere new.
	Touch time.Duration

	mu   sync.Mutex
	seen map[int64]time.Time
}

// Answer is what a fetch returns: the environment it came from, where it is up
// to, and the pairs.
//
// A map rather than a list because that is what the caller is going to build
// anyway, and the order of environment variables has never meant anything.
type Answer struct {
	// Workspace is which application these belong to. Reported rather than
	// implied: a fetch that lands in the wrong container's logs should say
	// whose configuration it was.
	Workspace string            `json:"workspace"`
	Env       string            `json:"env"`
	Revision string            `json:"revision"`
	Secrets  map[string]string `json:"secrets"`
	// Unreadable names the keys sealed with a key this vault does not have.
	// They are named rather than silently dropped: an application missing one
	// variable should fail with the name of it in the message.
	Unreadable []string `json:"unreadable,omitempty"`
}

// Register mounts the whole surface. Four routes, and one of them is /healthz.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/secrets", s.values)
	mux.HandleFunc("GET /v1/secrets/{key}", s.value)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n")) //nolint:errcheck
	})
}

// values answers the whole environment, as JSON or as .env text.
//
// The .env form is not a convenience: an init container that writes a file and
// a shell that sources one are how most things still take configuration, and a
// vault that only spoke JSON would have everybody piping it through jq.
func (s *Server) values(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return
	}
	revision, err := s.Store.Revision(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Nothing has moved since they last asked. The point of answering this
	// cheaply is that an application can then poll — a rotated secret reaching
	// a running process without a redeploy is the reason to have a server here
	// rather than a file.
	tag := `"` + strconv.FormatInt(revision, 10) + `"`
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "no-store")
	if match := r.Header.Get("If-None-Match"); match != "" && match == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	pairs, _, err := s.Store.Values(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.used(holder, r, len(pairs))
	if r.URL.Query().Get("format") == "env" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(model.FormatEnv(pairs))) //nolint:errcheck
		return
	}
	answer := Answer{Workspace: holder.Workspace, Env: holder.EnvName,
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

// value answers one key — for the caller that wants one thing and does not want
// the other forty in its memory to do it.
func (s *Server) value(w http.ResponseWriter, r *http.Request) {
	holder, ok := s.authorize(w, r)
	if !ok {
		return
	}
	wanted := r.PathValue("key")
	pairs, _, err := s.Store.Values(holder.EnvID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, pair := range pairs {
		if pair.Key != wanted {
			continue
		}
		if pair.Unreadable {
			s.deny(w, r, http.StatusConflict, "that secret was sealed with a different key — set it again")
			return
		}
		s.used(holder, r, 1)
		writeJSON(w, model.Secret{Key: pair.Key, Value: pair.Value})
		return
	}
	s.deny(w, r, http.StatusNotFound, "no such secret in "+holder.Workspace+"/"+holder.EnvName)
}

// authorize turns a bearer token into the environment it may read.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) (Holder, bool) {
	token := bearer(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="guard secrets"`)
		s.deny(w, r, http.StatusUnauthorized, "this endpoint needs a secrets key")
		return Holder{}, false
	}
	sum := sha256.Sum256([]byte(token))
	holder, err := s.Store.Holder(sum[:])
	if errors.Is(err, sql.ErrNoRows) {
		s.deny(w, r, http.StatusUnauthorized, "that key is not one this vault knows, or it has been revoked")
		return Holder{}, false
	}
	if err != nil {
		s.fail(w, r, err)
		return Holder{}, false
	}
	return holder, true
}

// bearer reads the token, and only from the Authorization header. Not from a
// query parameter: that is the one place a credential ends up in an access log,
// a proxy log and a browser history at the same time.
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
	// A bare token, because somebody's HTTP client makes the scheme awkward and
	// there is nothing else this header could mean here.
	return header
}

// used records the fetch, throttled, and never in the way.
func (s *Server) used(holder Holder, r *http.Request, count int) {
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

	ip := callerIP(r)
	slog.Info("secrets read", slog.String("key", holder.Name),
		slog.String("workspace", holder.Workspace), slog.String("env", holder.EnvName),
		slog.Int("count", count), slog.String("ip", ip))
	// Off the request path entirely, not merely tolerant of failure. A write
	// against a database that is locked or a disk that is full does not fail —
	// it *waits*, up to busy_timeout, three statements deep. Left in line, the
	// exact situation this bookkeeping is least important in is the one where
	// it would hold an application's boot open for fifteen seconds and then
	// have it time out. Nothing reads the row back, so nothing is waiting for
	// this to be true.
	go func() {
		if err := s.Store.Used(holder, ip, count); err != nil {
			slog.Warn("could not record a secrets fetch", slog.String("key", holder.Name), slog.Any("err", err))
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
	slog.Info("secrets refused", slog.String("path", r.URL.Path),
		slog.String("ip", callerIP(r)), slog.String("why", message))
	writeStatus(w, code, map[string]string{"error": message})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("secrets failed", slog.String("path", r.URL.Path), slog.Any("err", err))
	writeStatus(w, http.StatusInternalServerError, map[string]string{"error": "the vault could not read that"})
}

func writeJSON(w http.ResponseWriter, body any) { writeStatus(w, http.StatusOK, body) }

// writeStatus sets the type before the code, in that order, because the other
// way round writes the header with no content type and no way to add one.
func writeStatus(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}
