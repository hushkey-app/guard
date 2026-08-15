package registries

import (
	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// AccountRegistries is one stored account with what its key can see right
// now. The error travels in the row rather than failing the request: a
// revoked key on one account must not blank the page for every other, and
// neither must an account at a provider that has no registries at all.
//
// Capabilities ride along because the buttons on a card are the provider's
// answer, not the page's opinion — whether this account's registries can be
// created and deleted is decided once, in the provider package, and read
// here.
type AccountRegistries struct {
	Account      model.ProviderAccount `json:"account"`
	Capabilities cloud.Capabilities    `json:"capabilities"`
	Registries   []cloud.Registry      `json:"registries"`
	Error        string                `json:"error,omitempty"`
}

// Overview lists every registry every stored key can see, read live from
// the provider. Nothing is cached on the server: the page asks when it is
// opened, not on the live tick, and a fresh answer is the point of asking.
var Overview = api.Define(api.Spec[api.None, api.None, []AccountRegistries]{
	Name: "Registries",
	Handler: func(r *api.Request[api.None, api.None]) ([]AccountRegistries, error) {
		accounts, err := store.Get().ProviderAccounts()
		if err != nil {
			return nil, err
		}
		out := make([]AccountRegistries, 0, len(accounts))
		for _, account := range accounts {
			row := AccountRegistries{Account: account, Registries: []cloud.Registry{}}
			if provider, findErr := cloud.For(account.Provider); findErr == nil {
				row.Capabilities = cloud.Describe(provider).Capabilities
			}
			// An account whose provider has no registries is not an error and
			// not a card: the storage page is where that account belongs.
			if !row.Capabilities.Registries {
				continue
			}
			registries, creds, openErr := apicloud.Registries(account.ID)
			if openErr != nil {
				row.Error = openErr.Error()
				out = append(out, row)
				continue
			}
			list, listErr := registries.Registries(r.Context(), creds)
			if listErr != nil {
				row.Error = listErr.Error()
			} else if list != nil {
				row.Registries = list
			}
			out = append(out, row)
		}
		return out, nil
	},
})
