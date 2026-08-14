package registries

import (
	"errors"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// RepoQuery names one registry under one stored account. Everything below
// the account level is addressed this way, because the account is what the
// key belongs to and the registry is what the provider's answers hang off.
type RepoQuery struct {
	Account  int64  `query:"account"`
	Registry string `query:"registry"`
}

func (q RepoQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored registry account")
	}
	if q.Registry == "" {
		return errors.New("registry is required")
	}
	return nil
}

// Repos lists what one registry holds, read live from the provider.
var Repos = api.Define(api.Spec[RepoQuery, api.None, []cloud.Repository]{
	Name: "Registry Repositories",
	Handler: func(r *api.Request[RepoQuery, api.None]) ([]cloud.Repository, error) {
		registries, creds, err := open(r.Query.Account)
		if err != nil {
			return nil, err
		}
		repos, err := registries.Repositories(r.Context(), creds, r.Query.Registry)
		if err != nil {
			return nil, fail(err)
		}
		if repos == nil {
			repos = []cloud.Repository{}
		}
		return repos, nil
	},
})
