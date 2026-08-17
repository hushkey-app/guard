package config

import (
	"github.com/hushkey-app/guard/internal/config"
	"github.com/mirairoad/howl-go/core/api"
)

// Restart makes what is stored the environment guard is running.
//
// Guard has no way to restart a service — it runs unprivileged, with
// NoNewPrivileges, and cannot call systemctl. What it does is exit, and the unit's
// Restart=always starts it again two seconds later against the new configuration.
// So this answers first and exits behind the response: the page that pressed it is
// about to lose its connection, and it polls until guard answers again.
//
// Where nothing would start guard again — a container run by hand, `go run .` —
// this refuses in words rather than stopping the process. An endpoint whose failure
// mode is "the tool is now off and you are not near the box" is not one to be
// clever about.
var Restart = api.Define(api.Spec[api.None, api.None, config.State]{
	Name:  "Restart Guard",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (config.State, error) {
		set := Get()
		if set == nil {
			return config.State{}, api.BadRequest("this instance has no stored configuration")
		}
		if err := set.Restart(); err != nil {
			state, _ := set.State()
			return state, api.BadRequest(err.Error())
		}
		state, err := set.State()
		if err != nil {
			return config.State{}, err
		}
		return state, nil
	},
})
