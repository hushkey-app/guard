package storage

import (
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// Regenerate issues a new S3 key pair and invalidates the old one.
//
// Everything holding the previous secret stops working the moment this
// returns — the deploy, the backup job, the uploader somebody wrote in 2023.
// That is the point of pressing it, and it is worth a sentence in the dialog
// rather than a surprise at the next deploy.
//
// The new pair comes back here, once, because a rotation nobody can read is a
// rotation that breaks everything and helps nothing. It is the same bargain
// as Keys: admin, unstored, logged.
var Regenerate = api.Define(api.Spec[api.None, Target, Credentials]{
	Name:  "Regenerate Object Storage Keys",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Target]) (Credentials, error) {
		keys, creds, err := keysFor(r.Body)
		if err != nil {
			return Credentials{}, err
		}
		pair, err := keys.RotateStorageKeys(r.Context(), creds, r.Body.Storage)
		if err != nil {
			return Credentials{}, fail(err)
		}
		slog.Warn("object storage keys regenerated",
			slog.Int64("account", r.Body.Account), slog.String("storage", r.Body.Storage))
		return Credentials{Hostname: pair.Hostname, AccessKey: pair.Access, SecretKey: pair.Secret}, nil
	},
})
