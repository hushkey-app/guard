package deploy

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/deployer"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Run names a deploy to stop.
type Run struct {
	RunID int64 `json:"run_id"`
}

func (r Run) Validate() error {
	if r.RunID <= 0 {
		return api.Invalid("run_id", "no run named")
	}
	return nil
}

// Cancel stops a deploy that is still going.
//
// It is deliberately not called "abort", because it undoes nothing. What it
// stops is guard *advancing* and guard *waiting*: no machine after the current
// one is touched, and the health gate and SSH session are cut rather than left
// to run down their clocks. A machine already deployed to keeps what it was
// given, and the one in flight may have a container running that guard never
// proved — the run says exactly that rather than calling it a failure.
//
// Going back is a deploy of the last known good tag, which is the ordinary
// press. There is no undo here and there should not be one: the only honest
// way back is forward through the same gate.
var Cancel = api.Define(api.Spec[api.None, Run, model.DeployRun]{
	Name:  "Cancel Deploy",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Run]) (model.DeployRun, error) {
		runner := deployer.Get()
		if runner == nil {
			return model.DeployRun{}, api.Unavailable("this instance is running without a deploy runner")
		}
		if err := runner.Cancel(r.Body.RunID); err != nil {
			return model.DeployRun{}, api.BadRequest(err.Error())
		}
		return store.Get().DeployRun(r.Body.RunID)
	},
})
