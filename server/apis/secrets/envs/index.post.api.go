package envs

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save adds a stage to a workspace or renames one.
var Save = api.Define(api.Spec[api.None, model.Env, model.Env]{
	Name:  "Save Secret Environment",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Env]) (model.Env, error) {
		saved, err := store.Get().SaveEnv(r.Body)
		if err != nil {
			return model.Env{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
