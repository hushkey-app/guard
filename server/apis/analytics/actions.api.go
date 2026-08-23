package analytics

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Actions is the discovery half: every name the tracker has sent, whether it is
// a column, and what has been counted under it.
//
// No window. The grid is read over one, because a page's numbers are only worth
// reading against a span — but "what actions exist" is not a measurement, and a
// list that emptied itself when somebody narrowed the window to a day would be a
// list nobody could pin from.
var Actions = api.Define(api.Spec[api.None, api.None, []model.Action]{
	Name: "Analytics Actions",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Action, error) {
		return store.Get().AnalyticsActions()
	},
})
