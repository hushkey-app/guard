package secrets

import (
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Delete removes a workspace, its environments, their secrets and every key
// that read them.
//
// The whole tree, which is why the page asks for the name to be typed — the
// same confirmation locking a machine takes. Anything left pointing at a
// deleted parent is a row nothing can reach and nobody thinks to revoke.
var Delete = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Secret Workspace",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil {
			return api.None{}, api.BadRequest("that is not a workspace id")
		}
		if err := store.Get().DeleteWorkspace(id); err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
