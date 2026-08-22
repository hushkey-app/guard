package ingest

// What a beacon costs against the one writer.
//
// The spec names the risk out loud, so it is measured here rather than
// asserted: an OTLP log record is one insert, and an analytics event is
// three — the raw row, the seen row that makes the session count exact, and
// the rollup the page actually reads — plus, for the page view that first
// brought a session to a path today, a fourth for where it came from. All of
// it inside one transaction on the single SQLite writer that everything else
// guard stores is also queued behind. An unmeasured risk is a rumour.
//
// Two numbers bracket real traffic, which is why both are here. A **new**
// session inserts into `analytics_seen` and moves the rollup's session count;
// a **returning** one is the same beacon where that insert is ignored and the
// rollup is a pure update. A site's day is a mixture, and reading either
// number as the whole answer is how a capacity estimate ends up wrong in the
// direction nobody planned for.
//
// The limiter is the real one with its budget lifted rather than a nil check
// skipped: everything else on the path — the origin allowlist, the body
// budget, the decode, both validations — is the code that runs in production,
// and a benchmark that took a shortcut around any of it would be measuring a
// door guard does not have.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func BenchmarkBeaconHandler(b *testing.B) {
	for _, events := range []int{1, 10} {
		b.Run(strconv.Itoa(events)+"_events", func(b *testing.B) {
			store := telemetry.NewStore(1_000_000)
			b.Cleanup(func() { store.Close() })
			benchmarkBeacons(b, store, events, sessionPerBeacon)
		})
	}
}

// The same door over a database on disk, which is the one that answers "how
// many visitors can this instance take" — the in-memory store above shares
// every code path with it except the fsync.
func BenchmarkBeaconSQLite(b *testing.B) {
	for _, events := range []int{1, 10} {
		b.Run(strconv.Itoa(events)+"_events", func(b *testing.B) {
			benchmarkBeacons(b, benchmarkSQLiteStore(b), events, sessionPerBeacon)
		})
	}
	b.Run("10_events_returning_session", func(b *testing.B) {
		benchmarkBeacons(b, benchmarkSQLiteStore(b), 10, oneSession)
	})
}

// Many browsers at once, which is the shape the risk actually arrives in: the
// writes are serialised by the single writer whatever the door does, so this
// is where a transaction that got longer stops being somebody's problem later.
func BenchmarkBeaconSQLiteParallel(b *testing.B) {
	const events = 10
	store := benchmarkSQLiteStore(b)
	handler := benchmarkBeaconHandler(store)
	template := benchmarkBeaconPayload(b, events)
	var sessions atomic.Uint64

	b.ReportAllocs()
	b.SetBytes(int64(len(template.body)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		// A copy per goroutine: the session id is rewritten in place, and
		// sharing the buffer would be a data race rather than a benchmark.
		payload := template.clone()
		for pb.Next() {
			postBeacon(handler, payload.withSession(sessions.Add(1)))
		}
	})
	b.ReportMetric(float64(b.N*events)/b.Elapsed().Seconds(), "events/s")
}

func benchmarkSQLiteStore(b *testing.B) *telemetry.Store {
	b.Helper()
	store, err := telemetry.Open(filepath.Join(b.TempDir(), "guard.db"), telemetry.Settings{RetentionHours: 24, MaxEvents: 1_000_000})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	return store
}

// How the session id moves between iterations.
type sessionMode int

const (
	sessionPerBeacon sessionMode = iota // a fresh visitor every time: seen inserts
	oneSession                          // the same visitor all day: seen is ignored
)

func benchmarkBeacons(b *testing.B, store *telemetry.Store, events int, mode sessionMode) {
	b.Helper()
	handler := benchmarkBeaconHandler(store)
	payload := benchmarkBeaconPayload(b, events)
	var session uint64

	b.ReportAllocs()
	b.SetBytes(int64(len(payload.body)))
	b.ResetTimer()
	for range b.N {
		if mode == sessionPerBeacon {
			session++
		}
		postBeacon(handler, payload.withSession(session))
	}
	b.ReportMetric(float64(b.N*events)/b.Elapsed().Seconds(), "events/s")
}

func benchmarkBeaconHandler(store *telemetry.Store) http.HandlerFunc {
	// A budget nobody can spend, because 120 posts a minute is a limit worth
	// having and a benchmark that hit it would be timing the 429.
	limiter := &rateLimiter{perMinute: 1 << 30, seen: map[string]*bucket{}}
	return Handler{Store: store}.beacon(Browser{Origins: []string{"https://app.example.com"}}, limiter)
}

func postBeacon(handler http.HandlerFunc, body []byte) {
	request := httptest.NewRequest(http.MethodPost, "/v1/rum/events", bytes.NewReader(body))
	request.Header.Set("Origin", "https://app.example.com")
	handler(httptest.NewRecorder(), request)
}

// A beacon encoded once, whose session id is sixteen bytes that can be
// rewritten in place. Encoding one per iteration would put encoding/json in
// the middle of a measurement of SQLite.
type beaconPayload struct {
	body    []byte
	session int // offset of the session id
}

const benchmarkSessionSlot = "0000000000000000"

func benchmarkBeaconPayload(b *testing.B, events int) beaconPayload {
	b.Helper()
	beacon := model.Beacon{
		Session: benchmarkSessionSlot,
		Path:    "/pricing",
		// What the tracker actually sends, so the source rollup on a session's
		// first page view is timed too rather than being the write that only
		// shows up in production.
		Source:   model.BeaconSource{Source: "newsletter", Medium: "email", Campaign: "launch"},
		Referrer: "news.example.com",
		Events:   make([]model.TrackEvent, events),
	}
	// The Views column first, because that is the event every page sends and
	// the one the source rollup hangs off.
	beacon.Events[0] = model.TrackEvent{Name: "page_view"}
	for i := 1; i < events; i++ {
		beacon.Events[i] = model.TrackEvent{
			Name:  "action_" + strconv.Itoa(i),
			Props: map[string]string{"plan": "team"},
		}
	}
	body, err := json.Marshal(beacon)
	if err != nil {
		b.Fatal(err)
	}
	offset := bytes.Index(body, []byte(benchmarkSessionSlot))
	if offset < 0 {
		b.Fatalf("no session id in %s", body)
	}
	return beaconPayload{body: body, session: offset}
}

func (p beaconPayload) clone() beaconPayload {
	return beaconPayload{body: slices.Clone(p.body), session: p.session}
}

// withSession rewrites the id in place and returns the payload. Lowercase
// hexadecimal because that is the only alphabet a session id may be in, so a
// counter written any other way would be refused by the validation this is
// meant to be running through.
func (p beaconPayload) withSession(n uint64) []byte {
	const digits = "0123456789abcdef"
	for i := len(benchmarkSessionSlot) - 1; i >= 0; i-- {
		p.body[p.session+i] = digits[n&0xf]
		n >>= 4
	}
	return p.body
}
