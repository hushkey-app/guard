package views

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/contract"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Preview runs a query that has not been saved.
//
// It reads rather than writes, so it declares no role: a builder you must be an
// admin to *use* would push people to save a panel in order to find out whether
// it was the panel they wanted. Saving it is the part that needs the token.
var Preview = api.Define(api.Spec[api.None, contract.PreviewRequest, model.Frame]{
	Name: "Preview View",
	Handler: func(r *api.Request[api.None, contract.PreviewRequest]) (model.Frame, error) {
		frame, err := store.Get().RunView(r.Body.Panel, r.Body.Query)
		if err != nil {
			return model.Frame{}, api.BadRequest(err.Error())
		}
		return frame, nil
	},
})
