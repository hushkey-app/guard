// Package groups is the sets of machines a deploy goes to.
//
// A group is a saved selection over the cluster and nothing more: it holds node
// ids, and the machines themselves are read through a join. So a machine
// deleted from the cluster leaves every group by itself, and there is no second
// inventory to keep in step with the first.
package groups

import "github.com/mirairoad/howl-go/core/api"

// Group is what a save sends: the name, the membership, and where this group's
// deploys speak up when one stops.
type Group struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	// NodeIDs is the whole membership, replaced on every save — the way the
	// machine's environment and the command list are edited, because it is one
	// thing somebody changes.
	NodeIDs []int64 `json:"node_ids"`
	// WebhookID is the destination a stopped run tells. Zero is allowed and
	// means a sequential deploy that stops will wait in silence, which the page
	// says beside the field rather than leaving to be discovered.
	WebhookID int64 `json:"webhook_id,omitempty"`
}

func (g Group) Validate() error {
	if g.Name == "" {
		return api.Invalid("name", "give the group a name")
	}
	return nil
}
