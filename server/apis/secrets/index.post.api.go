package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save adds an environment or renames one.
//
// A group is a name and nothing else — there is no project, no hierarchy and
// no inheritance between environments. An installation that wants one app's
// production kept apart from another's makes two groups and names them, which
// is the same thing without a schema that has to change when the shape of the
// organisation does.
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
