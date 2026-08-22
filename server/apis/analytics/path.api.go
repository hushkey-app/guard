package analytics

import (
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Path is what one opened row is drawn from: that path's days, page views and
// the sessions behind them.
//
// The one thing the fold reads on its own. Everything else it shows — every
// action on the path, pinned or not — is already in the grid's answer, because
// two reads of what happened on a page are two answers to one question. A day
// series is a different question: it is a hundred times the rows, wanted for
// the handful of paths somebody actually opens, so it is asked for per path
// rather than carried for every path on a timer.
var Path = api.Define(api.Spec[contract.AnalyticsPathQuery, api.None, []model.PathPoint]{
	Name: "Analytics Path",
	Handler: func(r *api.Request[contract.AnalyticsPathQuery, api.None]) ([]model.PathPoint, error) {
		from, to := r.Query.Window(time.Now())
		return store.Get().AnalyticsPathSeries(r.Query.Path, from, to)
	},
})
