package analytics

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Remove deletes an action and everything counted under it.
//
// The path segment is `{id}` by the filename convention and an action's id is
// its name — there is no other handle, because a name is what the tracker sends
// and what the column is called.
//
// It answers 404 for a name that is not there rather than 204: the dialog above
// this says the rollup rows go with it, and a silent success would be guard
// agreeing to something it did not do. `page_view` is never in the table, so the
// one name that must survive lands there too.
var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Analytics Action",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		if err := store.Get().DeleteAnalyticsAction(r.Param("id")); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("action not found")
		} else if err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
