package provider

import (
	"errors"
	"log/slog"

	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SnapshotQuery addresses one image of one machine.
type SnapshotQuery struct {
	Node     int64  `query:"node"`
	Snapshot string `query:"snapshot"`
}

func (q SnapshotQuery) Validate() error {
	if q.Node <= 0 {
		return errors.New("node must name a machine")
	}
	if q.Snapshot == "" {
		return errors.New("snapshot is required")
	}
	return nil
}

// DeleteSnapshot forgets one image, at the provider and here.
//
// Counted as a change to the machine rather than a read, so a locked machine
// refuses it: the images are what makes everything else survivable, and a
// lock that protected the machine while its last rollback point was deleted
// would be protecting the wrong thing.
var DeleteSnapshot = api.Define(api.Spec[SnapshotQuery, api.None, api.None]{
	Name:  "Delete Machine Snapshot",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[SnapshotQuery, api.None]) (api.None, error) {
		link, key, err := target(r.Query.Node, true)
		if err != nil {
			return api.None{}, err
		}
		err = cloud.Client.DeleteSnapshot(r.Context(), key, r.Query.Snapshot)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Info("machine snapshot delete",
			slog.Int64("node", link.NodeID), slog.String("snapshot", r.Query.Snapshot),
			slog.String("result", outcome))
		if err != nil {
			return api.None{}, cloud.Fail(err)
		}
		if err := store.Get().ForgetSnapshot(link.NodeID, r.Query.Snapshot); err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
