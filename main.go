package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hushkey-app/guard/client/pages"
	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/build"
	"github.com/hushkey-app/guard/internal/cluster"
	"github.com/hushkey-app/guard/internal/ingest"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/server/apis"
	apicollector "github.com/hushkey-app/guard/server/apis/collector"
	apiprober "github.com/hushkey-app/guard/server/apis/prober"
	apirunner "github.com/hushkey-app/guard/server/apis/runner"
	apischeduler "github.com/hushkey-app/guard/server/apis/scheduler"
	apisignin "github.com/hushkey-app/guard/server/apis/signin"
	apistore "github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
	"github.com/mirairoad/howl-go/core/app"
	"github.com/mirairoad/howl-go/core/console"
	"github.com/mirairoad/howl-go/core/mw"
)

//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module github.com/hushkey-app/guard/client/pages
//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsapis -dir server/apis -module github.com/hushkey-app/guard/server/apis -client client/api/api_gen.go -client-pkg apiclient

//go:embed client/public
var publicFS embed.FS

func main() {
	addr := flag.String("addr", ":4318", "HTTP and OTLP/HTTP listen address")
	dbPath := flag.String("db", env("GUARD_DB_PATH", "guard.db"), "SQLite database file")
	retentionHours := flag.Int("retention-hours", envInt("GUARD_RETENTION_HOURS", 24), "hours of telemetry to retain")
	maxEvents := flag.Int("max-events", envInt("GUARD_MAX_EVENTS", 1_000_000), "maximum telemetry events retained")
	clusterInterval := flag.Duration("cluster-interval", envDuration("GUARD_CLUSTER_INTERVAL", 30*time.Second), "how long the prober waits when no node is due; each node carries its own interval")
	clusterTimeout := flag.Duration("cluster-timeout", envDuration("GUARD_CLUSTER_TIMEOUT", 5*time.Second), "how long a cluster health check may take")
	sshTimeout := flag.Duration("ssh-timeout", envDuration("GUARD_SSH_TIMEOUT", remote.DefaultTimeout), "how long a command run over SSH may take")
	// A scheduled command gets its own, much longer budget: the jobs people put
	// on a timer are dumps and syncs, and a backup killed at the two minutes a
	// pressed button gets is a backup that has never once worked.
	scheduleTimeout := flag.Duration("schedule-timeout", envDuration("GUARD_SCHEDULE_TIMEOUT", cluster.DefaultScheduleTimeout), "how long a scheduled command may take")
	alertWebhook := flag.String("alert-webhook", env("GUARD_ALERT_WEBHOOK", ""), "URL to POST staleness alerts to; alerts are logged either way")
	alertInterval := flag.Duration("alert-interval", envDuration("GUARD_ALERT_INTERVAL", 5*time.Minute), "how often to check that scheduled jobs are still succeeding")
	alertRepeat := flag.Duration("alert-repeat", envDuration("GUARD_ALERT_REPEAT", 6*time.Hour), "how long a stale job stays quiet after it has been reported")
	flag.Parse()

	// Tinted columns in a terminal, JSON into a pipe or a log file — which is
	// what guard itself would rather ingest.
	console.Setup(console.Options{})

	store, err := telemetry.Open(*dbPath, telemetry.Settings{RetentionHours: *retentionHours, MaxEvents: *maxEvents})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	public, err := fs.Sub(publicFS, "client/public")
	if err != nil {
		log.Fatal(err)
	}

	// Sign-in, if the environment configured any. With no OAuth credentials
	// this builds a service with no providers, every request passes straight
	// through it, and guard is the open tool it has always been. A half-filled
	// configuration is fatal rather than ignored — somebody who set a client id
	// and forgot the secret meant to close the door.
	token := os.Getenv("GUARD_TOKEN")
	signin := auth.FromEnv()
	signin.APIToken = token
	sessions, err := auth.New(store, signin)
	if err != nil {
		log.Fatal(err)
	}
	sessions.Startup()

	a := app.New(app.Config{
		Addr: *addr, Routes: pages.FsClientRoutes(), Shell: pages.App, NotFound: pages.NotFound, Public: public,
		// Outermost first. Guard is an observability tool, so its own requests
		// are worth observing: one structured line each, with an id that also
		// goes back on the response.
		Use: []mw.Middleware{
			mw.RequestID,
			// Callers is the point of it here: everything that posts telemetry
			// to guard IS an outside caller, and knowing which service and
			// which host is half of what you open guard to find out.
			mw.LogWith(mw.LogOptions{Callers: true, Skip: mw.SkipNoise}),
			mw.Recover(nil),
			mw.Compress{}.Handler,
			// Who is asking. It runs for every request — pages, API and
			// static — and publishes the answer on the context, so nothing
			// downstream has to remember to check. Outside the coalescer on
			// purpose: a request that is not allowed should never reach the
			// thing that shares one render between callers.
			sessions.Guard,
			// The dashboard fires seven parallel GETs every 3s, per open tab.
			// Identical ones now share a single query instead of racing each
			// other through SQLite. Writes, and anything carrying an
			// Authorization header, are passed straight through.
			(&mw.Coalesce{}).Handler,
		},
		OnError: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("render failed", "path", r.URL.Path, "err", err)
			http.Error(w, "guard failed to render this page", http.StatusInternalServerError)
		},
	})
	mux := a.Mux()

	// The browser flow: /login, /auth/{provider}/start and the callbacks.
	// Ordinary handlers, because every one of them answers with a redirect or a
	// cookie rather than with JSON.
	sessions.Register(mux)

	// The OTLP receiver: protobuf in, protobuf out, byte-compatible with every
	// exporter. Not part of the typed API layer, on purpose.
	receiver := ingest.Handler{Store: store, Token: token}
	receiver.Register(mux)

	// The browser intake, off unless origins are named. It cannot be enabled by
	// accident, because an unauthenticated write endpoint that appears by
	// default is a hole somebody finds before you do.
	receiver.RegisterBrowser(mux, ingest.Browser{
		Origins:   splitList(os.Getenv("GUARD_RUM_ORIGINS")),
		Service:   env("GUARD_RUM_SERVICE", "browser"),
		Instance:  os.Getenv("GUARD_RUM_RELEASE"),
		PerMinute: envInt("GUARD_RUM_PER_MINUTE", 120),
	})

	// The cluster prober: the one part of guard that makes outbound requests.
	// It watches machines that were declared rather than ones that talk to
	// us — the difference between "this service stopped sending telemetry" and
	// "this box is down", which is the whole reason to have it.
	probe := &cluster.Prober{Store: store, Interval: *clusterInterval, Timeout: *clusterTimeout}
	proberCtx, stopProber := context.WithCancel(context.Background())
	defer stopProber()
	go probe.Run(proberCtx)

	// The JSON API. The table is generated from server/apis/, so an endpoint's
	// file is its URL and nothing is registered by hand.
	apistore.Use(store)
	apiprober.Use(probe)
	// The other half of reaching out: running a stored command on a machine
	// over SSH. Nothing happens through it unless somebody saved a command and
	// a password, so an instance that never uses the feature never opens a
	// connection.
	runner := &remote.Runner{Timeout: *sshTimeout}
	apirunner.Use(runner)
	// And the third: asking each machine how it is doing, over the same SSH
	// login, on its own much slower cadence. It samples nothing unless a
	// machine has a stored login and a cadence — so an instance that never
	// gave guard a way in never opens a connection here either.
	stats := &cluster.Collector{Store: statsStore{store}, Runner: runner}
	go stats.Run(proberCtx)
	apicollector.Use(stats)
	// The fourth loop: the stored commands that carry a schedule. Its own
	// runner, because its timeout is half an hour where a pressed button gets
	// two minutes — a dump is a legitimately long thing to be doing.
	schedule := &cluster.Scheduler{
		Store:   statsStore{store},
		Runner:  &remote.Runner{Timeout: *scheduleTimeout},
		Timeout: *scheduleTimeout,
	}
	go schedule.Run(proberCtx)
	apischeduler.Use(schedule)
	// And the watch over it, which is a separate loop on purpose: a check that
	// only ran as part of the dump would never fire on the day the dump did
	// not, which is the exact day it is for. It reads the database rather than
	// the scheduler, so it still speaks when the scheduler is wedged or was
	// never started, and it reaches out through its own client rather than the
	// SSH runner it is reporting on.
	watch := &cluster.Watchdog{
		Store:    store,
		Interval: *alertInterval,
		Repeat:   *alertRepeat,
	}
	if *alertWebhook != "" {
		watch.Notifier = &cluster.Webhook{URL: *alertWebhook}
	}
	go watch.Run(proberCtx)
	// What the members endpoints need to know that only the environment can
	// answer: whether sign-in is on, and which admins came from it.
	apisignin.Use(sessions)
	routes := apis.FsApiRoutes()
	// Permission. howl-go carries the role strings an endpoint declared and
	// asks guard what they mean; the answer is in internal/auth, because with
	// sign-in configured it depends on who is signed in.
	api.Register(mux, api.Config{Authorize: sessions.Authorize}, routes...)

	// Built at startup from the same table that was just registered, so it
	// cannot describe an endpoint this binary does not serve.
	mux.HandleFunc("GET /api/openapi.json", api.OpenAPI(api.Info{
		Title:       "Guard",
		Version:     build.Version,
		Description: "OTLP/HTTP telemetry receiver.",
	}, routes...))
	mux.HandleFunc("GET /api/docs", api.Docs("/api/openapi.json"))

	// A node's favicon: bytes, not JSON, so it stays an ordinary handler rather
	// than being squeezed through the typed layer as base64. Immutable for an
	// hour — a machine's icon changes about once a year, and the dashboard asks
	// for every node's every three seconds.
	mux.HandleFunc("GET /api/cluster/{id}/icon", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		icon, contentType, err := store.Icon(id)
		if err != nil {
			http.Error(w, "no icon", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Write(icon) //nolint:errcheck
	})

	log.Fatal(a.Listen(mux))
}

// Guard's permission layer used to live here as a bearer-token check. It is now
// (*auth.Service).Authorize, because the answer depends on whether anybody had
// to sign in: the token still works and still means "a machine, let it
// through", but with OAuth configured an endpoint declaring Roles:
// []string{"admin"} asks who is signed in and what they are on the members
// list. howl-go itself has no user model and no opinion about tokens, sessions
// or scopes, which is the only way it can stay out of the way of an application
// that has all three.

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// splitList reads a comma-separated environment variable, ignoring the empty
// entries that trailing commas and copy-paste leave behind.
func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}

// statsStore adapts the store to what the stats collector asks for.
//
// One method's worth of difference: the store's SSHLogin and the runner's
// Login are the same four fields declared in two packages, because neither
// should have to import the other to describe a password. The conversion is
// the seam, and it lives here rather than in either of them.
type statsStore struct{ *telemetry.Store }

func (s statsStore) SSHLoginFor(nodeID int64) (remote.Login, error) {
	login, err := s.Store.SSHLoginFor(nodeID)
	return remote.Login(login), err
}
