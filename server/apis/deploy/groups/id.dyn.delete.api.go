package groups

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Remove deletes a group. The runs it started stay: they carry its name rather
// than a pointer to it, so the history reads the same afterwards.
var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Deploy Group",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteDeployGroup(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("no group with that id")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
