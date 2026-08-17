// Package access is the two credentials guard is reached with, and the one
// place they can be replaced without an SSH session.
//
// `GUARD_TOKEN` opens every write endpoint in the API; `GUARD_OTEL_SECRET`
// opens the three ingest routes and nothing else. Both come from the
// environment, which means both come from a file on the box — so rotating one
// has always been "log in, generate 32 bytes, edit an env file, restart the
// unit". That is four steps, three of which are a shell, and the usual result
// is that neither is ever rotated at all.
//
// This package is those four steps behind a button. It owns one env file that
// guard itself may write — deliberately not `/etc/guard/guard.env`, which stays
// root-owned and holds the OAuth credentials and everything else typed by hand.
// systemd reads guard's file last, so a value generated here wins over the same
// name set by hand, and the button cannot quietly lose to a line somebody wrote
// a year ago.
//
// Writing it is not applying it: the process has its credentials from the
// environment it started with, and only a start reads an environment. So the
// state carries "the file says one thing and this process is running another",
// and restarting is a second, explicit press — because the restart drops the
// dashboard it was pressed on, along with any OTLP in flight.
//
// Guard does not restart itself by calling systemctl; it runs unprivileged with
// NoNewPrivileges and could not. It exits, and the unit's Restart=always brings
// it back two seconds later against the new file. That is also why the restart
// is offered only under systemd: exiting anywhere else is just stopping.
package access

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// DefaultPath is the file guard writes. It is under a directory of its own —
// /etc/guard is root's, and only this one directory inside it is handed to the
// guard user, so a write here cannot become a write to guard.env beside it.
const DefaultPath = "/etc/guard/env.d/tokens.env"

// The two names this package knows. Nothing else may be written to the file:
// it is loaded by systemd into guard's environment, so "a name and a value"
// would be "any environment variable you like", set from a browser.
const (
	// NameToken is the operator's credential: the API and ingest.
	NameToken = "GUARD_TOKEN"
	// NameSecret is the collector's: ingest only, and safe to hand to a box
	// somebody else administers.
	NameSecret = "GUARD_OTEL_SECRET"
)

// Keys reads and writes that file.
type Keys struct {
	// Path is the env file. Empty means DefaultPath.
	Path string
	// Running is what this process actually started with — read from the
	// environment in main, not from the file, because guard.env, a container's
	// -e flag and a shell all set it too and the file is only one of them.
	Running Credentials
	// Restart asks the supervisor for a new process. Nil means guard has no way
	// to come back from exiting, and the dashboard offers no button.
	Restart func()

	mu sync.Mutex
}

// Credentials is the pair.
type Credentials struct {
	Token  string
	Secret string
}

// State is what the settings card draws.
//
// The values are in the clear, and that is the one place this differs from
// every other credential guard holds. An SSH password is one guard uses on
// somebody's behalf and never reads out; these two are values a person has to
// paste into a collector's env file on another box, and a card that shows dots
// is a card that sends them back to the shell it was meant to replace. The
// endpoints are admin-only, so anyone who can read this already holds the
// token — on an instance with no token and no sign-in there is nothing here to
// leak, because there is nothing set.
type State struct {
	// Token and Secret are what the next start will use: the file's value where
	// it names one, and otherwise whatever this process has.
	Token  string `json:"token,omitempty"`
	Secret string `json:"secret,omitempty"`
	// Pending says the file and this process disagree — generated, not yet
	// restarted into. The card says so rather than claiming a rotation that has
	// not happened, because the collector that matters is still presenting the
	// old one.
	TokenPending  bool `json:"token_pending"`
	SecretPending bool `json:"secret_pending"`
	// Managed says guard can write the file: the directory exists and is
	// writable. On a laptop or a container it does not, and the card explains
	// the env file instead of offering a button that cannot work.
	Managed bool `json:"managed"`
	// Restartable says something will start guard again if it exits.
	Restartable bool   `json:"restartable"`
	Path        string `json:"path"`
}

// State reports the file, the process, and whether the two agree.
func (k *Keys) State() State {
	stored := k.stored()
	state := State{
		Token:       first(stored[NameToken], k.Running.Token),
		Secret:      first(stored[NameSecret], k.Running.Secret),
		Managed:     k.writable(),
		Restartable: k.Restart != nil,
		Path:        k.path(),
	}
	state.TokenPending = stored[NameToken] != "" && stored[NameToken] != k.Running.Token
	state.SecretPending = stored[NameSecret] != "" && stored[NameSecret] != k.Running.Secret
	return state
}

