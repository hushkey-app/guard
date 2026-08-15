// Package cluster is the endpoint layer for the machines guard watches from
// the outside.
//
// An instance is derived from telemetry and disappears when the telemetry
// does — which is the moment you most want to hear about it. A node is
// declared, so guard can say "VPS-1 has been down for six minutes" about a
// machine that has stopped talking to anyone.
package cluster

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/prober"
	"github.com/hushkey-app/guard/server/apis/runner"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// wake nudges the scheduler after the node list changes, so an edit takes
// effect while its author is still looking at the page. Nil-safe: a guard
// running without a prober — a test server — simply has nothing to nudge.
func wake() {
	if p := prober.Get(); p != nil {
		p.Wake()
	}
}

// verifySSH connects with the credentials that are about to be stored, and
// refuses the write if it cannot get in.
//
// Because the alternative is worse than it looks: a machine saved with a
// mistyped password looks exactly like a machine saved correctly, and the
// difference is discovered at 3am by somebody pressing "Reboot" on a box that
// is already on fire. Checking here means a login that was accepted is a login
// that worked at least once.
//
// No SSH address is not a failure — a machine can be watched without anybody
// being able to log in to it, and most are. Only a login that was *given* and
// does not work is refused.
//
// Returns the host key seen, so the caller can pin it: the connection just made
// is the first-use in trust-on-first-use.
func verifySSH(ctx context.Context, node model.Node, password string) (string, error) {
	if strings.TrimSpace(node.SSHAddress) == "" {
		return "", nil
	}
	user, address, ok := node.SSHDial()
	if !ok {
		return "", api.BadRequest("ssh address must be user@host, like root@10.10.10.10")
	}
	if strings.TrimSpace(password) == "" {
		return "", api.BadRequest("an ssh address needs a password to go with it")
	}
	engine := runner.Get()
	if engine == nil {
		// An instance with no runner cannot check, and refusing every save
		// because of that would make the field unusable rather than safe.
		return "", nil
	}
	result, err := engine.Probe(ctx, remote.Login{
		User: user, Address: address, Password: password, Fingerprint: node.SSHFingerprint,
	})
	if err != nil {
		// The runner's message already names the host and says what went wrong;
		// wrapping it again would print the address twice in one sentence.
		return "", api.BadRequest(err.Error())
	}
	return result.Fingerprint, nil
}

// execute runs one command on one machine and returns what happened.
//
// A command that failed to connect, timed out or was refused comes back as a
// Run carrying the reason, not as an HTTP error. The distinction the reader
// cares about is "did my command work", and a 500 with a correlation id answers
// a different question — the output is the answer, including when the output is
// "the password was refused".
//
// Only the reasons guard itself cannot proceed on — no runner wired, no ssh
// address, no stored password — are errors, because those are configuration
// rather than results.
func execute(ctx context.Context, nodeID int64, command string) (model.Run, error) {
	run := model.Run{Command: command, RanAt: time.Now().UTC()}
	engine := runner.Get()
	if engine == nil {
		return run, api.Unavailable("this instance is running without an ssh runner")
	}
	login, err := store.Get().SSHLoginFor(nodeID)
	if err != nil {
		return run, api.BadRequest(err.Error())
	}

	result, runErr := engine.Run(ctx, remote.Login{
		User:        login.User,
		Address:     login.Address,
		Password:    login.Password,
		Fingerprint: login.Fingerprint,
	}, command)
	run.Output = result.Output
	run.ExitCode = result.ExitCode
	run.DurationMS = result.DurationMS
	run.Truncated = result.Truncated
	// Trust on first use: the key seen on the first successful connection is
	// the one every later connection must present.
	if result.Fingerprint != "" && login.Fingerprint == "" {
		if err := store.Get().PinFingerprint(nodeID, result.Fingerprint); err != nil {
			slog.Error("ssh host key not stored", slog.Int64("node", nodeID), slog.Any("err", err))
		}
	}
	if runErr != nil {
		run.Error = runErr.Error()
		// Distinct from any exit status a shell can produce, so the dashboard
		// can tell "the command said no" from "there was no command".
		run.ExitCode = -1
	}
	// Worth a line in the log whatever happened: this is the one part of guard
	// that changes somebody else's machine, and the log is where that fact
	// outlives the browser tab it was done from.
	slog.Info("ran a command over ssh",
		slog.Int64("node", nodeID), slog.String("user", login.User), slog.String("address", login.Address),
		slog.String("command", command), slog.Int("exit", run.ExitCode), slog.String("err", run.Error))
	return run, nil
}

// List returns every node with its latest check, a day of uptime and enough
// recent history to draw a strip. One request for the whole cluster: the list
// is small by nature, and a request per node would make the settings page
// slower the more machines you watch.
var List = api.Define(api.Spec[api.None, api.None, []model.Node]{
	Name: "Cluster",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Node, error) {
		return store.Get().Nodes()
	},
})
