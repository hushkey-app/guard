package env

import (
	"database/sql"
	"errors"
	"log/slog"

	"github.com/hushkey-app/guard/internal/envfile"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/runner"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Injected is what one press did.
type Injected struct {
	Count int      `json:"count"`
	Files []string `json:"files"`
	// Output is what the machine printed, so the page reports the box's own
	// account of it rather than guard's assumption.
	Output string             `json:"output,omitempty"`
	State  model.NodeEnvState `json:"state"`
}

// Inject puts the machine's stored environment on the machine.
//
// It takes a node id and nothing else. The variables come from the database, the
// paths are fixed in internal/envfile, and the login comes off the machine — so
// there is no request anybody can shape into "write this content to that path".
//
// What lands: /etc/environment for logins, and a systemd drop-in so every service
// started after it inherits the same set. `systemctl daemon-reexec` follows.
// Services already running keep the environment they started with — restarting one
// is one of the machine's stored commands, and the answer says so rather than
// implying the change is live everywhere.
var Inject = api.Define(api.Spec[api.None, Target, Injected]{
	Name:  "Inject Machine Environment",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Target]) (Injected, error) {
		target, err := store.Get().EnvTargetFor(r.Body.NodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return Injected{}, api.NotFound("node not found")
		}
		if err != nil {
			return Injected{}, api.BadRequest(err.Error())
		}
		command, err := envfile.InjectCommand(target.Vars)
		if err != nil {
			return Injected{}, api.BadRequest(err.Error())
		}
		engine := runner.Get()
		if engine == nil {
			return Injected{}, api.Unavailable("this instance is running without an ssh runner")
		}
		login := remote.Login{
			User:        target.Login.User,
			Address:     target.Login.Address,
			Password:    target.Login.Password,
			Fingerprint: target.Login.Fingerprint,
		}
		result, runErr := engine.Run(r.Context(), login, command)
		if result.Fingerprint != "" && target.Login.Fingerprint == "" {
			if err := store.Get().PinFingerprint(target.NodeID, result.Fingerprint); err != nil {
				slog.Error("ssh host key not stored", slog.Int64("node", target.NodeID), slog.Any("err", err))
			}
		}
		// The one line that outlives the browser tab: which machine, how many
		// variables, and how it ended. Never the values.
		slog.Info("injected a machine environment",
			slog.Int64("node", target.NodeID), slog.String("machine", target.Name),
			slog.Int("vars", len(target.Vars)), slog.String("user", login.User),
			slog.String("address", login.Address), slog.Int("exit", result.ExitCode))
		if runErr != nil {
			return Injected{}, api.BadRequest(runErr.Error())
		}
		if result.ExitCode != 0 {
			return Injected{}, api.BadRequest(trim(result.Output))
		}
		answer := Injected{
			Count:  len(target.Vars),
			Files:  []string{envfile.EnvironmentPath, envfile.SystemdPath},
			Output: trim(result.Output),
		}
		if err := store.Get().EnvInjected(target.NodeID); err == nil {
			if node, err := store.Get().Node(target.NodeID); err == nil {
				answer.State = node.Env
				answer.State.Count = len(target.Vars)
			}
		}
		return answer, nil
	},
})

// trim is what the machine said, short enough to be a sentence on a page.
func trim(output string) string {
	if len(output) > 400 {
		return output[:400] + "…"
	}
	return output
}
