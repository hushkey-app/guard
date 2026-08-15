package cluster

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/prober"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// CheckNow probes every node immediately, for the question the interval cannot
// answer: is it back yet.
//
// Admin, because it is the one read-shaped endpoint that makes guard send
// traffic somewhere else — an open one would let any visitor use this instance
// as a request amplifier against the machines it watches.
//
// It answers with the nodes, freshly checked, so the page can render the result
// rather than poll for it.
var CheckNow = api.Define(api.Spec[api.None, api.None, []model.Node]{
	Name:  "Check Cluster Now",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Node, error) {
		p := prober.Get()
		if p == nil {
			return nil, api.Unavailable("this instance is running without a cluster prober")
		}
		// Everything, not just what is due: the question this button asks is
		// "is it back yet", and a node checked two seconds ago is exactly the
		// one being asked about.
		p.RoundAll(r.Context())
		return store.Get().Nodes()
	},
})
