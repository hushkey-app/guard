package checks

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/prober"
	"github.com/mirairoad/howl-go/core/api"
)

var Run = api.Define(api.Spec[api.None, contract.HealthCheckRequest, model.Check]{
	Name:  "Run Health Check",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.HealthCheckRequest]) (model.Check, error) {
		p := prober.Get()
		if p == nil {
			return model.Check{}, api.Unavailable("this instance is running without a health prober")
		}
		return p.CheckNow(r.Context(), r.Body.CheckID)
	},
})
