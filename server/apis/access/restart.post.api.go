package access

import (
	"github.com/hushkey-app/guard/internal/access"
	"github.com/mirairoad/howl-go/core/api"
)

// Restart makes the file on disk the environment guard is running.
//
// Guard has no way to restart a service — it runs unprivileged, with
// NoNewPrivileges, and cannot call systemctl. What it does is exit, and the
// unit's Restart=always starts it again two seconds later against the new env
// file. So this answers first and exits behind the response: the page that
// pressed it is about to lose its connection, and it polls until guard answers
// again.
//
// Where nothing would start guard again — a container run by hand, `go run .`
// — this refuses in words rather than stopping the process. An endpoint whose
// failure mode is "the tool is now off and you are not near the box" is not
// one to be clever about.
var Restart = api.Define(api.Spec[api.None, api.None, access.State]{
	Name:  "Restart Guard",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (access.State, error) {
		keys := Get()
		if err := keys.Ask(); err != nil {
			return keys.State(), api.BadRequest(err.Error())
		}
		return keys.State(), nil
	},
})
