package storage

import (
	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// AccountStorage is one stored account with the object storage its key can
// see right now. The error travels in the row rather than failing the
// request: a revoked key on one account must not blank the page for every
// other.
//
// Capabilities ride along because what a card may offer is the provider's
// answer, not the page's opinion. A Vultr subscription can be renamed and its
// keys revealed; an R2 bucket can do neither, and the card that says so is
// drawn from this rather than from a provider name in the JavaScript.
type AccountStorage struct {
	Account      model.ProviderAccount `json:"account"`
	Capabilities cloud.Capabilities    `json:"capabilities"`
	Storage      []cloud.Storage       `json:"storage"`
	Error        string                `json:"error,omitempty"`
}

// Overview lists every object storage every stored key can see, read live.
// Nothing is cached on the server: the page asks when it is opened, not on
// the dashboard's tick, and a fresh answer is the point of asking.
//
// The keys are not in this answer. Every row says whether a pair exists, and
// the page draws dots — the same contract the SSH passwords and the account
// keys keep.
var Overview = api.Define(api.Spec[api.None, api.None, []AccountStorage]{
	Name: "Object Storage",
	Handler: func(r *api.Request[api.None, api.None]) ([]AccountStorage, error) {
		accounts, err := store.Get().ProviderAccounts()
		if err != nil {
			return nil, err
		}
		out := make([]AccountStorage, 0, len(accounts))
		for _, account := range accounts {
			row := AccountStorage{Account: account, Storage: []cloud.Storage{}}
			if provider, findErr := cloud.For(account.Provider); findErr == nil {
				row.Capabilities = cloud.Describe(provider).Capabilities
			}
			// An account whose provider has no object storage is not an error
			// and not a row: it belongs on one of the other pages.
			if !row.Capabilities.Storage {
				continue
			}
			storages, creds, openErr := apicloud.Storages(account.ID)
			if openErr != nil {
				row.Error = openErr.Error()
				out = append(out, row)
				continue
			}
			list, listErr := storages.Storages(r.Context(), creds)
			if listErr != nil {
				row.Error = listErr.Error()
			} else if list != nil {
				row.Storage = list
			}
			out = append(out, row)
		}
		return out, nil
	},
})
