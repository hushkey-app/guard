package accounts

import (
	"database/sql"
	"errors"
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/cloud"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// S3Request sets or clears one account's S3 pair.
type S3Request struct {
	AccountID int64  `json:"account_id"`
	Access    string `json:"s3_access_key"`
	Secret    string `json:"s3_secret_key"`
}

func (s S3Request) Validate() error {
	if s.AccountID <= 0 {
		return api.Invalid("account_id", "must name a stored cloud account")
	}
	// Half a pair signs nothing. Both empty is the other legal shape: it means
	// forget the pair, which is how browsing is turned off again.
	if (strings.TrimSpace(s.Access) == "") != (strings.TrimSpace(s.Secret) == "") {
		return api.Invalid("s3_access_key", "an access key and its secret are stored together or not at all")
	}
	return nil
}

// SetS3 stores the S3 credentials an account needs to open its buckets.
//
// This is the only part of a stored account that can be edited rather than
// re-created, and the asymmetry is deliberate. The API key is what the account
// *is*, so rotating it is delete-and-add and the proof can never be skipped.
// The S3 pair is a second credential for one feature — and requiring the
// account to be deleted to add one would mean unlinking every machine that
// points into it just to gain a Browse button.
//
// The proof survives: the pair has to read something before it is stored, the
// same rule the API key keeps. Sending an empty pair forgets the stored one,
// which is how browsing is switched back off.
var SetS3 = api.Define(api.Spec[api.None, S3Request, api.None]{
	Name:  "Set Account S3 Keys",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, S3Request]) (api.None, error) {
		access := strings.TrimSpace(r.Body.Access)
		secret := strings.TrimSpace(r.Body.Secret)
		if access != "" {
			provider, creds, err := apicloud.Open(r.Body.AccountID)
			if err != nil {
				return api.None{}, err
			}
			browser, ok := provider.(cloud.StorageObjects)
			if !ok {
				return api.None{}, api.BadRequest(
					cloud.Unsupported(provider.Describe().Label, "use an S3 key for anything").Error())
			}
			creds.S3 = cloud.Keys{Access: access, Secret: secret}
			if err := browser.VerifyObjects(r.Context(), creds); err != nil {
				return api.None{}, api.Invalid("s3_access_key", "the S3 credentials were refused: "+err.Error())
			}
		}
		var stored *string
		if secret != "" {
			stored = &secret
		}
		if err := store.Get().SetProviderS3(r.Body.AccountID, access, stored); errors.Is(err, sql.ErrNoRows) {
			return api.None{}, api.NotFound("no such cloud account")
		} else if err != nil {
			return api.None{}, err
		}
		slog.Info("cloud account s3 keys set",
			slog.Int64("account", r.Body.AccountID), slog.Bool("stored", access != ""))
		return api.None{}, nil
	},
})
