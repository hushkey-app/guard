package access

import (
	"github.com/hushkey-app/guard/internal/access"
	"github.com/mirairoad/howl-go/core/api"
)

// Read is the two credentials as they stand: what the next start will use,
// whether it differs from what this process is running, and whether this box
// is one where guard can change either.
var Read = api.Define(api.Spec[api.None, api.None, access.State]{
	Name:  "Access Credentials",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (access.State, error) {
		return Get().State(), nil
	},
})
