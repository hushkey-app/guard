package config

import (
	"github.com/hushkey-app/guard/internal/config"
	"github.com/mirairoad/howl-go/core/api"
)

// Update stores the names it is given and removes the ones sent empty.
//
// All or nothing: a value that will not parse, a name guard does not know, or a
// provider's credentials left half-filled refuses the whole save. Guard treats
// half a sign-in configuration as fatal at startup, deliberately, so the moment
// to say so is while somebody is still looking at the field — not from a log
// file at the next restart, with the dashboard down.
var Update = api.Define(api.Spec[api.None, Values, config.State]{
	Name:  "Update Configuration",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Values]) (config.State, error) {
		set := Get()
		if set == nil {
			return config.State{}, api.BadRequest("this instance has no stored configuration")
		}
		state, err := set.Save(r.Body.Values)
		if err != nil {
			return state, api.BadRequest(err.Error())
		}
		return state, nil
	},
})
