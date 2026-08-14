package cluster

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Run executes one stored action on the machine it belongs to.
//
// This is the endpoint that runs somebody's command on somebody's server, so
// three things are true of it on purpose. It takes an action id and never a
// command, which means every line that runs was stored first. The machine is
// read from the action rather than from the request, so a caller cannot aim a
// stored command at a different box. And it is admin, like everything else that
// changes state — the same token that can add a machine can run things on it,
// because anyone who can add a machine could add one and run things on that.
//
// A command that exited non-zero is a 200 carrying the exit code. Failing the
// request would hide the output, which is the only part anybody wants.
var Run = api.Define(api.Spec[api.None, contract.RunRequest, model.Run]{
	Name:  "Run Cluster Action",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.RunRequest]) (model.Run, error) {
		action, err := store.Get().Action(r.Body.ActionID)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Run{}, api.NotFound("that action no longer exists")
		}
		if err != nil {
			return model.Run{}, err
		}
		run, err := execute(r.Context(), action.NodeID, action.Command)
		if err != nil {
			return model.Run{}, err
		}
		run.ActionID = action.ID
		run.NodeID = action.NodeID
		// Who asked, kept with the run: "it has been working" and "somebody has
		// been running it by hand every morning" are different states of the
		// same job, and the history is where the difference shows.
		run.Trigger = model.TriggerManual
		// Remembered so the button can say how it went last time. A failure to
		// record is not a failure to run — the command already happened.
		if err := store.Get().RecordRun(action.ID, run); err != nil {
			return run, nil //nolint:nilerr
		}
		return run, nil
	},
})
