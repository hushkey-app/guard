package views

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/contract"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Order rewrites the dashboard's layout.
//
// One request for the whole order rather than a position per panel: dragging
// one card moves every card after it, and sending sixteen updates would be
// sixteen chances for the layout to end up half-applied.
//
// It answers with the views in their new order, so the browser can settle on
// what was actually stored instead of trusting its own optimistic shuffle.
var Order = api.Define(api.Spec[api.None, contract.ViewOrder, []model.View]{
	Name:  "Reorder Views",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.ViewOrder]) ([]model.View, error) {
		if err := store.Get().ReorderViews(r.Body.IDs); err != nil {
			return nil, api.BadRequest(err.Error())
		}
		return store.Get().Views()
	},
})
