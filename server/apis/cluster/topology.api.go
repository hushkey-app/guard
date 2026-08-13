package cluster

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Topology answers "what runs where": the instances grouped under the machine
// their telemetry says served them, and the ones nothing could place.
//
// The join is on hosts found in the telemetry, never on names. It is therefore
// incomplete by design — a background worker with no HTTP surface has no host
// to match — and the unplaced instances are returned rather than hidden.
var Topology = api.Define(api.Spec[api.None, api.None, model.ClusterTopology]{
	Name: "Cluster Topology",
	Handler: func(r *api.Request[api.None, api.None]) (model.ClusterTopology, error) {
		return store.Get().ClusterTopology()
	},
})
