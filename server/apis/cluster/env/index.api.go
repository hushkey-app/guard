package env

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Read is one machine's stored environment, values included.
//
// One machine at a time on purpose: the cluster list carries a count and two
// dates, because the dashboard reads that list every three seconds and a fleet's
// worth of passwords does not belong in it. Values come back in the clear here —
// the box is an editor, and an editor of masked fields is a form nobody can check
// against what their application actually needs.
var Read = api.Define(api.Spec[Query, api.None, Saved]{
	Name:  "Machine Environment",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Query, api.None]) (Saved, error) {
		if r.Query.NodeID <= 0 {
			return Saved{}, api.Invalid("node_id", "is required")
		}
		vars, err := store.Get().NodeEnv(r.Query.NodeID)
		if err != nil {
			return Saved{}, err
		}
		node, err := store.Get().Node(r.Query.NodeID)
		if err != nil {
			return Saved{}, api.NotFound("node not found")
		}
		state := node.Env
		state.Count = len(vars)
		return Saved{Vars: vars, Text: model.RenderEnvVars(vars), State: state}, nil
	},
})
