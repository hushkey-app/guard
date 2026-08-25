package checks

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Update = api.Define(api.Spec[api.None, model.HealthCheck, model.HealthCheck]{
	Name:  "Update Health Check",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.HealthCheck]) (model.HealthCheck, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.HealthCheck{}, api.Invalid("id", "must be a number")
		}
		check := r.Body
		check.ID = id
		saved, err := store.Get().SaveHealthCheck(check)
		if errors.Is(err, sql.ErrNoRows) {
			return model.HealthCheck{}, api.NotFound("health check not found")
		}
		if err != nil {
			return model.HealthCheck{}, api.BadRequest(err.Error())
		}
		wake()
		return saved, nil
	},
})
