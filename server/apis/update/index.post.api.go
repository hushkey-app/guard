package update

import (
	"github.com/hushkey-app/guard/internal/release"
	"github.com/mirairoad/howl-go/core/api"
)

// Request names the version this box should be on.
type Request struct {
	Version string `json:"version"`
}

// Apply writes the wanted version down, and nothing else.
//
// It is not an install and it does not restart anything: `deploy/guard-update`
// is started immediately by a path unit, verifies the checksum of what it downloads,
// restarts guard and then the vault, and rolls either back if it does not
// answer. So the answer here is "asked for", and the sidebar says so — the
// change starts immediately, and the page it was pressed on is
// one of the things that gets restarted.
//
// Only the release guard has actually seen may be named, because the file is
// read by something running as root that puts the value in a URL.
var Apply = api.Define(api.Spec[api.None, Request, release.State]{
	Name:  "Request Update",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Request]) (release.State, error) {
		state, err := Get().Request(r.Body.Version)
		if err != nil {
			return state, api.BadRequest(err.Error())
		}
		return state, nil
	},
})
