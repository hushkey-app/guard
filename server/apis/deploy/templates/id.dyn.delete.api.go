package templates

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Remove deletes every version of a template. The runs stay, with the name and
// the version number they recorded — a deleted template must not make a past
// deploy unreadable.
var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Compose Template",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteDeployTemplate(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("no template with that id")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
