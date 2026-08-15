// Package envs is one workspace's stages: local, develop, staging, production,
// or whatever that application needs.
//
// A stage belongs to a workspace rather than being global, so `hushkey` can
// have a `preview` that `auth` does not, and two applications both having a
// `production` is unremarkable rather than a name collision.
package envs

import (
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

type Query struct {
	Workspace int64 `query:"workspace"`
}

func (q Query) Validate() error {
	if q.Workspace <= 0 {
		return errors.New("name a workspace")
	}
	return nil
}

var List = api.Define(api.Spec[Query, api.None, []model.Env]{
	Name:  "Secret Environments",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Query, api.None]) ([]model.Env, error) {
		return store.Get().Envs(r.Query.Workspace)
	},
})
