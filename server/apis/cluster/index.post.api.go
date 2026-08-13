package cluster

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Add registers a machine to watch.
//
// Admin, and not only because it writes: this is the endpoint that decides
// which URLs guard will fetch, on a timer, from inside whatever network it runs
// in. Anyone who can add a node can point the prober at anything that guard can
// reach.
var Add = api.Define(api.Spec[api.None, model.Node, model.Node]{
	Name:  "Add Cluster Node",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Node]) (model.Node, error) {
		node := r.Body
		node.ID = 0
		// A node added through the form is meant to be watched; the field
		// exists to pause one later, not to create one asleep.
		node.Enabled = true
		saved, err := store.Get().SaveNode(node)
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		// Check it now rather than at the end of whatever sleep the scheduler
		// happens to be in. A new machine sitting at "unknown" for half a
		// minute is the first thing anyone reads as broken.
		wake()
		return saved, nil
	},
})
