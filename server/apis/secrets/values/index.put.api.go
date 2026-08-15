package values

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save writes one pair, by key rather than by id: setting a value is the same
// operation whether or not it was already there, which is also what makes an
// import a loop over this one.
var Save = api.Define(api.Spec[api.None, model.Secret, model.Secret]{
	Name:  "Save Secret",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Secret]) (model.Secret, error) {
		saved, err := store.Get().SaveSecret(r.Body)
		if err != nil {
			return model.Secret{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
