package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"strconv"

	"github.com/mirairoad/guard/client/pages"
	"github.com/mirairoad/guard/internal/ingest"
	"github.com/mirairoad/guard/internal/telemetry"
	"github.com/mirairoad/howl-go/core/app"
)

//go:generate go run github.com/mirairoad/howl-go/core/cmd/fsroutes -module github.com/mirairoad/guard/client/pages

//go:embed client/public
var publicFS embed.FS

func main() {
	addr := flag.String("addr", ":4318", "HTTP and OTLP/HTTP listen address")
	dbPath := flag.String("db", env("GUARD_DB_PATH", "guard.db"), "SQLite database file")
	retentionHours := flag.Int("retention-hours", envInt("GUARD_RETENTION_HOURS", 24), "hours of telemetry to retain")
	maxEvents := flag.Int("max-events", envInt("GUARD_MAX_EVENTS", 1_000_000), "maximum telemetry events retained")
	flag.Parse()

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
		Data: func(ctx context.Context, _ string) context.Context {
			summary, err := store.Snapshot()
			if err != nil {
				log.Printf("snapshot: %v", err)
			}
			return telemetry.WithSnapshot(ctx, summary)
		},
	})
	mux := a.Mux()
	ingest.Handler{Store: store, Token: os.Getenv("GUARD_TOKEN")}.Register(mux)
	log.Printf("guard watching on http://localhost%s", *addr)
	log.Fatal(a.Listen(mux))
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
