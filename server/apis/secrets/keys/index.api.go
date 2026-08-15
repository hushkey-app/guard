// Package keys is the tokens applications hold: minted here, spent against
// guard-vault.
//
// A token is random, and what is stored is its SHA-256 — the same contract as
// a browser session. So the list carries prefixes and dates and nothing that
// could be presented to anything, and the only moment the token itself exists
// on this side of the wire is the answer to the request that created it.
package keys

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var List = api.Define(api.Spec[api.None, api.None, []model.APIKey]{
	Name:  "Secret Keys",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.APIKey, error) {
		return store.Get().APIKeys()
	},
})
