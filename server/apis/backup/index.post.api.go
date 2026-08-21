package backup

import (
	"log/slog"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Export answers with the file. The browser saves it; guard keeps no copy —
// there is nowhere for one to live that is not the disk this is a backup of.
//
// It is logged either way, and the log says whether the credentials went with
// it: an export is the one request that can put every secret guard holds into
// somebody's downloads folder.
var Export = api.Define(api.Spec[api.None, contract.BackupRequest, model.Backup]{
	Name:  "Export Configuration",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.BackupRequest]) (model.Backup, error) {
		doc, err := store.Get().ExportBackup(r.Body.Passphrase)
		if err != nil {
			return model.Backup{}, err
		}
		slog.Info("configuration exported",
			slog.String("secrets", doc.Secrets), slog.Int("tables", len(doc.Tables)))
		return doc, nil
	},
})
