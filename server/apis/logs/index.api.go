package logs

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List is /api/events with the signal pinned, for callers that only ever want
// logs and should not have to remember to ask.
var List = api.Define(api.Spec[model.Filter, api.None, []model.Event]{
	Name: "Logs",
	Handler: func(r *api.Request[model.Filter, api.None]) ([]model.Event, error) {
		filter := r.Query
		filter.Signal = "logs"
		return store.Get().Query(filter)
	},
})
