package env

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save stores one machine's environment. It does not touch the machine.
//
// Two presses rather than one, and this is the harmless half: what is saved is
// guard's copy of somebody's intent, and a locked machine may still be edited
// here because nothing has been written to it. The inject is what reaches out, and
// that is what the lock refuses.
//
// A paste is parsed here rather than in the browser — same dialect as the vault's
// .env import, including a double-quoted value running over several lines — and the
// lines that could not be read come back named with their line numbers rather than
// counted.
var Save = api.Define(api.Spec[api.None, Vars, Saved]{
	Name:  "Save Machine Environment",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Vars]) (Saved, error) {
		vars, skipped := r.Body.Vars, []model.ImportSkip{}
		if r.Body.Text != "" {
			vars, skipped = model.ParseEnvVars(r.Body.Text)
		}
		saved, err := store.Get().SaveNodeEnv(r.Body.NodeID, vars)
		if errors.Is(err, sql.ErrNoRows) {
			return Saved{}, api.NotFound("node not found")
		}
		if err != nil {
			return Saved{}, api.BadRequest(err.Error())
		}
		node, err := store.Get().Node(r.Body.NodeID)
		if err != nil {
			return Saved{}, err
		}
		state := node.Env
		state.Count = len(saved)
		return Saved{Vars: saved, Text: model.RenderEnvVars(saved), Skipped: skipped, State: state}, nil
	},
})
