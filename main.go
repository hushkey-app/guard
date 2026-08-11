package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"

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
	capacity := flag.Int("capacity", 10_000, "maximum telemetry events retained in memory")
	flag.Parse()

	store := telemetry.NewStore(*capacity)
	public, err := fs.Sub(publicFS, "client/public")
	if err != nil {
		log.Fatal(err)
	}

	a := app.New(app.Config{
		Addr: *addr, Routes: pages.FsClientRoutes(), Shell: pages.App, NotFound: pages.NotFound, Public: public,
		Data: func(ctx context.Context, _ string) context.Context {
			return telemetry.WithSnapshot(ctx, store.Snapshot())
		},
	})
	mux := a.Mux()
	ingest.Handler{Store: store, Token: os.Getenv("GUARD_TOKEN")}.Register(mux)
	log.Printf("guard watching on http://localhost%s", *addr)
	log.Fatal(a.Listen(mux))
}
