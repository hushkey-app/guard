package incidents

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Add = api.Define(api.Spec[api.None, model.HealthIncidentCreate, model.HealthIncident]{
	Name:  "Add Health Incident",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.HealthIncidentCreate]) (model.HealthIncident, error) {
		incident, err := store.Get().CreateHealthIncident(r.Body)
		if err != nil {
			return model.HealthIncident{}, api.BadRequest(err.Error())
		}
		return incident, nil
	},
})
