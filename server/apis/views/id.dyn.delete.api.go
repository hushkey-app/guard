package views

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Remove deletes a view. Answers 204, and 404 for a view that was already gone
// rather than pretending to have deleted something.
var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete View",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteView(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("view not found")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
