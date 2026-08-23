package analytics

import (
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// The preview is run against the last hundred distinct paths the tracker sent.
// Enough that a rule meets the variants that made somebody write it, few enough
// to read down a dialog — and a number beside its reader rather than a knob,
// like every other cadence and ceiling in guard.
const previewPaths = 100

// Preview answers what a set of rules that has not been saved would make of the
// paths that have actually arrived.
//
// The paths come from the store, never from the request: a preview run against
// paths a caller supplied would prove a rule against a site somebody imagined,
// and the reason to prove it at all is that a rule shapes what is stored and
// cannot be applied to the days already rolled up.
//
// It reads and stores nothing, so it declares no role — for the same reason the
// view builder's preview does not: needing to be an admin to find out what a
// rule would do pushes people to save one in order to see it.
var Preview = api.Define(api.Spec[api.None, contract.PathRuleSet, []contract.PathPreview]{
	Name: "Preview Analytics Path Rules",
	Handler: func(r *api.Request[api.None, contract.PathRuleSet]) ([]contract.PathPreview, error) {
		paths, err := store.Get().AnalyticsRecentPaths(previewPaths)
		if err != nil {
			return nil, err
		}
		// The same preparation and the same application the save runs, so the
		// dialog cannot describe something other than what the press does —
		// which also means a pattern that will not compile is refused here, in
		// front of somebody who is still typing it.
		results, err := store.Get().PreviewPathRules(r.Body.Rules, paths)
		if err != nil {
			return nil, api.BadRequest(err.Error())
		}
		out := make([]contract.PathPreview, len(paths))
		for i, path := range paths {
			out[i] = contract.PathPreview{Path: path, Result: results[i]}
		}
		return out, nil
	},
})
