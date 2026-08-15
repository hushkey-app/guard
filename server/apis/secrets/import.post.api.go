package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Import applies a paste of .env text to one environment.
//
// The same call answers "what would this do" and "do it", which is the point:
// the page asks with dry_run first, shows the counts on the confirm — twelve
// new, three changed, forty-one already the same — and then repeats the call
// without it. Two code paths agreeing with each other by hand is how a confirm
// dialog ends up describing something other than what happens.
//
// Existing keys are overwritten and unmentioned ones are left alone unless
// prune says otherwise, because the common paste is a handful of new values
// and a default that quietly emptied an environment would be the last time
// anybody used this.
var Import = api.Define(api.Spec[api.None, model.Import, model.ImportResult]{
	Name:  "Import Secrets",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Import]) (model.ImportResult, error) {
		result, err := store.Get().ImportSecrets(r.Body)
		if err != nil {
			return model.ImportResult{}, api.BadRequest(err.Error())
		}
		return result, nil
	},
})
