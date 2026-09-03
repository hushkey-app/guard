package incidents

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

type IncidentQuery struct {
	CheckID int64 `query:"check_id"`
}

var List = api.Define(api.Spec[IncidentQuery, api.None, model.HealthIncidentBoard]{
	Name:  "Completed Health Incidents",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[IncidentQuery, api.None]) (model.HealthIncidentBoard, error) {
		if r.Query.CheckID <= 0 {
			return model.HealthIncidentBoard{}, api.Invalid("check_id", "is required")
		}
		return store.Get().HealthIncidentBoard(r.Query.CheckID)
	},
})
