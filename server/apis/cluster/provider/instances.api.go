package provider

import (
	"errors"

	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// AccountQuery names one stored account.
type AccountQuery struct {
	Account int64 `query:"account"`
}

func (q AccountQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored cloud account")
	}
	return nil
}

// InstanceRow is one instance in the account, with the machine already
// watching it if there is one.
//
// The pairing is the whole point of this listing. It is read twice: by the
// picker that links an existing machine, where an instance already taken is
// one to warn about, and by the import list, where it is one to grey out.
// Two rows watching one box is two health checks, two command lists and an
// argument about which is the real one.
type InstanceRow struct {
	Instance vultr.Instance `json:"instance"`
	NodeID   int64          `json:"node_id,omitempty"`
	NodeName string         `json:"node_name,omitempty"`
}

// Instances lists what one account runs, read live.
var Instances = api.Define(api.Spec[AccountQuery, api.None, []InstanceRow]{
	Name:  "Cloud Instances",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[AccountQuery, api.None]) ([]InstanceRow, error) {
		key, err := cloud.VultrKeyFor(r.Query.Account)
		if err != nil {
			return nil, err
		}
		instances, err := cloud.Client.Instances(r.Context(), key)
		if err != nil {
			return nil, cloud.Fail(err)
		}
		out := make([]InstanceRow, 0, len(instances))
		for _, instance := range instances {
			row := InstanceRow{Instance: instance}
			nodeID, name, err := store.Get().NodeForInstance(r.Query.Account, instance.ID)
			if err != nil {
				return nil, err
			}
			row.NodeID, row.NodeName = nodeID, name
			out = append(out, row)
		}
		return out, nil
	},
})
