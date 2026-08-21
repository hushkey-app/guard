package deploy

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/deployer"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Resolve answers a run that stopped at a failed machine.
//
// Three answers and no fourth: retry the machine, skip it and carry on, or stop
// the run. Rollback is not one of them — it is a deploy of the last known good
// tag, which is the ordinary endpoint with a different tag, so it leaves a run
// record of its own instead of hiding inside this one.
//
// A run that is not waiting says so. "I pressed retry and nothing happened" is
// the worst thing this feature could do, so the refusal is explicit rather than
// a silent success.
var Resolve = api.Define(api.Spec[api.None, Answer, model.DeployRun]{
	Name:  "Answer Stopped Deploy",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Answer]) (model.DeployRun, error) {
		runner := deployer.Get()
		if runner == nil {
			return model.DeployRun{}, api.Unavailable("this instance is running without a deploy runner")
		}
		if err := runner.Resolve(r.Body.RunID, r.Body.Decision); err != nil {
			return model.DeployRun{}, api.BadRequest(err.Error())
		}
		return store.Get().DeployRun(r.Body.RunID)
	},
})
