package ingest

// The browser-facing door.
//
// Telemetry from a browser is telemetry from a stranger. Everything else guard
// ingests comes from a process someone deployed, over a network they control,
// holding a token they issued. A browser holds no secret — anything shipped to
// it is public by definition — and posts from an address anyone can have.
//
// So this is not /v1/traces with the auth removed. It is a narrower door with
// its own rules:
//
//   - off unless origins are configured, so it cannot be exposed by accident
//   - the service identity is assigned here, never accepted from the payload
//   - a body budget two orders of magnitude smaller than the collector's
//   - a request budget per address
//   - traces and logs only
//
// The identity rule is the important one. Without it a visitor can post spans
// claiming service.name="api", and every panel, every view and every uptime
// figure on the dashboard is now something a stranger can write to.

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// Browser is the configuration of the browser intake. A zero value is disabled,
// which is the only safe default for an unauthenticated write endpoint.
type Browser struct {
	// Origins that may post. Empty disables the intake entirely. "*" is
	// accepted and means what it says — appropriate when the relay in front of
	// guard has already decided who may reach it, and wrong on anything
	// publicly reachable.
	Origins []string
	// Service is the identity every browser event is filed under, overriding
	// whatever the payload claimed. One name, because a browser cannot be
	// trusted to name itself and "which page" belongs in an attribute.
	Service string
	// Instance separates deployments of that service — a release tag, usually.
	Instance string
	// PerMinute is how many requests one address may make. The SDK batches, so
	// a busy tab is a handful a minute; a hundred is generous and a thousand is
	// a hole.
	PerMinute int
}

const (
	// A browser batch is a few spans. The collector's 16MB is sized for a
	// gateway forwarding thousands.
	maxBrowserBody = 256 << 10
	defaultRate    = 120
)

// Enabled reports whether the intake should be mounted at all.
func (b Browser) Enabled() bool { return len(b.Origins) > 0 }

// RegisterBrowser mounts the browser intake. Paths mirror the OTLP ones so the
// JavaScript SDK needs only a different base URL, not a different exporter.
func (h Handler) RegisterBrowser(mux *http.ServeMux, config Browser) {
	if !config.Enabled() {
		return
	}
	if config.Service == "" {
		config.Service = "browser"
	}
	if config.PerMinute <= 0 {
		config.PerMinute = defaultRate
	}
	limiter := &rateLimiter{perMinute: config.PerMinute, seen: map[string]*bucket{}}

	mux.HandleFunc("POST /v1/rum/traces", h.browser(config, limiter, "traces"))
	mux.HandleFunc("POST /v1/rum/logs", h.browser(config, limiter, "logs"))
	// The preflight the browser sends before either of the above.
	mux.HandleFunc("OPTIONS /v1/rum/{signal}", func(w http.ResponseWriter, r *http.Request) {
		if !cors(w, r, config.Origins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	})
}

func (h Handler) browser(config Browser, limiter *rateLimiter, signal string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cors(w, r, config.Origins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if !limiter.allow(address(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		// A smaller budget than the collector's, enforced before decoding
		// rather than after: the point is not to have read it.
		r.Body = http.MaxBytesReader(w, r.Body, maxBrowserBody)

		var events []telemetry.Event
		switch signal {
		case "traces":
			var request collectortracepb.ExportTraceServiceRequest
			jsonMode, err := decode(r, &request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			events = traceEvents(&request)
			defer writeProto(w, jsonMode, &collectortracepb.ExportTraceServiceResponse{})
		default:
			var request collectorlogspb.ExportLogsServiceRequest
			jsonMode, err := decode(r, &request)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			events = logEvents(&request)
			defer writeProto(w, jsonMode, &collectorlogspb.ExportLogsServiceResponse{})
		}

		for i := range events {
			// Assigned, never accepted. This is the line between "a browser
			// reports its own page views" and "a stranger writes to your
			// service dashboard".
			events[i].Service = config.Service
			events[i].Instance = config.Instance
			if events[i].Attributes == nil {
				events[i].Attributes = map[string]any{}
			}
			// Marked, so a view can include or exclude the public half of the
			// telemetry deliberately rather than by accident.
			events[i].Attributes["telemetry.source"] = "browser"
		}
		if len(events) > 0 {
			if err := h.Store.Add(events...); err != nil {
				http.Error(w, "could not store telemetry", http.StatusInternalServerError)
				return
			}
		}
	}
}

// cors answers the origin check and sets the headers when it passes.
//
// No Access-Control-Allow-Credentials anywhere: this endpoint neither reads
// cookies nor wants them, and the pair of a wildcard origin and credentials is
// the classic way to turn a public endpoint into a private one.
func cors(w http.ResponseWriter, r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	for _, candidate := range allowed {
		if candidate == "*" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			return true
		}
		if strings.EqualFold(candidate, origin) && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			return true
		}
	}
	// A relay posting server-to-server sends no Origin at all, and is allowed:
	// it reached this endpoint through whatever fronts guard, which is where
	// that decision belongs.
	return origin == ""
}

// address is who to count requests against. X-Forwarded-For is honoured because
// the recommended deployment puts a relay in front — which also means it must
// be a relay you trust, since a client can send that header itself.
func address(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if comma := strings.IndexByte(forwarded, ','); comma > 0 {
			return strings.TrimSpace(forwarded[:comma])
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter is a token bucket per address, refilled continuously.
//
// Continuous rather than per-window because a fixed window lets an address
// spend its whole budget in the last second of one window and again in the
// first second of the next.
type rateLimiter struct {
	mu        sync.Mutex
	perMinute int
	seen      map[string]*bucket
	swept     time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func (l *rateLimiter) allow(address string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	// Addresses are unbounded — one visitor per address, and the internet has
	// plenty. Anything idle for a full budget's worth of time is forgotten.
	if now.Sub(l.swept) > time.Minute {
		for key, b := range l.seen {
			if now.Sub(b.at) > time.Minute {
				delete(l.seen, key)
			}
		}
		l.swept = now
	}

	b := l.seen[address]
	if b == nil {
		b = &bucket{tokens: float64(l.perMinute), at: now}
		l.seen[address] = b
	}
	rate := float64(l.perMinute) / 60
	b.tokens = min(float64(l.perMinute), b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
