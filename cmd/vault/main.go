// Command guard-vault hands stored secrets to the applications that need them.
//
// It is deliberately small, and it ships beside guard rather than inside it. An
// application asking for its database password at boot should not depend on a
// dashboard being healthy — the two share a database file and a key, and
// nothing else. Guard owns the schema and every write; this reads.
//
//	guard-vault                      serve on :4319, reading ./guard.db
//	guard-vault -db /data/guard.db   the usual deployment
//	guard-vault fetch -env local     print one environment as .env, no server
//	guard-vault fetch -workspace hushkey -env local
//
// The environment a request may read comes from the key it presents, so there
// is nothing to configure per application beyond a URL and a token.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/internal/vault"
	"github.com/mirairoad/howl-go/core/console"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fetch" {
		if err := fetch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("guard-vault", flag.ExitOnError)
	addr := flags.String("addr", env("GUARD_VAULT_ADDR", ":4319"), "HTTP listen address")
	dbPath := flags.String("db", env("GUARD_DB_PATH", "guard.db"), "the SQLite database guard writes")
	touch := flags.Duration("touch", envDuration("GUARD_VAULT_TOUCH", time.Minute), "how often one key's use is recorded")
	if err := flags.Parse(args); err != nil {
		return err
	}
	// Tagged, because in development this shares a terminal with guard: two
	// processes writing tinted lines to one screen, and `app=vault` is the
	// difference between reading that and untangling it.
	slog.SetDefault(console.Setup(console.Options{}).With(slog.String("app", "vault")))

	store, err := vault.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	mux := http.NewServeMux()
	(&vault.Server{Store: store, Touch: *touch}).Register(mux)
	server := &http.Server{
		Addr:    *addr,
		Handler: mux,
		// Short and fixed. Everything here is one indexed read and a decrypt;
		// a request that has taken ten seconds is not going to finish.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown) //nolint:errcheck
	}()

	slog.Info("guard-vault", slog.String("addr", *addr), slog.String("db", *dbPath))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// fetch prints one environment as .env text, straight from the file.
//
// No HTTP and no token: this is the local case — seeding a .env for a checkout,
// or reading the values on a box where the server is what has gone wrong.
// Whoever runs it already has the database and the key, which is everything a
// token would have protected.
func fetch(args []string) error {
	flags := flag.NewFlagSet("guard-vault fetch", flag.ExitOnError)
	dbPath := flags.String("db", env("GUARD_DB_PATH", "guard.db"), "the SQLite database guard writes")
	workspace := flags.String("workspace", env("GUARD_WORKSPACE", ""), "which application's environment to print; needed once there is more than one")
	name := flags.String("env", "local", "which environment to print")
	if err := flags.Parse(args); err != nil {
		return err
	}
	store, err := vault.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	id, err := store.EnvByName(*workspace, *name)
	if err != nil {
		if strings.Contains(err.Error(), "workspace") {
			return err
		}
		if *workspace == "" {
			return fmt.Errorf("no environment called %q", *name)
		}
		return fmt.Errorf("no environment called %q in workspace %q", *name, *workspace)
	}
	pairs, _, err := store.Values(id)
	if err != nil {
		return err
	}
	readable := make([]model.Secret, 0, len(pairs))
	for _, pair := range pairs {
		if pair.Unreadable {
			// To stderr, so a redirect into .env gets the values and the
			// person gets the warning.
			fmt.Fprintf(os.Stderr, "%s was sealed with a different key and is not in this output\n", pair.Key)
			continue
		}
		readable = append(readable, pair)
	}
	fmt.Print(model.FormatEnv(readable))
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}
