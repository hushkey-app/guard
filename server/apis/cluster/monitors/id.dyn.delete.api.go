package monitors

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var Delete = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Cluster Monitor",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil {
			return api.None{}, api.BadRequest("that is not a rule id")
		}
		if err := store.Get().DeleteMonitor(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return api.None{}, api.NotFound("no such rule")
			}
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
