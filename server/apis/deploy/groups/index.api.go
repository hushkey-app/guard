package groups

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Groups is every group with its machines.
//
// Each member carries whether it has a login and whether it is locked, because
// those are the two reasons a deploy will refuse — and a page that only found
// out at the press would be a page that lets somebody line up a deploy that
// cannot run.
var Groups = api.Define(api.Spec[api.None, api.None, []model.DeployGroup]{
	Name:  "Deploy Groups",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.DeployGroup, error) {
		return store.Get().DeployGroups()
	},
})
