// Package envfile puts a machine's environment on the machine.
//
// One command, over the SSH login the machine already has, writing the variables
// guard keeps for a box into the two places Linux takes an environment from:
//
//	/etc/environment                               logins, PAM, anything shell
//	/etc/systemd/system.conf.d/10-guard-env.conf    every systemd service
//
// Both, because "the machine's environment" is a different file depending on what
// is reading it, and writing only the first would do nothing for a box whose apps
// are systemd units — which is most of them. A `daemon-reexec` follows so the
// manager re-reads its defaults; each service keeps the environment it started
// with until it is restarted, and guard says that rather than implying otherwise.
//
// Three rules keep it safe:
//
//   - **The values travel as base64.** Not because they are secret — they are
//     going over SSH either way — but because an environment holds passwords with
//     quotes and dollars in them, and every bug in a thing like this is a quoting
//     bug. Base64 has no metacharacters.
//   - **Guard writes what guard rendered.** The content is `model.RenderEnvVars`
//     over the stored variables. No caller sends file content, so this cannot
//     become a way to drop an arbitrary file on a box.
//   - **Each file is replaced atomically and the old one kept** as `.guard-bak`:
//     written to a temporary name beside the target and renamed over it, so nothing
//     ever reads half an environment.
package envfile

import (
	"encoding/base64"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Where a machine takes its environment from. Fixed paths rather than settings:
// the point of this is that somebody types variables and presses a button, and a
// path to fill in first is what turns that into a chore.
const (
	// EnvironmentPath is read by PAM for every login session.
	EnvironmentPath = "/etc/environment"
	// SystemdPath is a drop-in for the service manager, so every unit started
	// after it inherits these.
	SystemdPath = "/etc/systemd/system.conf.d/10-guard-env.conf"
)

// Render is what goes into /etc/environment. The header matters: the next person
// to open this file needs to know it is generated and what regenerates it.
func Render(vars []model.NodeEnvVar) string {
	return "# Written by guard. Edit this machine's environment from the dashboard;\n" +
		"# the next inject replaces this file and keeps the old one as .guard-bak.\n" +
		model.RenderEnvVars(vars)
}

// RenderSystemd is the same variables as a systemd drop-in.
//
// One DefaultEnvironment= line per variable rather than one long one: systemd
// takes the union of them, and a single line is a single thing to get wrong.
func RenderSystemd(vars []model.NodeEnvVar) string {
	var out strings.Builder
	out.WriteString("# Written by guard. Every systemd service started after this\n")
	out.WriteString("# inherits these; restart a service to pick them up.\n[Manager]\n")
	for _, entry := range vars {
		out.WriteString("DefaultEnvironment=" + entry.Key + "=" + model.EnvQuote(entry.Value) + "\n")
	}
	return out.String()
}

// InjectCommand writes both files and asks systemd to re-read its configuration.
//
// It prints what it did, so the dashboard reports the machine's own account of it
// rather than its own assumption.
func InjectCommand(vars []model.NodeEnvVar) (string, error) {
	if err := model.ValidateEnvVars(vars); err != nil {
		return "", err
	}
	return "set -e; umask 077; " +
		write(EnvironmentPath, Render(vars)) +
		// The drop-in directory usually exists; a box that has never had one
		// still needs it, and `mkdir -p` is the whole difference.
		`mkdir -p /etc/systemd/system.conf.d; ` +
		write(SystemdPath, RenderSystemd(vars)) +
		// Re-exec rather than reload: DefaultEnvironment is manager
		// configuration, and only a re-exec makes the manager read it again. It
		// keeps every running service running.
		`if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reexec || true; fi; ` +
		`echo ` + quote("wrote "+EnvironmentPath+" and "+SystemdPath), nil
}

// write is one file: content in base64, into a temp file beside the target, mode
// set, previous kept, renamed over.
//
// 0644 rather than the mode that was there, because both of these are guard's own
// files and that is what they are: an /etc/environment ordinary users cannot read
// is a login that gets no environment at all.
func write(path, content string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return `d=$(dirname -- ` + quote(path) + `); t=$(mktemp "$d/.guard-env.XXXXXX"); ` +
		`printf %s ` + quote(encoded) + ` | base64 -d > "$t"; chmod 644 "$t"; ` +
		`if [ -f ` + quote(path) + ` ]; then cp -p -- ` + quote(path) + ` ` + quote(path+".guard-bak") + `; fi; ` +
		`mv -f -- "$t" ` + quote(path) + `; `
}

// quote wraps a string as one single-quoted shell word. Everything it is given
// here is a constant path or base64, neither of which can contain a quote — it is
// here so that reading the command tells you that.
func quote(value string) string { return "'" + value + "'" }
