package webhooks

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save adds a destination or edits one.
//
// Editable in place, unlike a cloud account, because there is nothing here to
// prove: an endpoint that refuses today may be behind a queue that is down,
// and guard is in no position to decide when somebody's URL is allowed to
// exist. The token follows the same pointer rule as an SSH password — absent
// leaves it alone, empty forgets it, a value replaces it — so renaming a
// destination cannot silently drop its credential.
var Save = api.Define(api.Spec[api.None, model.Webhook, model.Webhook]{
	Name:  "Save Event Destination",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Webhook]) (model.Webhook, error) {
		saved, err := store.Get().SaveWebhook(r.Body)
		if err != nil {
			return model.Webhook{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
