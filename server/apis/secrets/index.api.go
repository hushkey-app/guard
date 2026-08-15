package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns the environments with their counts — the page's left column,
// and everything it needs to draw it: how many secrets are in each group, how
// many live keys read it, and when it last changed.
//
// One query rather than a count per group, because a page that asked per group
// would be four requests on a fresh installation and twenty on a real one.
var List = api.Define(api.Spec[api.None, api.None, []model.Env]{
	Name:  "Secret Environments",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Env, error) {
		return store.Get().Envs()
	},
})
