package registries

import (
	"errors"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// TagQuery reaches one repository. The repo is the full name the registry
// knows — "hushkey/pack" — exactly as the repository listing spelled it.
type TagQuery struct {
	Account  int64  `query:"account"`
	Registry string `query:"registry"`
	Repo     string `query:"repo"`
}

func (q TagQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored registry account")
	}
	if q.Registry == "" {
		return errors.New("registry is required")
	}
	if q.Repo == "" {
		return errors.New("repo is required")
	}
	return nil
}

// Tags lists one repository's tags with digests and sizes. This is the one
// read that goes to the registry itself rather than the provider's account
// API — the account API stops at the repository level — so it runs the
// docker token flow with credentials the provider just returned, and the
// browser sees only the result.
var Tags = api.Define(api.Spec[TagQuery, api.None, []cloud.Tag]{
	Name: "Registry Tags",
	Handler: func(r *api.Request[TagQuery, api.None]) ([]cloud.Tag, error) {
		registries, creds, err := open(r.Query.Account)
		if err != nil {
			return nil, err
		}
		tags, err := registries.Tags(r.Context(), creds, r.Query.Registry, r.Query.Repo)
		if err != nil {
			return nil, fail(err)
		}
		if tags == nil {
			tags = []cloud.Tag{}
		}
		return tags, nil
	},
})
