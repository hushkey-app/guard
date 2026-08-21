package deploy

// Making a machine deployable: docker, and the compose plugin.
//
// This is `internal/envfile`'s shape, and for the same reason. The request that
// reaches it carries a **node id and nothing else** — the command below is a
// constant in guard's source, the login comes off the machine, and there is no
// shape of the call that runs chosen text on a chosen box. That is what lets it
// be one button rather than a stored command somebody has to write first.
//
// It exists because the alternative is worse DX and worse security: without it,
// the answer to "this box has no compose plugin" is somebody opening a terminal,
// pasting a curl-to-shell from a chat window into a root session, and guard
// having no record that it happened. Here it is one press, over the login guard
// already proved, and the machine's own account of it is stored on the row.
//
// Two things it deliberately is not:
//
//   - **Not part of a deploy.** A deploy that silently installed a package
//     manager's worth of software the first time it ran would be a deploy that
//     does something nobody asked for on the worst possible day. Guard's deploy
//     refuses with `no docker compose on this machine` and this is the separate
//     press that answers it.
//   - **Not a way to run anything else.** It installs docker and starts it. It
//     does not configure a firewall, add users or touch what is running.

import (
	"log/slog"
	"strings"
)

// Preparation is what one press did, in the machine's own words.
type Preparation struct {
	// Changed is false when docker and the plugin were already there. The press
	// is safe to repeat, and saying which of the two happened is the difference
	// between "it worked" and "it was already fine".
	Changed bool   `json:"changed"`
	Docker  string `json:"docker,omitempty"`
	Compose string `json:"compose,omitempty"`
	// Running says the machine is still talking. The page polls while it is
	// true, and the output grows underneath it.
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
	Output  string `json:"output"`
}

// alreadyThere is what the script prints when there was nothing to do. Matched
// rather than guessed at, so `Changed` is the machine's answer and not this
// package's assumption about exit codes.
const alreadyThere = "guard: already installed"

// PrepareCommand installs docker and the compose plugin, and is a constant.
//
// The order matters and is the whole of the design:
//
//  1. **Already working is the first branch**, so the press is idempotent and
//     costs one SSH round trip on a box that is fine.
//  2. **Docker present but no plugin** is the common case on a machine somebody
//     set up two years ago, and it is a package from the distribution — not a
//     script from the internet.
//  3. **No docker at all** falls back to `get.docker.com`, which is Docker's own
//     documented installer. It is a script piped into a root shell, which is
//     worth saying out loud rather than hiding: it is downloaded to a file
//     first, run, and removed, so what ran is at least a thing that existed on
//     disk for a moment rather than a pipe. Anyone who would rather not can
//     install docker their own way and press this afterwards, where it takes
//     branch one.
func PrepareCommand() string {
	return strings.Join([]string{
		`set -e`,
		// Already good. Nothing is downloaded, nothing is installed.
		`if docker compose version >/dev/null 2>&1; then`,
		`  echo "` + alreadyThere + `"; docker --version; docker compose version; exit 0; fi`,
		`if command -v docker >/dev/null 2>&1; then`,
		`  echo "guard: docker is here, the compose plugin is not";`,
		`  if command -v apt-get >/dev/null 2>&1; then`,
		`    export DEBIAN_FRONTEND=noninteractive; apt-get update -qq; apt-get install -y -qq docker-compose-plugin;`,
		`  elif command -v dnf >/dev/null 2>&1; then dnf install -y -q docker-compose-plugin;`,
		`  elif command -v yum >/dev/null 2>&1; then yum install -y -q docker-compose-plugin;`,
		`  else echo "guard: no apt-get, dnf or yum here — install the compose plugin by hand" >&2; exit 1; fi`,
		`else`,
		`  echo "guard: installing docker from get.docker.com";`,
		`  if command -v curl >/dev/null 2>&1; then curl -fsSL https://get.docker.com -o /tmp/guard-get-docker.sh;`,
		`  elif command -v wget >/dev/null 2>&1; then wget -qO /tmp/guard-get-docker.sh https://get.docker.com;`,
		`  else echo "guard: neither curl nor wget on this machine" >&2; exit 1; fi`,
		`  sh /tmp/guard-get-docker.sh; rm -f /tmp/guard-get-docker.sh;`,
		`fi`,
		// Started and enabled, because an installed docker that does not come
		// back after a reboot is a deploy that fails at 4am rather than now.
		`if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker || true; fi`,
		// The last word is the proof. `set -e` means a machine that still cannot
		// answer these two is a failed press rather than a cheerful one.
		`docker --version`,
		`docker compose version`,
	}, "\n")
}

