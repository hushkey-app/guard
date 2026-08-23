package analytics

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Pin decides the grid's columns: pin, unpin and reorder are one request,
// because they are one decision — the pinned names, in the order they are drawn.
//
// It answers with the whole list, so the page settles on what was stored rather
// than on the shuffle it drew optimistically.
var Pin = api.Define(api.Spec[api.None, contract.ActionPins, []model.Action]{
	Name:  "Pin Analytics Actions",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.ActionPins]) ([]model.Action, error) {
		// A name that was never seen is the caller's mistake rather than the
		// store's failure — it is a column that could have no cells — so it is
		// answered in words with the name in them.
		actions, err := store.Get().PinAnalyticsActions(r.Body.Pinned)
		if err != nil {
			return nil, api.BadRequest(err.Error())
		}
		return actions, nil
	},
})
