package config

import (
	"github.com/hushkey-app/guard/internal/config"
	"github.com/mirairoad/howl-go/core/api"
)

// Name selects the setting to mint.
type Name struct {
	Name string `json:"name"`
}

// Generate mints a value for one of the two credentials and stores it.
//
// It exists so nobody has to open a shell for `openssl rand -hex 32`, which was
// the last step of this page that still needed one. Only the rows the catalogue
// marks generatable qualify: a value guard invents for an OAuth client secret or
// an alert token would be a value the far end has never heard of, and minting
// GUARD_SECRET_KEY would orphan every sealed row in the database.
//
// It goes through the same save as a typed value — validated, logged, and pending
// a restart — because it is the same thing: a stored setting the process has not
// read yet.
var Generate = api.Define(api.Spec[api.None, Name, config.State]{
	Name:  "Generate Credential",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Name]) (config.State, error) {
		set := Get()
		if set == nil {
			return config.State{}, api.BadRequest("this instance has no stored configuration")
		}
		state, err := set.Generate(r.Body.Name)
		if err != nil {
			return state, api.BadRequest(err.Error())
		}
		return state, nil
	},
})
