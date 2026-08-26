package incidents

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Save = api.Define(api.Spec[api.None, model.HealthIncidentReport, api.None]{
	Name:  "Save Health Incident Report",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.HealthIncidentReport]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().SaveHealthIncident(id, r.Body); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("completed incident not found")
		} else if err != nil {
			return api.None{}, api.BadRequest(err.Error())
		}
		return api.None{}, nil
	},
})
