package views

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Create saves a new view. model.View.Validate has already run — core/api calls
// it before the handler — so what reaches here is structurally sound; what it
// is not is necessarily *sensible*, and that is the author's call to make.
var Create = api.Define(api.Spec[api.None, model.View, model.View]{
	Name:  "Create View",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.View]) (model.View, error) {
		view := r.Body
		// An id in the body of a create is a client bug, and honouring it would
		// silently overwrite an existing panel.
		view.ID = 0
		saved, err := store.Get().SaveView(view)
		if err != nil {
			return model.View{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
