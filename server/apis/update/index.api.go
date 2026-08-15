package update

import (
	"github.com/hushkey-app/guard/internal/release"
	"github.com/mirairoad/howl-go/core/api"
)

// State is what this build is, what the newest release is, and whether this box
// can do anything about it.
//
// Answered from memory: guard polls GitHub on its own timer, so a dashboard
// left open in four tabs costs nothing and cannot spend the sixty requests an
// hour that an unauthenticated address is allowed.
var State = api.Define(api.Spec[api.None, api.None, release.State]{
	Name: "Update State",
	Handler: func(r *api.Request[api.None, api.None]) (release.State, error) {
		return Get().State(), nil
	},
})
