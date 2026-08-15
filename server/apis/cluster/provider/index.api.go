package provider

import (
	"errors"

	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// NodeQuery names one machine. Every call in this package starts here.
type NodeQuery struct {
	Node int64 `query:"node"`
}

func (q NodeQuery) Validate() error {
	if q.Node <= 0 {
		return errors.New("node must name a machine")
	}
	return nil
}

// Facts is what the provider says about one machine right now.
//
// Read on open and on explicit refresh, never on the dashboard's three-second
// tick: behind it is somebody's API rate limit, and a power state that is
// twenty seconds old has never been the thing that made an outage worse.
type Facts struct {
	Node     int64          `json:"node"`
	Account  int64          `json:"account"`
	Instance vultr.Instance `json:"instance"`
	// Bandwidth is best effort. It is a second request, and a machine whose
	// transfer cannot be read is still a machine worth showing the state of.
	Bandwidth vultr.Bandwidth `json:"bandwidth"`
	Transfer  string          `json:"transfer_error,omitempty"`
}

// Instance reads one machine's instance from the provider.
var Instance = api.Define(api.Spec[NodeQuery, api.None, Facts]{
	Name: "Machine Instance",
	Handler: func(r *api.Request[NodeQuery, api.None]) (Facts, error) {
		link, key, err := target(r.Query.Node, false)
		if err != nil {
			return Facts{}, err
		}
		instance, err := cloud.Client.Instance(r.Context(), key, link.InstanceID)
		if err != nil {
			return Facts{}, cloud.Fail(err)
		}
		facts := Facts{Node: link.NodeID, Account: link.AccountID, Instance: instance}
		bandwidth, err := cloud.Client.BandwidthFor(r.Context(), key, link.InstanceID)
		if err != nil {
			facts.Transfer = err.Error()
		} else {
			facts.Bandwidth = bandwidth
		}
		return facts, nil
	},
})
