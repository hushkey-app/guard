// Package status is the one endpoint in guard that answers a stranger.
//
// Everything else behind /api requires a session or a token. This does not, by
// design, because a status page nobody can read without signing in is not a
// status page. That makes it the only place where getting the shape of a reply
// wrong is a disclosure rather than a bug, which is why the type it returns was
// written field by field in model.PublicStatus instead of filtered down from
// Node — a filtered shape publishes the next field somebody adds.
//
// Three things are true of it and are meant to stay true:
//
//   - It reads only machines with public = 1. Off is the default, so a machine
//     somebody adds at 3am is not on the internet by morning.
//   - It answers with the public name, never the machine's own. "Database"
//     rather than PACK-POSTGRES-VPS-MAIN, which would tell a stranger the
//     naming scheme, the provider and the shape of the fleet.
//   - It carries no address, no latency, no status code and no error text. The
//     error string on a failed check is "dial tcp 10.19.96.4:5432: connect:
//     connection refused", and publishing that is publishing the network.
package status

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Public is GET /api/status. No Roles: the absence is the feature, and it is
// listed in internal/auth's open paths for the same reason.
var Public = api.Define(api.Spec[api.None, api.None, model.PublicStatus]{
	Name: "Public Status",
	Handler: func(r *api.Request[api.None, api.None]) (model.PublicStatus, error) {
		return store.Get().PublicStatus()
	},
})
