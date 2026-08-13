// Package cluster is the endpoint layer for the machines guard watches from
// the outside.
//
// An instance is derived from telemetry and disappears when the telemetry
// does — which is the moment you most want to hear about it. A node is
// declared, so guard can say "VPS-1 has been down for six minutes" about a
// machine that has stopped talking to anyone.
package cluster

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns every node with its latest check, a day of uptime and enough
// recent history to draw a strip. One request for the whole cluster: the list
// is small by nature, and a request per node would make the settings page
// slower the more machines you watch.
var List = api.Define(api.Spec[api.None, api.None, []model.Node]{
	Name: "Cluster",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Node, error) {
		return store.Get().Nodes()
	},
})
