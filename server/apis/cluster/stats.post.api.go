package cluster

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/collector"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SampleNow asks one machine how it is doing, right now, instead of waiting
// out its cadence — the same button as "check now", aimed at the machine
// rather than at the service.
//
// It runs one fixed, read-only command that lives in guard's source
// (`cluster.StatsCommand`), never anything from the machine's command list.
// That is why it is allowed against a locked machine: locking closes the list
// of things somebody can add, and this is not on it.
//
// A sample that failed is a 200 carrying the reason. "Guard cannot get in
// since 04:12" is the answer, and failing the request would hide it behind a
// toast that says "error".
var SampleNow = api.Define(api.Spec[api.None, contract.NodeRequest, model.HostStats]{
	Name:  "Sample Machine",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.NodeRequest]) (model.HostStats, error) {
		node, err := store.Get().Node(r.Body.NodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return model.HostStats{}, api.NotFound("no such machine")
		}
		if err != nil {
			return model.HostStats{}, err
		}
		sampler := collector.Get()
		if sampler == nil {
			return model.HostStats{}, api.BadRequest("this guard is running without a stats collector")
		}
		return sampler.SampleNode(r.Context(), node.ID), nil
	},
})
