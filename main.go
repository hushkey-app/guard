package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
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
	"github.com/hushkey-app/guard/internal/config"
	"github.com/hushkey-app/guard/internal/deploy"
	"github.com/hushkey-app/guard/internal/ingest"
	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/release"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/statuspage"
	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/internal/vaultproxy"
	"github.com/hushkey-app/guard/internal/viewalerts"
	"github.com/hushkey-app/guard/server/apis"
	apicollector "github.com/hushkey-app/guard/server/apis/collector"
	apiconfig "github.com/hushkey-app/guard/server/apis/config"
	apideployer "github.com/hushkey-app/guard/server/apis/deployer"
	apiprober "github.com/hushkey-app/guard/server/apis/prober"
	apirunner "github.com/hushkey-app/guard/server/apis/runner"
	apischeduler "github.com/hushkey-app/guard/server/apis/scheduler"
	apisignin "github.com/hushkey-app/guard/server/apis/signin"
	apistore "github.com/hushkey-app/guard/server/apis/store"
	apiupdate "github.com/hushkey-app/guard/server/apis/update"
	terminalhandler "github.com/hushkey-app/guard/server/terminal"
	"github.com/mirairoad/howl-go/core/api"
	"github.com/mirairoad/howl-go/core/app"
	"github.com/mirairoad/howl-go/core/console"
	"github.com/mirairoad/howl-go/core/mw"
)

//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module github.com/hushkey-app/guard/client/pages
//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsapis -dir server/apis -module github.com/hushkey-app/guard/server/apis -client client/api/api_gen.go -client-pkg apiclient

//go:embed client/public
var publicFS embed.FS

// WebSocket handshakes must write their 101 response before hijacking the
// connection. Middleware that buffers an ordinary HTTP response (compression
// and request coalescing) cannot wrap an upgrade: delaying the status turns
// the first WebSocket frame into an invalid HTTP response.
func withoutWebSocket(buffered mw.Middleware) mw.Middleware {
	return func(next http.Handler) http.Handler {
		ordinary := buffered(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			ordinary.ServeHTTP(w, r)
		})
	}
}

