package deploy

import (
	"github.com/hushkey-app/guard/internal/deploy"
	"github.com/hushkey-app/guard/internal/telemetry"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/deployer"
	"github.com/mirairoad/howl-go/core/api"
)

// Deploy starts a run and answers with it immediately.
//
// It answers with the plan rather than the result: a rolling deploy across five
// machines takes minutes, and a request held open for it would be a deploy that
// fails when somebody's laptop sleeps. Everything that can be wrong about the
// request — an unreadable template, a missing vault key, a locked machine, a
// machine already being deployed to — is refused here, before any box is
// touched. What comes back is a run id, and the page follows it.
var Deploy = api.Define(api.Spec[api.None, Start, model.DeployRun]{
	Name:  "Start Deploy",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Start]) (model.DeployRun, error) {
		runner := deployer.Get()
		if runner == nil {
			return model.DeployRun{}, api.Unavailable("this instance is running without a deploy runner")
		}
		run, err := runner.Start(deploy.Request{
			GroupID:         r.Body.GroupID,
			TemplateID:      r.Body.TemplateID,
			TemplateVersion: r.Body.TemplateVersion,
			Tag:             r.Body.Tag,
			Mode:            r.Body.Mode,
			NodeIDs:         r.Body.NodeIDs,
			Rollback:        r.Body.Rollback,
		})
		if err != nil {
			// A locked machine is a conflict rather than a bad request: nothing
			// about what was asked is malformed, and the thing to do about it
			// is not to fix the request.
			if isLocked(err) {
				return model.DeployRun{}, api.Conflict(err.Error())
			}
			return model.DeployRun{}, api.BadRequest(err.Error())
		}
		return run, nil
	},
})

func isLocked(err error) bool {
	for err != nil {
		if err == telemetry.ErrDeployLocked {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}
