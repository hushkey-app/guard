package keys

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Create mints one token for one environment and answers with it once.
//
// Once is the contract, and it is worth being blunt about in the UI: nothing in
// guard can produce this string again, because only its hash was kept. A key
// that was not copied is replaced, not recovered — a worse afternoon for one
// person, and the reason a leaked database is not a list of live credentials.
//
// One key, one environment. A token that could read three of them is a token
// nobody dares rotate when one service is redeployed.
var Create = api.Define(api.Spec[api.None, model.APIKey, model.APIKey]{
	Name:  "Create Secret Key",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.APIKey]) (model.APIKey, error) {
		minted, err := store.Get().CreateAPIKey(r.Body)
		if err != nil {
			return model.APIKey{}, api.BadRequest(err.Error())
		}
		return minted, nil
	},
})
