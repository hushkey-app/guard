// Package terminal bridges a browser WebSocket to an interactive SSH PTY.
//
// It is intentionally outside the typed API table: an API endpoint returns one
// JSON value, while this connection carries terminal bytes in both directions
// until the browser, shell, or timeout closes it.
package terminal

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

const protocol = "guard-terminal"

// Handler owns no sessions itself. One HTTP upgrade is one SSH connection;
// closing the page cancels it and closes the remote PTY.
type Handler struct {
	Store     *telemetry.Store
	Runner    *remote.Runner
	Authorize func(*http.Request, []string) error
}

type message struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Store == nil || h.Runner == nil || h.Authorize == nil {
		http.Error(w, "interactive terminals are unavailable", http.StatusServiceUnavailable)
		return
	}
	authorized := requestWithProtocolToken(r)
	if err := h.Authorize(authorized, []string{model.RoleAdmin}); err != nil {
		http.Error(w, "not allowed", http.StatusUnauthorized)
		return
	}
	nodeID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || nodeID <= 0 {
		http.Error(w, "no machine with that id", http.StatusNotFound)
		return
	}
	login, err := h.Store.ExecTarget(nodeID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(128 << 10)

	// A websocket outlives the HTTP handler's ordinary request lifetime. Its
	// context is canceled by the reader when the browser disconnects, or by the
	// runner when the PTY reaches its own limit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputR, inputW := io.Pipe()
	defer inputR.Close()
	sizes := make(chan remote.TerminalSize, 8)
	output := &socketWriter{ctx: ctx, cancel: cancel, conn: conn}

	go readBrowser(ctx, cancel, conn, inputW, sizes)
	started := time.Now().UTC()
	result, terminalErr := h.Runner.Terminal(ctx, remote.Login{
		User:        login.User,
		Address:     login.Address,
		Password:    login.Password,
		Fingerprint: login.Fingerprint,
	}, inputR, output, sizes)
	if terminalErr != nil && ctx.Err() == nil {
		_, _ = output.Write([]byte(fmt.Sprintf("\r\n[guard: %s]\r\n", terminalErr)))
	}
	cancel()
	_ = inputW.Close()

	if result.Fingerprint != "" && login.Fingerprint == "" {
		if err := h.Store.PinFingerprint(nodeID, result.Fingerprint); err != nil {
			slog.Error("ssh host key not stored", slog.Int64("node", nodeID), slog.Any("err", err))
		}
	}
	run := model.Run{
		Command: "[interactive terminal]", ExitCode: result.ExitCode,
		DurationMS: result.DurationMS, RanAt: started, Trigger: model.TriggerManual,
	}
	if terminalErr != nil {
		run.Error = terminalErr.Error()
		run.ExitCode = -1
	}
	if err := h.Store.RecordExec(nodeID, run); err != nil {
		slog.Error("interactive terminal session not recorded", slog.Int64("node", nodeID), slog.Any("err", err))
	}
	slog.Info("interactive ssh terminal ended",
		slog.Int64("node", nodeID), slog.String("user", login.User), slog.String("address", login.Address),
		slog.Float64("duration_ms", result.DurationMS), slog.Int("exit", run.ExitCode), slog.String("err", run.Error))
	_ = conn.Close(websocket.StatusNormalClosure, "terminal closed")
}

func readBrowser(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, input *io.PipeWriter, sizes chan<- remote.TerminalSize) {
	defer cancel()
	defer input.Close()
	for {
		kind, body, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText || len(body) > 128<<10 {
			continue
		}
		var frame message
		if json.Unmarshal(body, &frame) != nil {
			continue
		}
		switch frame.Type {
		case "input":
			if len(frame.Data) > 64<<10 {
				continue
			}
			if _, err := io.WriteString(input, frame.Data); err != nil {
				return
			}
		case "resize":
			select {
			case sizes <- remote.TerminalSize{Rows: frame.Rows, Cols: frame.Cols}:
			default:
				// Resize storms collapse naturally; the next frame carries the
				// current size and the PTY never needs every intermediate pixel.
			}
		}
	}
}

// The browser WebSocket API cannot set Authorization. It can set a subprotocol,
// so the existing bearer token is base64url-encoded into a second offered
// protocol and restored only on the request copy passed to Authorize. It never
// enters the URL or request log.
func requestWithProtocolToken(r *http.Request) *http.Request {
	const prefix = "guard-token."
	for _, value := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, offered := range strings.Split(value, ",") {
			offered = strings.TrimSpace(offered)
			if !strings.HasPrefix(offered, prefix) {
				continue
			}
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(offered, prefix))
			if err != nil || len(decoded) == 0 {
				continue
			}
			clone := r.Clone(r.Context())
			clone.Header = r.Header.Clone()
			clone.Header.Set("Authorization", "Bearer "+string(decoded))
			return clone
		}
	}
	return r
}

type socketWriter struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   *websocket.Conn
	mu     sync.Mutex
}

func (w *socketWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// The SSH package may reuse its read buffer after Write returns.
	body := append([]byte(nil), p...)
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, body); err != nil {
		w.cancel()
		return 0, err
	}
	return len(p), nil
}
