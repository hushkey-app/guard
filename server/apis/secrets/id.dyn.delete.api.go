package secrets

import (
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Delete removes an environment, its secrets and its keys.
//
// All three, which is why the page asks for the name to be typed — the same
// confirmation locking a machine takes. A key left pointing at a deleted
// environment would be a token nobody thinks to revoke, and a secret left
// behind would be a value nothing can reach and nothing can delete.
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
