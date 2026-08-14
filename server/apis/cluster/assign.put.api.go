package cluster

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Assign states where an instance runs, when the telemetry cannot say.
//
// Host matching covers the ordinary case and cannot cover all of them. A
// browser runs on nobody's machine. A service behind a load balancer reports
// the balancer's host — and adding that balancer as a machine to watch, purely
// so the grouping comes out right, would put something on the dashboard that
// nobody actually wants to watch.
//
// So the answer can be typed instead, and it outranks every guess. Node zero
// releases the instance back to whatever its telemetry implies.
var Assign = api.Define(api.Spec[api.None, contract.Assignment, model.ClusterTopology]{
	Name:  "Assign Instance",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.Assignment]) (model.ClusterTopology, error) {
		if err := store.Get().AssignInstance(r.Body.Service, r.Body.Instance, r.Body.NodeID); err != nil {
			return model.ClusterTopology{}, api.BadRequest(err.Error())
		}
		// The new arrangement, so the page settles on what was stored rather
		// than on its own guess at the consequence.
		return store.Get().ClusterTopology()
	},
})
