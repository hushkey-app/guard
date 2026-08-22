package analytics

import (
	"time"

	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Path is what one opened row is drawn from: that path's days, page views and
// the sessions behind them, and where those sessions came from.
//
// The one thing the fold reads on its own. Everything else it shows — every
// action on the path, pinned or not — is already in the grid's answer, because
// two reads of what happened on a page are two answers to one question. These
// two are a different question: they are a hundred times the rows, wanted for
// the handful of paths somebody actually opens, so they are asked for per path
// rather than carried for every path on a timer.
var Path = api.Define(api.Spec[contract.AnalyticsPathQuery, api.None, contract.AnalyticsPath]{
	Name: "Analytics Path",
	Handler: func(r *api.Request[contract.AnalyticsPathQuery, api.None]) (contract.AnalyticsPath, error) {
		// One clock for both halves, the way the grid's endpoint resolves one
		// for the strip and the rows: a chart and a source list a second apart
		// would be two windows drawn as one panel.
		from, to := r.Query.Window(time.Now())
		series, err := store.Get().AnalyticsPathSeries(r.Query.Path, from, to)
		if err != nil {
			return contract.AnalyticsPath{}, err
		}
		sources, err := store.Get().AnalyticsPathSources(r.Query.Path, from, to)
		if err != nil {
			return contract.AnalyticsPath{}, err
		}
		return contract.AnalyticsPath{Series: series, Sources: sources}, nil
	},
})
