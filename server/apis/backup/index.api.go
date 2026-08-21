package backup

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Summary is what a backup taken now would hold, section by section, and what
// it would leave behind. Drawn before anybody presses anything, because "export
// configuration" with no list under it is a button people press hoping.
var Summary = api.Define(api.Spec[api.None, api.None, model.BackupSummary]{
	Name:  "Backup Summary",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (model.BackupSummary, error) {
		return store.Get().BackupSummary()
	},
})
