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
	"fmt"
	"regexp"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// composeService is model's servicePattern, kept here too: this package turns
// the name into shell, so it does its own checking rather than trusting that
// whoever built the struct went through Validate.
var composeService = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

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
	// The service name reaches the command as a bare shell word, so it is
	// checked here as well as at the save. `model.DeployTemplate.Validate`
	// already holds it to the same characters; this is the line that means a
	// template arriving from anywhere else cannot smuggle a shell metacharacter
	// into the one field that is not base64.
	service := template.ServiceName
	// A template saved before guard derived the service from the compose file
	// can carry a name the file does not have — the old default was a slug of
	// the *template's* name, which is what a person calls the deploy rather
	// than what the file calls the container. Deploying that addresses nothing
	// and docker says `no such service`. The file is the authority, so it is
	// read here rather than trusting a stored guess, and the substitution is
	// announced in the output instead of happening quietly.
	corrected := ""
	if declared := model.ServicesInCompose(template.ComposeYAML); len(declared) > 0 {
		has := false
		for _, name := range declared {
			if name == service {
				has = true
				break
			}
		}
		if !has {
			tagged := model.ServiceForTag(template.ComposeYAML)
			if tagged == "" {
				return "", fmt.Errorf("this compose file has no service called %s — it declares %s",
					service, strings.Join(declared, ", "))
			}
			corrected = fmt.Sprintf("guard: this template names %q, which the compose file does not have — deploying %q, the service tagged with ${TAG}",
				service, tagged)
			service = tagged
		}
	}
	if !composeService.MatchString(service) {
		return "", fmt.Errorf("%q is not a compose service name", service)
	}
	dir := template.Path
	var out strings.Builder
	if corrected != "" {
		out.WriteString("echo " + quote(corrected) + "; ")
	}
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
	//
	// Only this template's service is pulled, because only this template's
	// service is being deployed. A sidecar's image is whatever the compose file
	// pins and pulling it every deploy is a round trip that can only change
	// something nobody asked to change.
	out.WriteString(`$dc pull ` + service + `; `)
	// What comes up, and this is the part with a choice in it.
	//
	// Guard rewrites the .env on every deploy, so its hash changes, so a plain
	// `up -d` recreates **every** service that reads it — including the one
	// holding :80, which then races its own outgoing container for the port and
	// loses. A deploy that changed one image has no business restarting a
	// reverse proxy, and `ServiceName` has always been documented as the thing
	// that gets recreated.
	//
	// But it cannot be the only shape: a fresh box, or a compose file that has
	// grown a service, needs everything started, and `--no-deps` would leave a
	// machine holding one container and no proxy. So the file is asked first —
	// if anything it declares is not up, this is a deploy that has to bring the
	// project up; otherwise it touches the one service it names.
	out.WriteString(`missing=; for svc in $($dc config --services); do ` +
		`if [ -z "$($dc ps -q "$svc" 2>/dev/null)" ]; then missing="$missing $svc"; fi; done; `)
	out.WriteString(`if [ -n "$missing" ]; then ` +
		`echo "guard: bringing up the project ($missing not running)"; up="$dc up -d --remove-orphans"; ` +
		`else echo "guard: recreating ` + service + ` only"; up="$dc up -d --no-deps ` + service + `"; fi; `)
	// And the recovery, for the one failure a retry actually cures.
	//
	// `port is already allocated` after a recreate is docker not yet having let
	// go of the outgoing container's binding. It is not a reason to go hunting
	// for whatever holds the port and kill it: the process on the other end of
	// `lsof -ti :80` is usually docker's own proxy, killing it leaves the daemon
	// believing the mapping is live, and on a box where it is somebody's nginx
	// it is an outage guard caused. So the retry is scoped to this project, in
	// this directory, through compose — `down` releases the bindings properly
	// and `up` puts the whole thing back. Once, and then it gives up, because a
	// port held by something that is not ours will never come free and a loop
	// would only take longer to say so.
	out.WriteString(`log=$(mktemp); status=$(mktemp); `)
	out.WriteString(`{ set +e; $up 2>&1; echo $? > "$status"; } | tee "$log"; `)
	out.WriteString(`code=$(cat "$status"); `)
	out.WriteString(`if [ "$code" != "0" ]; then ` +
		`if grep -qiE 'port is already allocated|address already in use|failed to set up container networking' "$log"; then ` +
		`echo "guard: a port was still held — taking this project down and bringing it back"; ` +
		`$dc down --remove-orphans; $dc up -d --remove-orphans; ` +
		`else rm -f "$log" "$status"; exit "$code"; fi; fi; `)
	out.WriteString(`rm -f "$log" "$status"; `)
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
