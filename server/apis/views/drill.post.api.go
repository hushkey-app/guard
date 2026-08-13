package views

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/contract"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Drill returns the events behind one mark on a panel.
//
// It is the step that makes a chart worth clicking: a bar is 217 events, and
// the reason anyone looks at the bar is to find out which ones. Reading, so no
// role — the same events are already served by /api/events.
var Drill = api.Define(api.Spec[api.None, contract.DrillRequest, model.Drill]{
	Name: "Drill Into Panel",
	Handler: func(r *api.Request[api.None, contract.DrillRequest]) (model.Drill, error) {
		drill, err := store.Get().DrillView(r.Body.Panel, r.Body.Query, r.Body.Selection)
		if err != nil {
			return model.Drill{}, api.BadRequest(err.Error())
		}
		return drill, nil
	},
})
