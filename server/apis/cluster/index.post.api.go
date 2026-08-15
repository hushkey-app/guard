package cluster

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Add registers a machine to watch.
//
// Admin, and not only because it writes: this is the endpoint that decides
// which URLs guard will fetch, on a timer, from inside whatever network it runs
// in. Anyone who can add a node can point the prober at anything that guard can
// reach.
//
// A login, if one was given, has to work before the machine is stored. A
// machine saved with a mistyped password looks exactly like one saved
// correctly, and the difference turns up later, on the worst night. Leave the
// SSH fields empty and none of that happens: plenty of machines are watched
// through a load balancer and never logged in to.
var Add = api.Define(api.Spec[api.None, model.Node, model.Node]{
	Name:  "Add Cluster Node",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Node]) (model.Node, error) {
		node := r.Body
		node.ID = 0
		// A node added through the form is meant to be watched; the field
		// exists to pause one later, not to create one asleep.
		node.Enabled = true
		// Validated before the connection is attempted, so a typo in the URL is
		// answered immediately instead of after an SSH timeout.
		if err := node.Validate(); err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		password := ""
		if node.Password != nil {
			password = *node.Password
		}
		fingerprint, err := verifySSH(r.Context(), node, password)
		if err != nil {
			return model.Node{}, err
		}
		saved, err := store.Get().SaveNode(node)
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		// The key from the connection just made. Trust on first use, with the
		// first use being the one that let this machine be added at all.
		if fingerprint != "" {
			if err := store.Get().PinFingerprint(saved.ID, fingerprint); err != nil {
				return model.Node{}, err
			}
			saved.SSHFingerprint = fingerprint
		}
		// Check it now rather than at the end of whatever sleep the scheduler
		// happens to be in. A new machine sitting at "unknown" for half a
		// minute is the first thing anyone reads as broken.
		wake()
		return saved, nil
	},
})
