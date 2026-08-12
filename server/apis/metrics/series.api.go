package metrics

import (
	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/contract"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Series returns one line per group, ordered oldest to newest so a chart can
// draw it without sorting.
var Series = api.Define(api.Spec[contract.SeriesQuery, api.None, []model.MetricSeries]{
	Name: "Metric Series",
	Handler: func(r *api.Request[contract.SeriesQuery, api.None]) ([]model.MetricSeries, error) {
		q := r.Query
		return store.Get().Metrics(model.Filter{
			Signal: "metrics", Name: q.Name, Service: q.Service,
			From: q.From, To: q.To, Limit: q.Limit,
		}, q.GroupBy)
	},
})
