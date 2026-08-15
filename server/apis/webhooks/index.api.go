// Package webhooks is where events go: the named destinations every watcher in
// guard delivers to.
//
// One list, reused. A machine rule, a stale backup and (next) a saved view all
// point at a row here rather than each carrying a URL, so a destination is
// typed once and revoked once — and so "send the database alerts somewhere
// else" is one edit rather than a hunt through every rule.
package webhooks

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// List returns the destinations, without their tokens. The token never comes
// back — the page draws dots, exactly as it does for a machine's password.
var List = api.Define(api.Spec[api.None, api.None, []model.Webhook]{
	Name:  "Event Destinations",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) ([]model.Webhook, error) {
		return store.Get().Webhooks()
	},
})
