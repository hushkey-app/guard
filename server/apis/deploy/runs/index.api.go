package runs

import (
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Runs is the deploy history, newest first, each with its machines.
var Runs = api.Define(api.Spec[Query, api.None, Page]{
	Name:  "Deploy Runs",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Query, api.None]) (Page, error) {
		// The active runs are a different question — "what is happening right
		// now" — and are never paged: there are at most a handful, and a pager
		// over the thing somebody is watching would be absurd.
		if r.Query.Active {
			runs, err := store.Get().ActiveDeployRuns()
			return Page{Runs: runs, Total: len(runs)}, err
		}
		runs, err := store.Get().DeployRuns(r.Query.Limit, r.Query.Offset)
		if err != nil {
			return Page{}, err
		}
		total, err := store.Get().CountDeployRuns()
		if err != nil {
			return Page{}, err
		}
		return Page{Runs: runs, Total: total}, nil
	},
})
