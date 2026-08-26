package updates

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Remove Health Incident Update",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteHealthIncidentUpdate(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("incident update not found")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
