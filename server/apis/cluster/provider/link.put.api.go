package provider

import (
	"database/sql"
	"errors"
	"log/slog"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Link points one machine at one instance, or — with an empty instance —
// forgets the pointing.
//
// The instance is checked against the provider before the link is stored, for
// the same reason an SSH login is proved before it is saved: a link to an id
// that does not exist looks exactly like a working one until somebody presses
// a power button at three in the morning.
//
// It is its own endpoint rather than three fields on the machine's save
// because a save is what the settings form does on every rename, and a link
// that a form could drop by not knowing about it is a link nobody can trust.
var Link = api.Define(api.Spec[api.None, model.ProviderLink, model.Node]{
	Name:  "Link Machine To Cloud",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.ProviderLink]) (model.Node, error) {
		link := r.Body
		if link.NodeID <= 0 {
			return model.Node{}, api.Invalid("node_id", "must name a machine")
		}
		if link.InstanceID != "" {
			if err := link.Validate(); err != nil {
				return model.Node{}, api.BadRequest(err.Error())
			}
			key, err := cloud.VultrKeyFor(link.AccountID)
			if err != nil {
				return model.Node{}, err
			}
			if _, err := cloud.Client.Instance(r.Context(), key, link.InstanceID); err != nil {
				return model.Node{}, cloud.Fail(err)
			}
			taken, name, err := store.Get().NodeForInstance(link.AccountID, link.InstanceID)
			if err != nil {
				return model.Node{}, err
			}
			if taken != 0 && taken != link.NodeID {
				return model.Node{}, api.BadRequest("that instance is already watched by " + name)
			}
		}
		node, err := store.Get().LinkNode(link.NodeID, link)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("no such machine")
		}
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		slog.Info("machine cloud link",
			slog.Int64("node", node.ID), slog.String("name", node.Name),
			slog.Int64("account", link.AccountID), slog.String("instance", link.InstanceID))
		return node, nil
	},
})
