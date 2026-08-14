package provider

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SnapshotRequest asks for an image of one machine.
type SnapshotRequest struct {
	NodeID      int64  `json:"node_id"`
	Description string `json:"description"`
}

func (s SnapshotRequest) Validate() error {
	if s.NodeID <= 0 {
		return api.Invalid("node_id", "must name a machine")
	}
	if len(s.Description) > 255 {
		return api.Invalid("description", "must be 255 characters or fewer")
	}
	return nil
}

// TakeSnapshot images one machine's disk.
//
// Allowed on a locked machine, unlike everything else here. A lock says this
// machine's dangerous half is finished being configured; taking a copy of it
// changes nothing about the machine and is the one thing that makes every
// other change survivable.
//
// The association is written here because the provider will not keep it: a
// Vultr snapshot has a description and no instance, so "the snapshots of this
// machine" is a question only guard's own record can answer. If recording it
// fails the snapshot still exists — it just shows up in the account's list
// rather than the machine's, which is worth an error nobody can act on.
var TakeSnapshot = api.Define(api.Spec[api.None, SnapshotRequest, vultr.Snapshot]{
	Name:  "Take Machine Snapshot",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, SnapshotRequest]) (vultr.Snapshot, error) {
		link, key, err := target(r.Body.NodeID, false)
		if err != nil {
			return vultr.Snapshot{}, err
		}
		description := strings.TrimSpace(r.Body.Description)
		if description == "" {
			description = "guard"
		}
		snapshot, err := cloud.Client.CreateSnapshot(r.Context(), key, link.InstanceID, description)
		if err != nil {
			slog.Info("machine snapshot",
				slog.Int64("node", link.NodeID), slog.String("instance", link.InstanceID),
				slog.String("result", err.Error()))
			return vultr.Snapshot{}, cloud.Fail(err)
		}
		slog.Info("machine snapshot",
			slog.Int64("node", link.NodeID), slog.String("instance", link.InstanceID),
			slog.String("snapshot", snapshot.ID), slog.String("result", "ok"))
		if err := store.Get().RecordSnapshot(link.NodeID, snapshot.ID, description); err != nil {
			return snapshot, nil //nolint:nilerr
		}
		return snapshot, nil
	},
})
