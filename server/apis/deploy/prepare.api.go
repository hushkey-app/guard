package deploy

import (
	"github.com/hushkey-app/guard/internal/deploy"
	"github.com/hushkey-app/guard/server/apis/deployer"
	"github.com/mirairoad/howl-go/core/api"
)

// Preparing is what one machine's install is saying right now.
//
// Polled rather than streamed, and that is a deliberate choice. A live
// connection is per-tab: close the tab and the record of what happened goes
// with it, and a rolling deploy across five machines would need five of them.
// The output is written where the page is already looking on its own tick, so
// reopening the page mid-install shows the same thing everyone else sees.
var Preparing = api.Define(api.Spec[StateQuery, api.None, deploy.Preparation]{
	Name:  "Machine Preparation",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[StateQuery, api.None]) (deploy.Preparation, error) {
		runner := deployer.Get()
		if runner == nil {
			return deploy.Preparation{}, api.Unavailable("this instance is running without a deploy runner")
		}
		report, found := runner.Preparing(r.Query.NodeID)
		if !found {
			return deploy.Preparation{}, api.NotFound("nothing has been installed on that machine from here")
		}
		return report, nil
	},
})
