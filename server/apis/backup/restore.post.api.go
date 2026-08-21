package backup

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Apply replaces this instance's configuration with the file's.
//
// Every refusal comes back as a 400 carrying its sentence, because every one of
// them is something the person at the keyboard can act on: the wrong passphrase,
// a file from a newer guard, a file that is not a backup at all. A restore that
// fails with "internal error" is a restore somebody retries five times.
//
// The store does everything that can fail before it writes anything, so a
// refusal here means the instance is exactly as it was.
var Apply = api.Define(api.Spec[api.None, contract.RestoreRequest, model.RestoreReport]{
	Name:  "Restore Configuration",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.RestoreRequest]) (model.RestoreReport, error) {
		report, err := store.Get().RestoreBackup(r.Body.Backup, r.Body.Passphrase)
		if err != nil {
			return model.RestoreReport{}, api.BadRequest(err.Error())
		}
		return report, nil
	},
})
