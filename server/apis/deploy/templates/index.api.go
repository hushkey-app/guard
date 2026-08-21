package templates

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Templates is the newest version of each, with its revision history.
//
// The history comes with the list rather than behind a second request: it is a
// handful of rows, and "which version is this and what else is there" is part
// of reading a template rather than a thing somebody goes looking for.
var Templates = api.Define(api.Spec[api.None, api.None, []model.DeployTemplate]{
	Name:  "Deploy Templates",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.DeployTemplate, error) {
		return store.Get().DeployTemplates()
	},
})
