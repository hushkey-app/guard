package analytics

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Health is what the tracker is sending that guard is not storing.
//
// Takes no window on purpose. The counters are the process's, kept since it
// started rather than per day, because the question they answer — "is anything
// being dropped" — is not one a range control should be able to make look
// better. The one figure that does carry a time is the last event received,
// and it is guard's clock.
//
// No role: it names nothing and counts what guard refused, so it is the same
// read the rules list is. Enabled comes from the door having been mounted, so
// an instance with no GUARD_RUM_ORIGINS answers `false` here rather than
// leaving the page to guess from an absence of rows.
var Health = api.Define(api.Spec[api.None, api.None, model.AnalyticsHealth]{
	Name: "Analytics Health",
	Handler: func(r *api.Request[api.None, api.None]) (model.AnalyticsHealth, error) {
		return store.Get().AnalyticsHealth()
	},
})
