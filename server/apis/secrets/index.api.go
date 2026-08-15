package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns the workspaces — one per application in the VPC — with what
// each holds.
//
// The page's top level. Counts come down with it because "pack: 4 environments,
// 31 secrets, 2 keys" is what somebody scanning for the application they meant
// is reading, and asking per workspace would be one request per application.
var List = api.Define(api.Spec[api.None, api.None, []model.Workspace]{
	Name:  "Secret Workspaces",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Workspace, error) {
		return store.Get().Workspaces()
	},
})
