// Package analytics is the read side of the tracker: what a browser did, drawn
// as one row per URL.
//
// Everything here is answered from analytics_rollup rather than from the raw
// feed. The raw feed is capped and swept by the telemetry retention, and the
// question analytics is actually asked is "versus last month" — so an endpoint
// reading the events table would go quiet exactly when somebody needed it.
package analytics

import (
	"time"

	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Index is the strip and the grid over one window.
//
// A GET, deliberately, like the view data endpoint: the page refreshes on a
// timer and mw.Coalesce only shares identical concurrent GETs, so two tabs open
// on the same window cost one pair of reads rather than two.
var Index = api.Define(api.Spec[contract.AnalyticsQuery, api.None, contract.Analytics]{
	Name: "Analytics",
	Handler: func(r *api.Request[contract.AnalyticsQuery, api.None]) (contract.Analytics, error) {
		// One clock for both halves. Resolved here rather than inside each
		// store call, because a strip and a grid that disagreed about where
		// "now" was would be two windows drawn as one.
		from, to := r.Query.Window(time.Now())
		summary, err := store.Get().AnalyticsSummary(from, to)
		if err != nil {
			return contract.Analytics{}, err
		}
		paths, err := store.Get().AnalyticsPaths(from, to)
		if err != nil {
			return contract.Analytics{}, err
		}
		return contract.Analytics{Summary: summary, Paths: paths}, nil
	},
})
