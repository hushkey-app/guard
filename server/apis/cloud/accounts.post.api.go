package cloud

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// AddAccount stores a cloud account, but only after its key has answered the
// provider once. The same rule as the SSH logins: a key saved with a typo
// looks exactly like a key saved correctly, and the difference should not be
// discovered by an empty page next week. A key that was accepted is a key
// that worked at least once.
//
// What the proof is belongs to the provider — Vultr lists registries,
// Cloudflare lists buckets — and both were picked for the same reason: the
// narrowest read the account has, one that says the key is real without
// asking for anything the account might not own yet.
var AddAccount = api.Define(api.Spec[api.None, model.ProviderAccount, model.ProviderAccount]{
	Name:  "Add Cloud Account",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.ProviderAccount]) (model.ProviderAccount, error) {
		account := r.Body
		account.ID = 0
		account.ExternalID = strings.TrimSpace(account.ExternalID)
		if account.Provider == "" {
			account.Provider = model.ProviderVultr
		}
		if err := account.Validate(); err != nil {
			return model.ProviderAccount{}, api.BadRequest(err.Error())
		}
		if account.APIKey == nil || *account.APIKey == "" {
			return model.ProviderAccount{}, api.BadRequest("an api key is required")
		}
		if err := Verify(r.Context(), account, *account.APIKey); err != nil {
			return model.ProviderAccount{}, err
		}
		saved, err := store.Get().SaveProviderAccount(account)
		if err != nil {
			return model.ProviderAccount{}, api.BadRequest(err.Error())
		}
		slog.Info("cloud account added",
			slog.Int64("account", saved.ID), slog.String("name", saved.Name),
			slog.String("provider", saved.Provider))
		return saved, nil
	},
})
