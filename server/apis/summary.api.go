package apis

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Summary is the dashboard's header: counts per signal, the instances seen,
// and the most recent events.
var Summary = api.Define(api.Spec[api.None, api.None, model.Summary]{
	Name: "Summary",
	Handler: func(r *api.Request[api.None, api.None]) (model.Summary, error) {
		return store.Get().Snapshot()
	},
})
