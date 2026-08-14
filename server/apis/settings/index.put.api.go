package settings

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Update changes retention and capacity, then applies them immediately —
// lowering either one is expected to free disk now, not at the next sweep.
var Update = api.Define(api.Spec[api.None, model.Settings, model.Settings]{
	Name:  "Update Settings",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Settings]) (model.Settings, error) {
		if err := store.Get().UpdateSettings(r.Body); err != nil {
			return model.Settings{}, api.BadRequest(err.Error())
		}
		return store.Get().Settings()
	},
})
