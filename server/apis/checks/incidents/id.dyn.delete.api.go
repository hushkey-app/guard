package incidents

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Remove Manual Health Incident",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteHealthIncident(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("only manually added incidents can be removed")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
