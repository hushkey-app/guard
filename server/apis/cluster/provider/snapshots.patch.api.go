package provider

import (
	"context"
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SnapshotUpdate names and claims an existing account snapshot for a machine.
type SnapshotUpdate struct {
	NodeID     int64  `json:"node_id"`
	SnapshotID string `json:"snapshot_id"`
	Name       string `json:"name"`
}

func (u SnapshotUpdate) Validate() error {
	if u.NodeID <= 0 {
		return api.Invalid("node_id", "must name a machine")
	}
	if strings.TrimSpace(u.SnapshotID) == "" {
		return api.Invalid("snapshot_id", "is required")
	}
	if strings.TrimSpace(u.Name) == "" {
		return api.Invalid("name", "is required")
	}
	if len(u.Name) > 255 {
		return api.Invalid("name", "must be 255 characters or fewer")
	}
	return nil
}

// UpdateSnapshot retroactively links a snapshot and gives it a durable Guard
// name. Vultr supports updating the description, so we mirror the name there;
// the Guard record remains authoritative if that update is unavailable to the
// account's API token.
var UpdateSnapshot = api.Define(api.Spec[api.None, SnapshotUpdate, api.None]{
	Name:  "Name Machine Snapshot",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, SnapshotUpdate]) (api.None, error) {
		link, key, err := target(r.Body.NodeID, false)
		if err != nil {
			return api.None{}, err
		}
		name := strings.TrimSpace(r.Body.Name)
		if err := store.Get().RecordSnapshot(link.NodeID, r.Body.SnapshotID, name); err != nil {
			return api.None{}, err
		}
		if err := syncSnapshotName(r.Context(), link.Provider, key, r.Body.SnapshotID, name); err != nil {
			slog.Info("machine snapshot provider rename",
				slog.Int64("node", link.NodeID), slog.String("snapshot", r.Body.SnapshotID),
				slog.String("result", err.Error()))
		}
		return api.None{}, nil
	},
})

// syncSnapshotName is deliberately optional. Guard's record is canonical;
// adapters only mirror it into providers that expose a native editable label.
func syncSnapshotName(ctx context.Context, provider, key, snapshotID, name string) error {
	switch provider {
	case model.ProviderVultr:
		return cloud.Client.UpdateSnapshot(ctx, key, snapshotID, name)
	default:
		return nil
	}
}
