package provider

import (
	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SnapshotRow is one image in the account, and whether guard took it of this
// machine.
//
// Both halves are listed on purpose. Ours is the honest answer to "what can I
// roll this machine back to" — the provider's snapshot carries no instance, so
// only guard's own record can say — and the rest of the account is listed
// because a snapshot taken in the provider's console an hour before a bad
// deploy is exactly the one somebody will want, and hiding it would send them
// to another website to find it.
type SnapshotRow struct {
	Snapshot vultr.Snapshot `json:"snapshot"`
	Ours     bool           `json:"ours"`
}

// Snapshots lists what one machine could be restored from, newest first.
var Snapshots = api.Define(api.Spec[NodeQuery, api.None, []SnapshotRow]{
	Name: "Machine Snapshots",
	Handler: func(r *api.Request[NodeQuery, api.None]) ([]SnapshotRow, error) {
		link, key, err := target(r.Query.Node, false)
		if err != nil {
			return nil, err
		}
		snapshots, err := cloud.Client.Snapshots(r.Context(), key)
		if err != nil {
			return nil, cloud.Fail(err)
		}
		ours, err := store.Get().NodeSnapshots(link.NodeID)
		if err != nil {
			return nil, err
		}
		out := make([]SnapshotRow, 0, len(snapshots))
		for _, snapshot := range snapshots {
			out = append(out, SnapshotRow{Snapshot: snapshot, Ours: ours[snapshot.ID]})
		}
		return out, nil
	},
})
