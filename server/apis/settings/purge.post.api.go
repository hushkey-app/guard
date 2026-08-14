package settings

import (
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Purge applies retention and capacity now.
var Purge = api.Define(api.Spec[api.None, api.None, contract.Purged]{
	Name:  "Purge",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (contract.Purged, error) {
		removed, err := store.Get().Purge()
		return contract.Purged{Removed: removed}, err
	},
})
