package cluster

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Duplicate copies a machine's configuration onto a new one.
//
// For the ordinary case of a fleet: five boxes that differ by an address and
// otherwise want the same health path, the same cadence and the same four
// commands. Typing that out five times is how the fifth one ends up with a
// slightly different reboot command than the others.
//
// The copy arrives paused, with no login, and neither locked nor sealed — see
// the store for why each of those is deliberate.
var Duplicate = api.Define(api.Spec[api.None, contract.NodeRequest, model.Node]{
	Name:  "Duplicate Cluster Node",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.NodeRequest]) (model.Node, error) {
		copied, err := store.Get().DuplicateNode(r.Body.NodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("node not found")
		}
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		wake()
		return copied, nil
	},
})
