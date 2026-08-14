package cloud

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Accounts lists the stored cloud accounts — names and providers, never
// keys. The API answers has_key and the dashboard draws dots, the same
// contract the SSH passwords keep.
var Accounts = api.Define(api.Spec[api.None, api.None, []model.ProviderAccount]{
	Name: "Cloud Accounts",
	Handler: func(r *api.Request[api.None, api.None]) ([]model.ProviderAccount, error) {
		return store.Get().ProviderAccounts()
	},
})
