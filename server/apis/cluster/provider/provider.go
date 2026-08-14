// Package provider is what a machine's cloud account can say about it, and
// do to it.
//
// A node in guard is declared: a name, an address, a health path, a cadence.
// That is enough to watch anything, and it deliberately knows nothing about
// where the machine came from. Linking one to an instance adds the half a
// health check cannot see — whether the box is powered on at all, what it
// costs, what it can be rolled back to — without the watching depending on it.
//
// Three rules, the same three the stored commands keep:
//
//   - Every endpoint takes a node id and never an instance id. The instance
//     comes from the link, through Store.ProviderTargetFor, so a caller
//     cannot aim a power switch at a box that is not on the row.
//   - The lock is enforced in the store. ProviderTargetFor refuses the
//     changing calls for a locked machine; a handler cannot forget to ask,
//     because asking is how it learns which instance to talk to.
//   - Everything that changes anything is admin, and logged.
package provider

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// target resolves one node to its instance and the key that opens it. reading
// says whether this call only looks: a locked machine is still readable, and
// still refuses everything else.
func target(nodeID int64, destructive bool) (model.ProviderLink, string, error) {
	if nodeID <= 0 {
		return model.ProviderLink{}, "", api.Invalid("node", "must name a machine")
	}
	link, err := store.Get().ProviderTargetFor(nodeID, destructive)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProviderLink{}, "", api.NotFound("no such machine")
	}
	if err != nil {
		return model.ProviderLink{}, "", api.BadRequest(err.Error())
	}
	key, err := cloud.VultrKeyFor(link.AccountID)
	if err != nil {
		return model.ProviderLink{}, "", err
	}
	return link, key, nil
}
