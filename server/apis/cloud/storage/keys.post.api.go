package storage

import (
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// Credentials is an S3 login, on its way to somebody's clipboard.
//
// It exists as its own response type so that "this endpoint returns a secret"
// is visible in the signature of the one endpoint that does, rather than
// hiding as a populated field on a struct forty other handlers also return.
type Credentials struct {
	Hostname  string `json:"s3_hostname"`
	AccessKey string `json:"s3_access_key"`
	SecretKey string `json:"s3_secret_key"`
}

// Keys reveals one subscription's S3 credentials.
//
// Every other read in guard withholds secrets, and this one hands one over on
// purpose. The reason is that copying these two strings into an application's
// configuration is the whole job of this page — a dashboard that shows dots
// and nothing else just sends people back to the provider's console, which is
// the site this feature exists to stop opening.
//
// So the bargain is made explicit instead: admin only, never persisted, never
// part of a listing, fetched on a press rather than on a render, and logged
// every time — an answer to "who has these keys" that survives the tab.
var Keys = api.Define(api.Spec[api.None, Target, Credentials]{
	Name:  "Reveal Object Storage Keys",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Target]) (Credentials, error) {
		keys, creds, err := keysFor(r.Body)
		if err != nil {
			return Credentials{}, err
		}
		pair, err := keys.StorageCredentials(r.Context(), creds, r.Body.Storage)
		if err != nil {
			return Credentials{}, fail(err)
		}
		if pair.Access == "" {
			return Credentials{}, api.BadRequest("this storage has no credentials yet — it may still be provisioning")
		}
		slog.Info("object storage keys revealed",
			slog.Int64("account", r.Body.Account), slog.String("storage", r.Body.Storage))
		return Credentials{Hostname: pair.Hostname, AccessKey: pair.Access, SecretKey: pair.Secret}, nil
	},
})
