package checks

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var List = api.Define(api.Spec[api.None, api.None, []model.HealthCheck]{
	Name: "Health Checks",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.HealthCheck, error) {
		return store.Get().HealthChecks()
	},
})
