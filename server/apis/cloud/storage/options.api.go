package storage

import (
	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// AccountOptions names one account to ask with. Where storage may live and
// what it costs there belong to the provider, so which account is asked
// decides both.
type AccountOptions struct {
	Account int64 `query:"account"`
}

func (a AccountOptions) Validate() error {
	if a.Account <= 0 {
		return api.Invalid("account", "must name a stored cloud account")
	}
	return nil
}

// Options lists where object storage can be created, and at what price.
//
// Read from the provider rather than hard-coded, because a list of regions
// baked into guard is a list that is wrong the week a new one opens — and
// somebody would find out by not seeing the region they were told to use.
// The one exception is a provider whose regions are part of its API's shape
// rather than its account's state: R2 has six location hints and one price,
// and asking Cloudflare for them every time the form opens would be a request
// that can only ever come back with the same six.
var Options = api.Define(api.Spec[AccountOptions, api.None, cloud.StorageOptions]{
	Name:  "Object Storage Options",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[AccountOptions, api.None]) (cloud.StorageOptions, error) {
		storages, creds, err := storagesFor(r.Query.Account)
		if err != nil {
			return cloud.StorageOptions{}, err
		}
		options, err := storages.StorageOptions(r.Context(), creds)
		if err != nil {
			return cloud.StorageOptions{}, fail(err)
		}
		if options.Regions == nil {
			options.Regions = []cloud.Region{}
		}
		if options.Tiers == nil {
			options.Tiers = []cloud.Tier{}
		}
		if options.Classes == nil {
			options.Classes = []string{}
		}
		return options, nil
	},
})
