package config

import (
	"github.com/hushkey-app/guard/internal/config"
	"github.com/mirairoad/howl-go/core/api"
)

// Read is the whole catalogue: every variable guard knows about, what the next
// start will use for it, where that came from, and whether this process is still
// running something else.
var Read = api.Define(api.Spec[api.None, api.None, config.State]{
	Name:  "Configuration",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (config.State, error) {
		set := Get()
		if set == nil {
			return config.State{}, api.BadRequest("this instance has no stored configuration")
		}
		state, err := set.State()
		if err != nil {
			return config.State{}, err
		}
		return state, nil
	},
})
