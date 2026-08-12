// Package views is the saved-query layer: the CRUD around a dashboard's panels
// and the two endpoints that run one.
//
// A view is a stored question — filters, a grouping, an aggregation, a window —
// and running it produces a model.Frame, which is a result layout rather than a
// chart. The compiler is in internal/telemetry; these files only decide who may
// ask and what a bad request looks like.
package views

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns every saved view in dashboard order.
var List = api.Define(api.Spec[api.None, api.None, []model.View]{
	Name: "Views",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.View, error) {
		return store.Get().Views()
	},
})
