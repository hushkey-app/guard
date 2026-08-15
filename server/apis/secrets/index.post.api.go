package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save adds a workspace or renames one.
//
// A new one arrives with local, develop, staging and production already in it.
// Adding an application should be one press: the four stages are what almost
// everybody was going to make anyway, and an application that needs a fifth
// adds it, while one that never uses local simply leaves it empty.
var Save = api.Define(api.Spec[api.None, model.Workspace, model.Workspace]{
	Name:  "Save Secret Workspace",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Workspace]) (model.Workspace, error) {
		saved, err := store.Get().SaveWorkspace(r.Body)
		if err != nil {
			return model.Workspace{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
