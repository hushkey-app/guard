package envs

import (
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Delete removes one stage, its secrets and its keys.
//
// The keys go with it, because a token pointing at a deleted environment is a
// token nobody thinks to revoke.
var Delete = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Secret Environment",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil {
			return api.None{}, api.BadRequest("that is not an environment id")
		}
		if err := store.Get().DeleteEnv(id); err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
