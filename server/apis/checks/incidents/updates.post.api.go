package incidents

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var AddUpdate = api.Define(api.Spec[api.None, model.HealthIncidentUpdateCreate, model.HealthIncidentUpdate]{
	Name:  "Add Health Incident Update",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.HealthIncidentUpdateCreate]) (model.HealthIncidentUpdate, error) {
		update, err := store.Get().AddHealthIncidentUpdate(r.Body)
		if err != nil {
			return model.HealthIncidentUpdate{}, api.BadRequest(err.Error())
		}
		return update, nil
	},
})
