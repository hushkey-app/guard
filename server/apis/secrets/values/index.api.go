// Package values is one environment's pairs — the table the page fills in.
//
// It answers with the values, decrypted. That is the one place guard hands a
// stored secret back to a browser, it is `admin`, and it is the reason this
// page exists: somebody has to be able to read what production is actually
// configured with without sshing into it.
package values

import (
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

type Query struct {
	Env int64 `query:"env"`
}

func (q Query) Validate() error {
	if q.Env <= 0 {
		return errors.New("name an environment")
	}
	return nil
}

var List = api.Define(api.Spec[Query, api.None, []model.Secret]{
	Name:  "Secrets",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Query, api.None]) ([]model.Secret, error) {
		return store.Get().Secrets(r.Query.Env)
	},
})
