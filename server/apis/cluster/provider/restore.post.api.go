package provider

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// RestoreRequest rolls one machine back to one image.
type RestoreRequest struct {
	NodeID     int64  `json:"node_id"`
	SnapshotID string `json:"snapshot_id"`
}

func (r RestoreRequest) Validate() error {
	if r.NodeID <= 0 {
		return api.Invalid("node_id", "must name a machine")
	}
	if strings.TrimSpace(r.SnapshotID) == "" {
		return api.Invalid("snapshot_id", "is required")
	}
	return nil
}

// Restore writes a snapshot back over the machine's disk.
//
// This is the most destructive thing guard can do to a machine, and it is
// deliberately the plainest: an id it was given, an instance it read from the
// link, and no cleverness in between. Everything on the box that is not in
// the image is gone, the instance reboots into the restored disk, and the
// only undo is another snapshot taken before this one.
//
// The dashboard asks for the machine's name to be typed before it calls this,
// the same confirmation locking and deleting take. A locked machine refuses
// outright, in the store.
var Restore = api.Define(api.Spec[api.None, RestoreRequest, api.None]{
	Name:  "Restore Machine Snapshot",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, RestoreRequest]) (api.None, error) {
		link, key, err := target(r.Body.NodeID, true)
		if err != nil {
			return api.None{}, err
		}
		err = cloud.Client.Restore(r.Context(), key, link.InstanceID, r.Body.SnapshotID)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Warn("machine restore",
			slog.Int64("node", link.NodeID), slog.String("instance", link.InstanceID),
			slog.String("snapshot", r.Body.SnapshotID), slog.String("result", outcome))
		if err != nil {
			return api.None{}, cloud.Fail(err)
		}
		return api.None{}, nil
	},
})
