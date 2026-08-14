package accounts

import (
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Remove forgets a cloud account and its sealed key. Nothing at the provider
// is touched — the registries, the machines and the storage all live there —
// so this is the one delete on these pages that destroys nothing but access.
//
// What it does change here: every machine linked into this account is
// unlinked, because a link to a key guard can no longer open is a provider
// strip that can only say "the stored key could not be opened".
var Remove = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Remove Cloud Account",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return api.None{}, api.Invalid("id", "must be a number")
		}
		if err := store.Get().DeleteProviderAccount(id); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("no such cloud account")
		} else if err != nil {
			return api.None{}, err
		}
		slog.Info("cloud account removed", slog.Int64("account", id))
		return api.None{}, nil
	},
})
