package deploy

import (
	"github.com/hushkey-app/guard/internal/deploy"
	"github.com/hushkey-app/guard/server/apis/deployer"
	"github.com/mirairoad/howl-go/core/api"
)

// Machine names the box to prepare, and is the whole request.
//
// A node id and nothing else — the same rule the environment inject keeps. The
// command is a constant in internal/deploy, the login comes off the machine, so
// there is no shape of this call that runs chosen text on a chosen box.
type Machine struct {
	NodeID int64 `json:"node_id"`
}

func (m Machine) Validate() error {
	if m.NodeID <= 0 {
		return api.Invalid("node_id", "no machine named")
	}
	return nil
}

// Prepare installs docker and the compose plugin over the login guard already
// has.
//
// It is a separate press from a deploy on purpose. A deploy that quietly
// installed a package manager's worth of software the first time it ran would
// be doing something nobody asked for on the worst possible day — so the deploy
// refuses with "no docker compose on this machine" and this is the button that
// answers it.
//
// It answers immediately and installs in the background, because a cold box is
// a minute or more of a package manager talking and a request held open for it
// shows nothing and then everything. The page polls GET /api/deploy/prepare for
// the output as it arrives.
//
// Idempotent: a machine that is already fine costs one round trip and reports
// `changed: false`.
var Prepare = api.Define(api.Spec[api.None, Machine, deploy.Preparation]{
	Name:  "Prepare Machine For Deploys",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Machine]) (deploy.Preparation, error) {
		runner := deployer.Get()
		if runner == nil {
			return deploy.Preparation{}, api.Unavailable("this instance is running without a deploy runner")
		}
		report, err := runner.Prepare(r.Body.NodeID)
		if err != nil {
			if isLocked(err) {
				return report, api.Conflict(err.Error())
			}
			return report, api.BadRequest(err.Error())
		}
		return report, nil
	},
})
