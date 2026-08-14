package provider

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// ImportRequest declares a machine for an instance the account already runs.
type ImportRequest struct {
	AccountID  int64  `json:"account_id"`
	InstanceID string `json:"instance_id"`
}

func (i ImportRequest) Validate() error {
	if i.AccountID <= 0 {
		return api.Invalid("account_id", "must name a stored cloud account")
	}
	if strings.TrimSpace(i.InstanceID) == "" {
		return api.Invalid("instance_id", "is required")
	}
	return nil
}

// Import turns one instance into a watched machine: the label becomes the
// name, the public address becomes the address, the provider's tags become
// guard's, and the link is set as it is created.
//
// It arrives **paused**, and that is the honest default rather than caution
// for its own sake. Guard has no idea what this machine serves or where its
// health endpoint is — the address it can guess is `http://<ip>`, which for
// most boxes answers nothing at all. Enabling it before somebody types the
// health path would produce a machine that is red from the moment it appears,
// which teaches people that red means nothing.
//
// No login is imported either. There is nothing to import: the provider does
// not hand out passwords, and a machine here with an unproved login would
// break the rule that every stored login connected at least once.
var Import = api.Define(api.Spec[api.None, ImportRequest, model.Node]{
	Name:  "Import Cloud Instance",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, ImportRequest]) (model.Node, error) {
		key, err := cloud.VultrKeyFor(r.Body.AccountID)
		if err != nil {
			return model.Node{}, err
		}
		instance, err := cloud.Client.Instance(r.Context(), key, r.Body.InstanceID)
		if err != nil {
			return model.Node{}, cloud.Fail(err)
		}
		taken, name, err := store.Get().NodeForInstance(r.Body.AccountID, instance.ID)
		if err != nil {
			return model.Node{}, err
		}
		if taken != 0 {
			return model.Node{}, api.BadRequest("that instance is already watched by " + name)
		}
		address := instance.MainIP
		if address == "" {
			address = instance.InternalIP
		}
		if address == "" {
			return model.Node{}, api.BadRequest("that instance has no address yet — it may still be installing")
		}
		node := model.Node{
			Name: firstOf(instance.Label, instance.Hostname, instance.MainIP),
			// http, because that is what a box answers on before somebody puts
			// a name and a certificate in front of it. The address is a field
			// on a form; it is meant to be corrected.
			Domain:             "http://" + address,
			Enabled:            false,
			IntervalSeconds:    model.DefaultIntervalSeconds,
			Tags:               importedTags(instance.Tags),
			Provider:           model.ProviderVultr,
			ProviderAccountID:  r.Body.AccountID,
			ProviderInstanceID: instance.ID,
		}
		saved, err := store.Get().SaveNode(node)
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		slog.Info("cloud instance imported",
			slog.Int64("node", saved.ID), slog.String("name", saved.Name),
			slog.Int64("account", r.Body.AccountID), slog.String("instance", instance.ID))
		return saved, nil
	},
})

// firstOf is the first of these that somebody could read on a card.
func firstOf(candidates ...string) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return "instance"
}

// importedTags carries the provider's tags across, in one colour. Guard does
// not know what any of them mean, so choosing hues for them would be inventing
// a meaning; the person who imports the machine can recolour what matters.
func importedTags(tags []string) []model.NodeTag {
	out := make([]model.NodeTag, 0, len(tags))
	for _, label := range tags {
		label = strings.TrimSpace(label)
		if label == "" || len(label) > 24 || len(out) >= model.MaxTagsPerNode {
			continue
		}
		out = append(out, model.NodeTag{Label: label, Colour: "slate"})
	}
	return out
}
