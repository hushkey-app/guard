package deploy

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// State is what one machine is running, per service.
//
// The pair that matters is current and last known good, and they only move on a
// passed health check — so a machine that failed its last deploy still says the
// tag that actually answered, which is both the true thing and the one rollback
// needs.
var State = api.Define(api.Spec[StateQuery, api.None, []model.DeployState]{
	Name:  "Machine Deploy State",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[StateQuery, api.None]) ([]model.DeployState, error) {
		return store.Get().DeployStates(r.Query.NodeID)
	},
})
