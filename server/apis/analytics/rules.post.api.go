package analytics

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// SaveRules replaces the list with the one that was sent.
//
// One request for adding, editing, removing and reordering, because they are
// one decision: which patterns are tried, and in what order. It answers with
// the stored list, so the page settles on what guard kept rather than on what
// the form held.
//
// A pattern that will not compile, a duplicate, or a half of a rule that is not
// a path is refused in words and nothing is written — the same refusal the
// preview beside it already showed, which is what makes the dialog worth
// reading.
var SaveRules = api.Define(api.Spec[api.None, contract.PathRuleSet, []model.PathRule]{
	Name:  "Save Analytics Path Rules",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.PathRuleSet]) ([]model.PathRule, error) {
		rules, err := store.Get().SavePathRules(r.Body.Rules)
		if err != nil {
			return nil, api.BadRequest(err.Error())
		}
		return rules, nil
	},
})
