package apis

import (
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Health is the liveness probe. Path is overridden because /healthz is a
// convention older than this application and every orchestrator expects it at
// the root rather than under /api.
//
// It touches the database on purpose: a process that is running but cannot read
// its store is not healthy, and answering 200 from memory is how an outage
// stays invisible until someone opens the dashboard.
var Health = api.Define(api.Spec[api.None, api.None, contract.Health]{
	Name: "Health",
	Path: "/healthz",
	Handler: func(r *api.Request[api.None, api.None]) (contract.Health, error) {
		if _, err := store.Get().Settings(); err != nil {
			return contract.Health{}, api.Unavailable("database unavailable")
		}
		summary, err := store.Get().Snapshot()
		if err != nil {
			return contract.Health{}, api.Unavailable("database unavailable")
		}
		return contract.Health{Status: "ok", Events: summary.Stored}, nil
	},
})
