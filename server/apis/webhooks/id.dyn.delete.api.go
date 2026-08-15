package webhooks

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Delete removes a destination and every rule that pointed at it.
//
// The rules go with it rather than being left aimed at nothing: a monitor with
// no destination evaluates, decides something is wrong, and says it to no one,
// which is worse than not having the rule — the page still lists it.
var Delete = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Delete Event Destination",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil {
			return api.None{}, api.BadRequest("that is not a destination id")
		}
		if err := store.Get().DeleteWebhook(id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return api.None{}, api.NotFound("no such destination")
			}
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
