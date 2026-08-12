package events

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns telemetry events matching the filter, newest first.
//
// model.Filter is the query type: the same struct the store takes, so the
// URL contract and the database query cannot disagree.
var List = api.Define(api.Spec[model.Filter, api.None, []model.Event]{
	Name: "Events",
	Handler: func(r *api.Request[model.Filter, api.None]) ([]model.Event, error) {
		return store.Get().Query(r.Query)
	},
})
