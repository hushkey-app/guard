// Package storage is the endpoint layer for object storage.
//
// The same account key as everything else, and the same shape as the
// registries page: what exists is read live from the provider on open, and
// guard stores nothing about it. A bucket's label, its region and its
// endpoint are the provider's to answer.
//
// Two providers answer here now, and they do not offer the same things. A
// Vultr subscription has a label that can be edited and a credential pair
// that can be revealed and rotated; an R2 bucket is its own name and its S3
// credentials are minted on a screen guard cannot reach. Neither difference
// is written down as a provider name below — each is an interface a provider
// either implements or does not, asked for at the top of the handler that
// needs it, and reported to the dashboard as a capability so the button is
// never drawn in the first place.
//
// One thing here is unlike anything else in guard. Vultr returns the S3
// access key and secret on every read of a subscription — it is how the API
// is built — and those are credentials to somebody's data. So they never
// appear in a listing, they are unexported inside the provider package, and
// there is exactly one endpoint that returns them: Keys, which is admin, logs
// that it happened, and is the reason somebody can stop keeping the
// provider's console open in another tab.
package storage

import (
	"errors"

	"github.com/hushkey-app/guard/internal/cloud"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// Target names one storage under one account. Everything below the account
// level is addressed this way, because the account is what the key belongs to
// and the storage is what the provider's answers hang off.
type Target struct {
	Account int64  `query:"account" json:"account_id"`
	Storage string `query:"storage" json:"storage_id"`
}

func (t Target) Validate() error {
	if t.Account <= 0 {
		return errors.New("account must name a stored cloud account")
	}
	if t.Storage == "" {
		return errors.New("storage is required")
	}
	return nil
}

// open is the pair every call that names one storage starts with.
func open(t Target) (cloud.Storages, cloud.Credentials, error) {
	if err := t.Validate(); err != nil {
		return nil, cloud.Credentials{}, api.BadRequest(err.Error())
	}
	return storagesFor(t.Account)
}

// storagesFor is the same for the two calls that name only an account: the
// listing and the create form's options.
func storagesFor(accountID int64) (cloud.Storages, cloud.Credentials, error) {
	if accountID <= 0 {
		return nil, cloud.Credentials{}, api.Invalid("account", "must name a stored cloud account")
	}
	return apicloud.Storages(accountID)
}

// keysFor resolves the half of a provider that hands back S3 credentials. A
// provider without one is refused with the reason, which for Cloudflare is a
// real sentence rather than a shrug: an account token cannot mint an R2 key
// pair, so there is nothing to reveal and nothing to rotate.
func keysFor(t Target) (cloud.StorageKeys, cloud.Credentials, error) {
	provider, creds, err := apicloud.Open(t.Account)
	if err != nil {
		return nil, creds, err
	}
	keys, ok := provider.(cloud.StorageKeys)
	if !ok {
		return nil, creds, api.BadRequest(
			cloud.Unsupported(provider.Describe().Label, "hand out S3 credentials for its storage").Error())
	}
	return keys, creds, nil
}

// renamer resolves the half of a provider whose storage has a label separate
// from its identity.
func renamer(t Target) (cloud.StorageRenamer, cloud.Credentials, error) {
	provider, creds, err := apicloud.Open(t.Account)
	if err != nil {
		return nil, creds, err
	}
	rename, ok := provider.(cloud.StorageRenamer)
	if !ok {
		return nil, creds, api.BadRequest(
			cloud.Unsupported(provider.Describe().Label, "rename its storage").Error())
	}
	return rename, creds, nil
}

// fail is the shared translation of a provider error into a status.
func fail(err error) error { return apicloud.Fail(err) }
