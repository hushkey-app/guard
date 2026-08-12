package settings

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Read returns retention and capacity. Public: it is what the settings page
// renders before anyone has typed a token.
var Read = api.Define(api.Spec[api.None, api.None, model.Settings]{
	Name: "Settings",
	Handler: func(r *api.Request[api.None, api.None]) (model.Settings, error) {
		return store.Get().Settings()
	},
})
