package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/mirairoad/guard/client/pages"
	"github.com/mirairoad/guard/internal/ingest"
	"github.com/mirairoad/guard/internal/telemetry"
	"github.com/mirairoad/guard/server/apis"
	apistore "github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
	"github.com/mirairoad/howl-go/core/app"
	"github.com/mirairoad/howl-go/core/console"
	"github.com/mirairoad/howl-go/core/mw"
)

//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module github.com/mirairoad/guard/client/pages
//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsapis -dir server/apis -module github.com/mirairoad/guard/server/apis -client client/api/api_gen.go -client-pkg apiclient

//go:embed client/public
var publicFS embed.FS

func main() {
	addr := flag.String("addr", ":4318", "HTTP and OTLP/HTTP listen address")
	dbPath := flag.String("db", env("GUARD_DB_PATH", "guard.db"), "SQLite database file")
	retentionHours := flag.Int("retention-hours", envInt("GUARD_RETENTION_HOURS", 24), "hours of telemetry to retain")
	maxEvents := flag.Int("max-events", envInt("GUARD_MAX_EVENTS", 1_000_000), "maximum telemetry events retained")
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

	// The OTLP receiver: protobuf in, protobuf out, byte-compatible with every
	// exporter. Not part of the typed API layer, on purpose.
	token := os.Getenv("GUARD_TOKEN")
	ingest.Handler{Store: store, Token: token}.Register(mux)

	// The JSON API. The table is generated from server/apis/, so an endpoint's
	// file is its URL and nothing is registered by hand.
	apistore.Use(store)
	routes := apis.FsApiRoutes()
	api.Register(mux, api.Config{Authorize: bearer(token)}, routes...)

	// Built at startup from the same table that was just registered, so it
	// cannot describe an endpoint this binary does not serve.
	mux.HandleFunc("GET /api/openapi.json", api.OpenAPI(api.Info{
		Title:       "Guard",
		Version:     "0.1.0",
		Description: "OTLP/HTTP telemetry receiver.",
	}, routes...))
	mux.HandleFunc("GET /api/docs", api.Docs("/api/openapi.json"))

	log.Fatal(a.Listen(mux))
}

// bearer is guard's permission layer, and it lives here rather than in the
// framework. howl-go carries the role strings an endpoint declared and asks
// this function what they mean; it has no user model and no opinion about
// tokens, sessions or scopes, which is the only way it can stay out of the way
// of an application that has all three.
//
// Guard's answer is deliberately small: one shared token, and every endpoint
// that changes state (writing a log, changing retention, purging) declares
// Roles: []string{"admin"}. With GUARD_TOKEN unset the instance is open, which
// is the right default for something you run on a laptop and the wrong one for
// anything reachable — hence the warning at startup.
func bearer(token string) func(*http.Request, []string) error {
	return func(r *http.Request, roles []string) error {
		if token == "" {
			slog.Warn("GUARD_TOKEN is unset — write endpoints are open",
				slog.String("path", r.URL.Path), slog.Any("roles", roles))
			return nil
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			return api.Unauthorized("a valid bearer token is required")
		}
		return nil
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
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
