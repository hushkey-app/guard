package events

import (
	"strconv"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// ByID is one event with its attributes, for the detail panel.
var ByID = api.Define(api.Spec[api.None, api.None, model.Event]{
	Name: "Event",
	Handler: func(r *api.Request[api.None, api.None]) (model.Event, error) {
		id, err := strconv.ParseUint(r.Param("id"), 10, 64)
		if err != nil {
			return model.Event{}, api.Invalid("id", "must be a number")
		}
		event, err := store.Get().Event(id)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "no rows") {
				return model.Event{}, api.NotFound("event not found")
			}
			return model.Event{}, err
		}
		return event, nil
	},
})