// Generate mints a new value for one of the two names and writes the file.
//
// It does not touch the other one. Rotating the collector's secret and
// rotating the operator's token are separate days: the first is done when a box
// is decommissioned, the second when a laptop is lost, and doing both because
// one was asked for means every collector in the fleet stops at once.
func (k *Keys) Generate(name string) (State, error) {
	value, err := mint()
	if err != nil {
		return k.State(), err
	}
	return k.write(name, value)
}

// Clear removes one name from the file, so the next start falls back to
// whatever else sets it — a line in guard.env, or nothing at all.
func (k *Keys) Clear(name string) (State, error) { return k.write(name, "") }

func (k *Keys) write(name, value string) (State, error) {
	if name != NameToken && name != NameSecret {
		return k.State(), fmt.Errorf("%q is not a credential guard writes", name)
	}
	if !k.writable() {
		return k.State(), errors.New("this instance cannot write " + k.path() + " — set the credential in the environment instead")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	stored := k.stored()
	if value == "" {
		delete(stored, name)
	} else {
		stored[name] = value
	}
	// Written beside and renamed over, because the reader is systemd at boot:
	// a half-written file is a box that starts with half a token, which looks
	// exactly like a token that was revoked.
	temp := k.path() + ".new"
	if err := os.WriteFile(temp, render(stored), 0o600); err != nil {
		return k.State(), fmt.Errorf("could not write %s: %w", temp, err)
	}
	if err := os.Rename(temp, k.path()); err != nil {
		os.Remove(temp) //nolint:errcheck
		return k.State(), fmt.Errorf("could not write %s: %w", k.path(), err)
	}
	// The value never goes near a log line; what happened does.
	slog.Info("credential written", slog.String("name", name), slog.String("path", k.path()),
		slog.Bool("cleared", value == ""))
	return k.State(), nil
}

// Ask restarts guard so the file becomes the environment.
//
// The caller gets its answer first — the response is written, and the exit
// happens behind it — because the page that pressed this is about to lose the
// connection and "did it take?" is the one thing it needs to know before that.
func (k *Keys) Ask() error {
	if k.Restart == nil {
		return errors.New("nothing here would start guard again — restart the service by hand")
	}
	slog.Warn("restarting to pick up the credentials on disk", slog.String("path", k.path()))
	k.Restart()
	return nil
}

// stored is the file, parsed. A missing or unreadable file is an empty map
// rather than an error: not having written one yet is the normal state.
//
// The parser is the one the vault's .env import uses, so guard reads its own
// file with the same dialect it accepts from anybody else.
func (k *Keys) stored() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(k.path())
	if err != nil {
		return out
	}
	pairs, _ := model.ParseEnv(string(raw))
	for _, pair := range pairs {
		if pair.Key == NameToken || pair.Key == NameSecret {
			out[pair.Key] = pair.Value
		}
	}
	return out
}

func render(values map[string]string) []byte {
	var out strings.Builder
	out.WriteString("# Written by guard, from Settings. Do not edit by hand — the next\n")
	out.WriteString("# press rewrites this whole file. Credentials set by hand belong in\n")
	out.WriteString("# /etc/guard/guard.env, which the unit reads before this one.\n")
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.WriteString(name + "=" + values[name] + "\n")
	}
	return []byte(out.String())
}

// mint is 32 bytes of randomness as hex — the same thing
// `openssl rand -hex 32` produces, because that is what every runbook for this
// says and a credential that looks unfamiliar is one somebody double-checks.
func mint() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not generate a credential: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (k *Keys) path() string {
	if strings.TrimSpace(k.Path) == "" {
		return DefaultPath
	}
	return k.Path
}

// writable reports a box where the file can be written: the directory exists
// and guard may create in it. Checked by asking the filesystem rather than by
// trusting a flag, because the answer changes when somebody fixes the
// permissions and no restart should be needed to notice.
func (k *Keys) writable() bool {
	dir := filepath.Dir(k.path())
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	// The file may not exist yet, so the directory is what is tested. A probe
	// file is the only honest test of "may I create here" — the mode bits lie
	// under a mount option, a container's user namespace, and ProtectSystem.
	probe := filepath.Join(dir, ".guard-write-probe")
	file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	file.Close()     //nolint:errcheck
	os.Remove(probe) //nolint:errcheck
	return true
}

// Supervised reports whether something will start guard again if it exits.
// systemd sets INVOCATION_ID for every service it runs, so the answer is the
// supervisor's own word rather than a setting somebody has to keep true.
func Supervised() bool { return os.Getenv("INVOCATION_ID") != "" }

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