// Prepare starts the install and answers immediately.
//
// Immediately, because installing docker on a cold box is a minute or more of a
// package manager talking, and a request held open for it is a page that shows
// nothing and then everything. It runs in the background like a deploy does, the
// output is collected as it arrives, and `Preparing` is what the page polls —
// the same bargain the deploy rows make, one size smaller.
//
// The login is read through `DeployTarget`, so the lock applies: this installs
// packages as root, which is exactly the class of thing a locked machine exists
// to refuse. Read here rather than in the goroutine so a locked machine is a
// refusal at the press instead of a failure discovered by polling.
func (r *Runner) Prepare(nodeID int64) (Preparation, error) {
	r.prepare()
	login, err := r.Store.DeployTarget(nodeID)
	if err != nil {
		return Preparation{}, err
	}
	r.mu.Lock()
	if running, busy := r.preparing[nodeID]; busy && running.Running {
		r.mu.Unlock()
		return *running, nil
	}
	ctx := r.ctx
	report := &Preparation{Running: true, Output: "guard: connecting…\n"}
	r.preparing[nodeID] = report
	r.mu.Unlock()

	go func() {
		result, runErr := r.run(ctx, login, PrepareCommand(), func(sofar string) {
			r.mu.Lock()
			report.Output = sofar
			r.mu.Unlock()
		}, func(sofar string) {
			r.publish(Frame{Kind: KindPrepare, NodeID: nodeID, Status: "installing", Output: sofar})
		})
		if result.Fingerprint != "" {
			if err := r.Store.PinFingerprint(nodeID, result.Fingerprint); err != nil {
				r.Log.Error("could not pin a host key", slog.Int64("node", nodeID), slog.Any("err", err))
			}
		}
		r.mu.Lock()
		report.Running = false
		report.Output = result.Output
		switch {
		case runErr != nil:
			report.Error = runErr.Error()
		case result.ExitCode != 0:
			report.Error = lastLine(result.Output)
		default:
			report.Changed = !strings.Contains(result.Output, alreadyThere)
			report.Docker, report.Compose = versions(result.Output)
		}
		finished := *report
		r.mu.Unlock()

		// Outside the lock, always. publish takes the same mutex, and a
		// mutex that is not reentrant plus a publish inside a critical section
		// is a deadlock that stops the runner and everything it holds.
		r.Log.Info("machine prepared for deploys", slog.Int64("node", nodeID),
			slog.Bool("changed", finished.Changed), slog.String("err", finished.Error))
		r.publish(Frame{Kind: KindPrepare, NodeID: nodeID, Status: "done",
			Output: finished.Output, Done: true})
	}()
	return *report, nil
}

// Preparing is what one machine's install is doing right now, for the page to
// poll. In memory, like the deploy locks: a restart loses it, and the press is
// idempotent, so the answer to a lost one is to press it again.
func (r *Runner) Preparing(nodeID int64) (Preparation, bool) {
	r.prepare()
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.preparing[nodeID]
	if !ok {
		return Preparation{}, false
	}
	return *report, true
}

// versions reads the two lines the script ends with, so the answer says what is
// now on the machine rather than what guard hoped it installed.
func versions(output string) (docker, compose string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Docker version"):
			docker = line
		case strings.HasPrefix(line, "Docker Compose version"), strings.HasPrefix(line, "docker-compose version"):
			compose = line
		}
	}
	return docker, compose
}
