package update

import (
	"github.com/hushkey-app/guard/internal/release"
	"github.com/mirairoad/howl-go/core/api"
)

// Check asks GitHub now rather than waiting for the next pass of the timer.
//
// It exists because "is there a new version" is a question people ask at a
// moment — after reading a changelog, before a maintenance window — and guard's
// own answer can be a quarter of an hour old. The alternative is a shorter
// timer, which spends the request budget of every instance to serve the few
// minutes somebody is actually looking.
//
// `admin`, unlike reading the state: this one leaves the box. Throttled in the
// watcher rather than here, so the sidebar's timer and this button cannot add
// up to more than GitHub allows.
var Check = api.Define(api.Spec[api.None, api.None, release.State]{
	Name:  "Check For Updates",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (release.State, error) {
		state, _ := Get().CheckNow(r.Context())
		return state, nil
	},
})
