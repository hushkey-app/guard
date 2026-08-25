package provider

import (
	"sort"
	"strings"

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
	Name     string         `json:"name"`
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
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
		out := make([]SnapshotRow, 0, len(snapshots)+len(ours))
		seen := make(map[string]bool, len(snapshots))
		for _, snapshot := range snapshots {
			seen[snapshot.ID] = true
			record, claimed := ours[snapshot.ID]
			name := snapshot.Description
			if claimed && record.Description != "" {
				name = record.Description
			}
			status := snapshotStatus(snapshot.Status)
			if claimed && status != "" {
				_ = store.Get().SetSnapshotStatus(link.NodeID, snapshot.ID, status, "")
			}
			if status == "" && claimed {
				status = record.Status
			}
			out = append(out, SnapshotRow{Snapshot: snapshot, Ours: claimed, Name: name, Status: status, Error: record.LastError})
		}
		// Provider list APIs can be eventually consistent. A snapshot Guard has
		// just created or previously linked remains visible from its durable row
		// even while the provider temporarily omits it.
		for id, record := range ours {
			if seen[id] {
				continue
			}
			out = append(out, SnapshotRow{
				Snapshot: vultr.Snapshot{
					ID: id, Description: record.Description, Created: record.Created, Status: record.Status,
				},
				Ours: true, Name: record.Description, Status: record.Status, Error: record.LastError,
			})
		}
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Snapshot.Created.After(out[j].Snapshot.Created)
		})
		return out, nil
	},
})

func snapshotStatus(providerStatus string) string {
	switch strings.ToLower(strings.TrimSpace(providerStatus)) {
	case "complete", "completed", "ready", "available":
		return "complete"
	case "failed", "failure", "error":
		return "failed"
	case "pending", "creating", "in-progress", "in_progress", "processing":
		return "pending"
	default:
		return ""
	}
}
