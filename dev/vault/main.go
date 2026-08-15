// Command vaultdev runs guard-vault beside `make dev` and restarts it when its
// source changes.
//
// howl dev watches and rebuilds exactly one package, which is guard. The vault
// is a second binary, so it needs a second watcher — and it is a very small
// one, because the vault is a very small program: build it, run it, notice when
// its sources move, do it again.
//
// The child inherits this terminal rather than being piped through here. That
// keeps its output tinted and interleaved with guard's own, which is the point
// of running them together; its lines carry `app=vault` so the two are still
// tellable apart.
//
// Two behaviours are deliberate and worth keeping:
//
//   - **A failed build leaves the running vault alone.** The same bargain howl
//     dev makes: a compile error should cost you the new code, not the process
//     that is serving your application its configuration.
//   - **A child that exits is restarted, slowly.** On a fresh checkout the
//     database has no secrets tables until guard has opened it once, and the
//     vault refuses to start against one — correctly. Retrying turns that from
//     "the vault is broken" into "the vault came up a few seconds after guard".
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// watched lists the trees whose .go files this binary is built from. Anything
// else changing — the pages, the endpoints, the stylesheet — is guard's
// business and restarting the vault for it would just be noise.
var watched = []string{
	"cmd/vault",
	"internal/vault",
	"internal/telemetry",
	"internal/secrets",
}

func main() {
	addr := flag.String("addr", ":4319", "address for the vault")
	dbPath := flag.String("db", "guard.db", "the database guard writes")
	poll := flag.Duration("poll", 500*time.Millisecond, "how often to look for changes")
	flag.Parse()

	binary := filepath.Join(".howl", "guard-vault")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "vaultdev:", err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	runner := &runner{binary: binary, args: []string{"-addr", *addr, "-db", *dbPath}}
	defer runner.kill()

	if err := build(binary); err != nil {
		// Nothing is running yet, so there is nothing to protect: say so and
		// keep watching, because the fix is usually the next keystroke.
		fmt.Fprintln(os.Stderr, "vaultdev: build failed\n"+err.Error())
	} else {
		runner.start()
	}

	stamp := fingerprint()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if next := fingerprint(); next != stamp {
				stamp = next
				if err := build(binary); err != nil {
					fmt.Fprintln(os.Stderr, "vaultdev: build failed — the running vault is untouched\n"+err.Error())
					continue
				}
				fmt.Fprintln(os.Stderr, "vaultdev: rebuilt")
				runner.kill()
				runner.start()
				continue
			}
			// The database may not have had its tables yet when this last
			// tried. Nothing else here would notice it had given up.
			runner.reviveIfDead()
		}
	}
}

func build(binary string) error {
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/vault")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

// fingerprint is the modification times of everything watched, in one string.
// Cheap, and it notices a file being added or removed as readily as one being
// edited — which a "newest mtime" check does not.
func fingerprint() string {
	var out strings.Builder
	for _, dir := range watched {
		filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error { //nolint:errcheck
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			fmt.Fprintf(&out, "%s:%d;", path, info.ModTime().UnixNano())
			return nil
		})
	}
	return out.String()
}

type runner struct {
	binary string
	args   []string
	cmd    *exec.Cmd
	// done is closed when the child exits, by the one goroutine that waits on
	// it. Everything here asks that channel rather than calling Wait a second
	// time, which is a thing os/exec does not forgive.
	done chan struct{}
	// last is when the child was started, so a process that dies on startup is
	// retried on a timer rather than in a spin.
	last time.Time
}

func (r *runner) start() {
	cmd := exec.Command(r.binary, r.args...)
	// Inherited, not piped: the vault's own tinted output, on this terminal,
	// beside guard's.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "vaultdev:", err)
		return
	}
	done := make(chan struct{})
	r.cmd, r.done, r.last = cmd, done, time.Now()
	go func() {
		cmd.Wait() //nolint:errcheck
		close(done)
	}()
}

func (r *runner) reviveIfDead() {
	if r.cmd == nil {
		return
	}
	select {
	case <-r.done:
		if time.Since(r.last) < 3*time.Second {
			return
		}
		r.start()
	default:
	}
}

func (r *runner) kill() {
	if r.cmd == nil {
		return
	}
	r.cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		r.cmd.Process.Kill() //nolint:errcheck
		<-r.done
	}
	r.cmd = nil
}
