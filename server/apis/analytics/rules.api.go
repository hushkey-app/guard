package analytics

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Rules is the ordered list of path rules, in the order they are applied.
//
// Configuration rather than a measurement, so it takes no window: a rule shapes
// what is stored, and the rule that collapsed a path a month ago is the same
// row as the one collapsing it now.
var Rules = api.Define(api.Spec[api.None, api.None, []model.PathRule]{
	Name: "Analytics Path Rules",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.PathRule, error) {
		return store.Get().PathRules()
	},
})