func main() {
	addr := flag.String("addr", ":4318", "HTTP and OTLP/HTTP listen address")
	dbPath := flag.String("db", env("GUARD_DB_PATH", "guard.db"), "SQLite database file")
	sshTimeout := flag.Duration("ssh-timeout", envDuration("GUARD_SSH_TIMEOUT", remote.DefaultTimeout), "how long a command run over SSH may take")
	// A scheduled command gets its own, much longer budget: the jobs people put
	// on a timer are dumps and syncs, and a backup killed at the two minutes a
	// pressed button gets is a backup that has never once worked.
	scheduleTimeout := flag.Duration("schedule-timeout", envDuration("GUARD_SCHEDULE_TIMEOUT", cluster.DefaultScheduleTimeout), "how long a scheduled command may take")
	updateRepo := flag.String("update-repo", env("GUARD_UPDATE_REPO", release.DefaultRepo), "the GitHub repository to watch for releases; empty watches nothing")
	// What this binary is, without starting it. The updater on the box asks the
	// file on disk rather than keeping its own note of what it installed, which
	// is the note that goes stale the one time somebody copies a binary by hand.
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(build.Tag())
		return
	}

	// Tinted columns in a terminal, JSON into a pipe or a log file — which is
	// what guard itself would rather ingest.
	console.Setup(console.Options{})

	// Retention and the event cap are rows in the settings table, edited on
	// Settings → Data storage and applied the moment they are saved. What is
	// passed here is only what a brand-new database starts with, so it is the
	// store's own default rather than a flag nobody would reach for twice.
	store, err := telemetry.Open(*dbPath, telemetry.Settings{})
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// Guard's own configuration, from guard's own database, put into this
	// process's environment before anything reads it. Everything below still
	// asks os.Getenv and knows nothing about the table — see internal/config
	// for why that is the whole design rather than a shortcut.
	//
	// Only the flags nobody typed are re-derived, and that ordering is the
	// escape hatch: `guard -update-repo=""` beats a stored value, which is what
	// somebody needs on the day the dashboard stored something that will not
	// start. (`GUARD_CONFIG_IGNORE=1` is the bigger hammer, and skips the lot.)
	settings, err := config.Load(store)
	if err != nil {
		log.Fatal(err)
	}
	typed := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { typed[f.Name] = true })
	redo(typed, "ssh-timeout", sshTimeout, func() time.Duration { return envDuration("GUARD_SSH_TIMEOUT", remote.DefaultTimeout) })
	redo(typed, "schedule-timeout", scheduleTimeout, func() time.Duration { return envDuration("GUARD_SCHEDULE_TIMEOUT", cluster.DefaultScheduleTimeout) })
	redo(typed, "update-repo", updateRepo, func() string { return env("GUARD_UPDATE_REPO", release.DefaultRepo) })

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
	// The collector's credential, and deliberately not the operator's. It is
	// never given to auth.Config: presenting it must not make a caller a
	// machine anywhere else in guard, or the split would be decoration.
	otelSecret := os.Getenv("GUARD_OTEL_SECRET")
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
			withoutWebSocket((mw.Compress{}).Handler),
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
			withoutWebSocket((&mw.Coalesce{}).Handler),
			// The public status page's data, for the one path that renders it.
			// A page cannot read the store itself — every page compiles into
			// views.wasm too, and modernc.org/sqlite has no js/wasm build — so
			// the value is computed here and published on the context, exactly
			// as the login view is.
			statuspage.Middleware(store),
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
	//
	// It sits outside sign-in — a collector cannot authenticate with Google —
	// so these two strings are the only thing between the port and whoever can
	// reach it. Unset both and every panel, view and uptime figure is something
	// a stranger can write to, so it is said once, loudly, at boot.
	receiver := ingest.Handler{Store: store, Token: token, Secret: otelSecret}
	if token == "" && otelSecret == "" {
		slog.Warn("OTLP ingest is unauthenticated — anything that can reach this port may post telemetry",
			slog.String("fix", "set GUARD_OTEL_SECRET"), slog.String("routes", "/v1/logs /v1/traces /v1/metrics"))
	}
	receiver.Register(mux)

	// The browser intake, off unless origins are named. It cannot be enabled by
	// accident, because an unauthenticated write endpoint that appears by
	// default is a hole somebody finds before you do.
	// One switch, and the rest is the package's own answer: the service identity
	// guard assigns to browser spans, and how many requests a minute one address
	// may post. Neither is a thing anybody has wanted to change, and both are
	// wrong to take from the payload.
	receiver.RegisterBrowser(mux, ingest.Browser{Origins: splitList(os.Getenv("GUARD_RUM_ORIGINS"))})

	// The secrets endpoints, forwarded to guard-vault, for the caller that
	// cannot reach :4319. Off unless asked for: guard's port is usually the
	// published one, and this is the one route where that difference decides
	// whether a leaked key is usable from the internet. It sits under /v1/, so
	// it is outside sign-in like every other machine route — and it has to be,
	// because the credential it takes is a vault key rather than a session.
	if err := vaultproxy.Register(mux, vaultproxy.Config{
		Enabled:  config.On("GUARD_VAULT_PROXY"),
		Upstream: vaultproxy.UpstreamFrom(os.Getenv("GUARD_VAULT_ADDR")),
	}); err != nil {
		log.Fatal(err)
	}

	// The cluster prober: the one part of guard that makes outbound requests.
	// It watches machines that were declared rather than ones that talk to
	// us — the difference between "this service stopped sending telemetry" and
	// "this box is down", which is the whole reason to have it.
	// No cadence passed: each machine carries its own interval in the database,
	// and what is left is an idle wait and a timeout that the package answers for
	// itself. Every loop below reads the same way.
	probe := &cluster.Prober{Store: store}
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
	//
	// One sender for every watcher: internal/notify owns the POST, the token
	// format and the timeout, so a second thing that needs to tell somebody
	// something is a caller rather than a copy.
	sender := &notify.Webhook{}
	watch := &cluster.Watchdog{Store: store, Sender: sender}
	go watch.Run(proberCtx)
	// The deploys. Not a loop — a deploy is driven by the press that started
	// it — but it takes the process's lifetime the same way, because a rolling
	// deploy outlives the request, and it makes honest at startup whatever the
	// last restart interrupted.
	//
	// Everything it needs it borrows: the SSH runner the scheduler uses (with
	// its own budget, because pulling a fat image is a legitimately long thing
	// to be doing), the prober's health check — which is the whole definition
	// of "did this deploy land" — and the same sender every other watcher
	// reaches for.
	deployer := &deploy.Runner{
		Store:  deployStore{store},
		SSH:    &remote.Runner{Timeout: deploy.Timeout},
		Probe:  probe,
		Sender: sender,
	}
	go deployer.Run(proberCtx)
	apideployer.Use(deployer)
	// And the rules over what the cluster page already measures — the health
	// check, the uptime share, and what each machine says about its own CPU,
	// memory and disk. Same delivery module, same destinations, its own faster
	// loop: a rule about a machine being down is worth evaluating in seconds,
	// where a rule about a six-hourly backup is not.
	monitors := &cluster.Monitors{Store: store, Sender: sender}
	go monitors.Run(proberCtx)
	// And the panels that watch themselves: a saved view with a line drawn
	// across it, run on its own slower loop because each pass is somebody's
	// compiled query against the same table the dashboard is reading.
	go (&viewalerts.Watcher{Store: store, Sender: sender}).Run(proberCtx)
	// Whether a newer guard exists. Its own slow loop, and the only thing it
	// can do about the answer is write a version into a file — installing is
	// deploy/guard-update, a root-owned unit on a timer, because the process
	// holding every application's secrets should not also be the one that can
	// replace binaries.
	updates := &release.Watch{Repo: *updateRepo, Current: build.Tag()}
	go updates.Run(proberCtx)
	apiupdate.Use(updates)
	// How a change to the stored configuration becomes a change to the running
	// process: guard exits, and its supervisor starts it again against the new
	// environment. It runs unprivileged with NoNewPrivileges and cannot ask
	// systemd for anything, so stopping and being brought back is the honest
	// way for a service to restart itself — offered only where something will
	// do the bringing back, which is what INVOCATION_ID answers.
	//
	// os.Exit rather than an unwind, for the same reason the normal path here is
	// log.Fatal: nothing in this process has a shutdown to run, and SQLite in
	// WAL mode is crash-consistent by design.
	if config.Supervised() {
		settings.Restartable(func() {
			time.AfterFunc(500*time.Millisecond, func() { os.Exit(0) })
		})
	}
	apiconfig.Use(settings)
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
	// /settings/cluster became /cluster: a machine is declared and acted on in
	// one place, and settings is configuration. Kept as a redirect rather than
	// dropped, because the path is in bookmarks, in alert emails already sent,
	// and in anything anyone wrote down.
	mux.HandleFunc("GET /settings/cluster", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cluster", http.StatusMovedPermanently)
	})

	mux.HandleFunc("GET /api/openapi.json", api.OpenAPI(api.Info{
		Title:       "Guard",
		Version:     build.Tag(),
		Description: "OTLP/HTTP telemetry receiver.",
	}, routes...))
	mux.HandleFunc("GET /api/docs", api.Docs("/api/openapi.json"))

	// A node's favicon: bytes, not JSON, so it stays an ordinary handler rather
	// than being squeezed through the typed layer as base64. Immutable for an
	// hour — a machine's icon changes about once a year, and the dashboard asks
	// for every node's every three seconds.
	//
	// `/icon/{id}` rather than `/{id}/icon`: the second form claims every
	// two-segment path under /api/cluster, so ServeMux refuses to register any
	// `/api/cluster/<thing>/{id}` beside it — "/api/cluster/env/icon" matches
	// both and neither is more specific. The panic is at startup, which is the
	// good version of that mistake, and the fix is to put the literal first.
	mux.HandleFunc("GET /api/cluster/icon/{id}", func(w http.ResponseWriter, r *http.Request) {
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

	// A real terminal is a WebSocket rather than a typed API response: browser
	// keystrokes and remote PTY bytes remain on the same connection until the
	// page, shell, or terminal timeout closes it.
	mux.Handle("GET /api/cluster/terminal/{id}", terminalhandler.Handler{
		Store: store, Runner: runner, Authorize: sessions.Authorize,
	})

	// Watching a deploy happen, as it happens.
	//
	// A raw handler rather than an endpoint, for the same reason the icon above
	// is one: the typed layer answers with a value, and this answers with a
	// connection that stays open. howl's app.SSE owns the wire format.
	//
	// It is a *second* way to see what the rows already say, never the only one.
	// A browser that never opens it, or one whose connection a proxy drops, sees
	// the same deploy on the three-second tick — which is what makes it safe for
	// this to be best-effort and for a frame to be dropped rather than queued.
	mux.HandleFunc("GET /api/deploy/stream", func(w http.ResponseWriter, r *http.Request) {
		if err := sessions.Authorize(r, []string{model.RoleAdmin}); err != nil {
			http.Error(w, "not allowed", http.StatusUnauthorized)
			return
		}
		kind, subject := deploy.KindRun, r.URL.Query().Get("run")
		if node := r.URL.Query().Get("node"); node != "" {
			kind, subject = deploy.KindPrepare, node
		}
		id, err := strconv.ParseInt(subject, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "name a run or a node", http.StatusBadRequest)
			return
		}
		frames, stop := deployer.Watch(kind, id)
		defer stop()
		stream, err := app.SSE(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// A heartbeat, because the interesting part of a deploy is the two
		// minutes where nothing is printed, and every proxy in the world closes
		// an idle connection somewhere inside that.
		beat := time.NewTicker(20 * time.Second)
		defer beat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-beat.C:
				if stream.Send("ping", "") != nil {
					return
				}
			case frame := <-frames:
				body, err := json.Marshal(frame)
				if err != nil {
					return
				}
				if stream.Send("frame", string(body)) != nil {
					return
				}
				if frame.Done {
					return
				}
			}
		}
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

// redo re-derives one flag from the environment, now that the stored
// configuration is part of it.
//
// Skipped for a flag somebody actually typed: the command line is the one place
// that outranks the database, because it is the only one available when the
// database holds a value that stops guard from starting.
func redo[T any](typed map[string]bool, name string, target *T, from func() T) {
	if typed[name] {
		return
	}
	*target = from()
}

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

// deployStore is the same seam for the deploys. One method's difference again,
// and the lock is on the other side of it: DeployTarget refuses a locked
// machine, so no adapter here can accidentally hand one out.
type deployStore struct{ *telemetry.Store }

func (s deployStore) DeployTarget(nodeID int64) (remote.Login, error) {
	login, err := s.Store.DeployTarget(nodeID)
	return remote.Login(login), err
}
