package deploy

// What actually goes over the wire: two files and two docker commands.
//
// The shape is internal/envfile's, deliberately, because it is the same act —
// guard putting a file on a machine — and the same three rules keep it safe:
//
//   - **Guard writes what guard rendered.** The compose file comes out of a
//     stored template and the .env out of the template's variables with the tag
//     on top. No caller passes file content, so a deploy cannot become a way to
//     drop a chosen file in a chosen place. The directory is checked by
//     `model.ValidateDeployPath` before it ever reaches a shell.
//   - **The values travel as base64.** A compose file is full of colons, quotes
//     and dollars, and a .env holds passwords. Base64 has no metacharacters, so
//     there is no quoting bug to have.
//   - **Each file is replaced atomically and the old one kept** as `.guard-bak`,
//     so nothing ever reads half a compose file, and the state before a bad
//     deploy is still on the box.
//
// The .env is 0600 where the compose file is 0644, and that is the one
// difference from envfile: a .env holds resolved secrets, and the machine's
// environment is world-readable on purpose because a login that cannot read it
// gets no environment at all. Here nothing but docker needs to read it.

import (
	"encoding/base64"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// The names guard writes inside the template's directory. Fixed, like
// envfile's paths: a filename to fill in is a chore, and `docker compose` finds
// these two without being told.
const (
	ComposeFile = "docker-compose.yml"
	EnvFile     = ".env"
)

// Command is the whole deploy for one machine: write both files, then pull and
// recreate the service.
//
// `set -e` throughout, so a machine that cannot write the file never goes on to
// recreate a container against the old one. The output is what docker said,
// which is what the pane on the page shows — a deploy that failed is usually
// explained by its own last three lines.
func Command(template model.DeployTemplate, tag string, vars []model.NodeEnvVar) (string, error) {
	if err := model.ValidateDeployPath(template.Path); err != nil {
		return "", err
	}
	if err := model.ValidateTag(tag); err != nil {
		return "", err
	}
	if err := model.ValidateEnvVars(vars); err != nil {
		return "", err
	}
	dir := template.Path
	var out strings.Builder
	out.WriteString("set -e; umask 077; mkdir -p " + quote(dir) + "; ")
	out.WriteString(write(dir+"/"+ComposeFile, template.ComposeYAML, "644"))
	out.WriteString(write(dir+"/"+EnvFile, model.EnvFor(tag, vars), "600"))
	out.WriteString("cd " + quote(dir) + "; ")
	// Which docker this box has. The plugin is what a current install gives you
	// and the standalone script is what a box provisioned three years ago has;
	// asking is one line and being wrong is a deploy that fails on a machine
	// where everything is fine.
	out.WriteString(`if docker compose version >/dev/null 2>&1; then dc="docker compose"; ` +
		`elif command -v docker-compose >/dev/null 2>&1; then dc="docker-compose"; ` +
		`else echo "no docker compose on this machine" >&2; exit 127; fi; `)
	// Pull first and separately: an image that will not pull must not take the
	// running container down with it, and "pull failed" is a different thing to
	// be told than "it came up and died".
	out.WriteString(`$dc pull; `)
	out.WriteString(`$dc up -d --remove-orphans; `)
	return out.String(), nil
}

// write is one file: content in base64, into a temp file beside the target,
// mode set, previous kept, renamed over.
func write(path, content, mode string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return `d=$(dirname -- ` + quote(path) + `); t=$(mktemp "$d/.guard-deploy.XXXXXX"); ` +
		`printf %s ` + quote(encoded) + ` | base64 -d > "$t"; chmod ` + mode + ` "$t"; ` +
		`if [ -f ` + quote(path) + ` ]; then cp -p -- ` + quote(path) + ` ` + quote(path+".guard-bak") + `; fi; ` +
		`mv -f -- "$t" ` + quote(path) + `; `
}

// quote wraps a string as one single-quoted shell word. Everything it is given
// is either base64 or a path `model.ValidateDeployPath` has already refused a
// quote in — it is here so that reading the command tells you that.
func quote(value string) string { return "'" + value + "'" }
