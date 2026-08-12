package apis

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Facets fills the filter dropdowns: which services, severities and metric
// names actually exist right now.
var Facets = api.Define(api.Spec[api.None, api.None, model.Facets]{
	Name: "Facets",
	Handler: func(r *api.Request[api.None, api.None]) (model.Facets, error) {
		return store.Get().Facets()
	},
})
