package cluster

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/scheduler"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Actions saves the commands kept for one machine.
//
// The whole list at once, because that is how the page edits them: a name
// changed, a command fixed, one removed and one added is a single save, and
// three endpoints doing it separately would leave the order to be argued over
// by whichever request landed last.
//
// Storing a command is the privileged act here, not running it — anything in
// this list can be run by anyone who can press the button next to it.
var Actions = api.Define(api.Spec[api.None, contract.ActionList, []model.NodeAction]{
	Name:  "Save Cluster Actions",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.ActionList]) ([]model.NodeAction, error) {
		saved, err := store.Get().SaveActions(r.Body.NodeID, r.Body.Actions)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, api.NotFound("node not found")
		}
		if err != nil {
			return nil, api.BadRequest(err.Error())
		}
		// A schedule somebody just typed is picked up on the next pass rather
		// than at the end of the current sleep, so the next-run line on the
		// page is true while they are still looking at it.
		if loop := scheduler.Get(); loop != nil {
			loop.Wake()
		}
		return saved, nil
	},
})
