package checks

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Add = api.Define(api.Spec[api.None, model.HealthCheck, model.HealthCheck]{
	Name:  "Add Health Check",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.HealthCheck]) (model.HealthCheck, error) {
		check := r.Body
		check.ID = 0
		check.Enabled = true
		saved, err := store.Get().SaveHealthCheck(check)
		if err != nil {
			return model.HealthCheck{}, api.BadRequest(err.Error())
		}
		wake()
		return saved, nil
	},
})
