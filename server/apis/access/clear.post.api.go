package access

import (
	"github.com/hushkey-app/guard/internal/access"
	"github.com/mirairoad/howl-go/core/api"
)

// Clear takes one credential back out of guard's env file, so the next start
// uses whatever else sets it — a line in guard.env, or nothing.
//
// Clearing GUARD_TOKEN on an instance where nobody signs in reopens every
// write endpoint, so it is the same admin press as generating one and the
// dashboard asks before sending it.
var Clear = api.Define(api.Spec[api.None, Request, access.State]{
	Name:  "Clear Credential",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Request]) (access.State, error) {
		state, err := Get().Clear(r.Body.Name)
		if err != nil {
			return state, api.BadRequest(err.Error())
		}
		return state, nil
	},
})
