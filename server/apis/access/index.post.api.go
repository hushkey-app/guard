package access

import (
	"github.com/hushkey-app/guard/internal/access"
	"github.com/mirairoad/howl-go/core/api"
)

// Generate mints a new value for one credential and writes it down.
//
// It is not in force yet, and the answer says so: the process is running the
// environment it started with. The restart is the next press, on purpose —
// it drops the dashboard it was pressed on and everything mid-flight at
// ingest, and that should be a thing somebody chooses rather than a thing that
// happens because they clicked "generate" to see what one looks like.
var Generate = api.Define(api.Spec[api.None, Request, access.State]{
	Name:  "Generate Credential",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Request]) (access.State, error) {
		state, err := Get().Generate(r.Body.Name)
		if err != nil {
			return state, api.BadRequest(err.Error())
		}
		return state, nil
	},
})
