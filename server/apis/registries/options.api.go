package registries

import (
	"errors"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// OptionsQuery names the account a create form is being drawn for. The
// regions and the plans belong to the provider, so which account is asked
// decides both.
type OptionsQuery struct {
	Account int64 `query:"account"`
}

func (q OptionsQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored cloud account")
	}
	return nil
}

// Options is what a registry can be created as: where it may live and what
// it may cost. Read live and only when the form is opened — it is the
// provider's price list, and a stale one would be a bill somebody did not
// agree to.
var Options = api.Define(api.Spec[OptionsQuery, api.None, cloud.RegistryOptions]{
	Name: "Registry Options",
	Handler: func(r *api.Request[OptionsQuery, api.None]) (cloud.RegistryOptions, error) {
		made, creds, err := maker(r.Query.Account)
		if err != nil {
			return cloud.RegistryOptions{}, err
		}
		options, err := made.RegistryOptions(r.Context(), creds)
		if err != nil {
			return cloud.RegistryOptions{}, fail(err)
		}
		if options.Regions == nil {
			options.Regions = []cloud.Region{}
		}
		if options.Plans == nil {
			options.Plans = []cloud.Tier{}
		}
		return options, nil
	},
})
