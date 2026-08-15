package keys

import (
	"strconv"

	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Revoke stops a token without forgetting it.
//
// The row stays, marked and dated, because it is the only record the key ever
// existed: "revoked in March" is the answer to somebody finding it in an old
// deployment file next year, and a deleted row answers that with silence.
// Revocation takes effect on the next fetch — the vault looks the hash up
// every time, which is the whole reason this is an opaque token and not a
// signed one.
var Revoke = api.Define(api.Spec[api.None, api.None, api.None]{
	Name:  "Revoke Secret Key",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (api.None, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil {
			return api.None{}, api.BadRequest("that is not a key id")
		}
		if err := store.Get().RevokeAPIKey(id); err != nil {
			return api.None{}, err
		}
		return api.None{}, nil
	},
})
