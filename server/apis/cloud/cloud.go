// Package cloud is the endpoint layer for the accounts guard borrows.
//
// Guard stores one thing per account: an API key, sealed, proved before it is
// stored. Everything the dashboard shows with it — registries, instances,
// object storage — is read live from the provider, so nothing here can go
// stale and nothing but the key needs deleting.
//
// The account is shared on purpose. It is one key at the provider, and asking
// for it once per feature would mean the same secret stored three times and
// revoked once. So this package owns the accounts, the providers and the
// clients, and the cluster, registries and storage endpoints borrow them:
// Open is the single door from an account id to the API behind it, and every
// request the provider sees leaves from the server.
//
// There is more than one provider now, which changed exactly one thing about
// that door. It returns the provider beside the credentials, and what a
// caller may do with it is an interface assertion — a registries endpoint
// asks for cloud.Registries and gets a plain "cloudflare cannot do that" if
// the account cannot. Nothing here switches on a provider name.
package cloud

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/cloudflare"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Client is the Vultr client the compute endpoints use directly. The machines
// half of guard is Vultr-shaped — instances, snapshots, a power switch — and
// has nothing yet to be made general against, so it holds this rather than an
// interface. Everything that is general goes through Open.
var Client = vultr.New()

// The providers guard knows how to talk to, wired up in one place on purpose:
// the set of APIs this binary can reach is worth being able to read at a
// glance. Registration is what makes a provider selectable in the account
// form and usable by every endpoint below.
func init() {
	cloud.Register(vultr.Provider(Client))
	cloud.Register(cloudflare.Provider(cloudflare.New()))
}

// Open resolves one stored account into the provider it speaks to and the
// credentials to speak with. It is the only way to reach either.
//
// A key that will not open is not a server fault, and answering 500 to it
// sends somebody looking for a bug in guard. It means the ciphertext and the
// secret key no longer match — nearly always because GUARD_SECRET_KEY changed
// or the key file beside the database did not travel with it — and the fix is
// to add the account again, which the sentence says.
func Open(accountID int64) (cloud.Provider, cloud.Credentials, error) {
	account, err := store.Get().ProviderAccount(accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, cloud.Credentials{}, api.NotFound("no such cloud account")
	}
	if err != nil {
		return nil, cloud.Credentials{}, err
	}
	provider, err := cloud.For(account.Provider)
	if err != nil {
		return nil, cloud.Credentials{}, api.BadRequest(err.Error())
	}
	key, err := KeyFor(accountID)
	if err != nil {
		return nil, cloud.Credentials{}, err
	}
	creds := cloud.Credentials{Key: key, Account: account.ExternalID}
	// The data-plane pair, where one is stored. Absent is normal — most
	// accounts have none — and the provider says what that costs, which is
	// only ever "this bucket cannot be opened".
	access, secret, err := store.Get().ProviderS3For(accountID)
	if err == nil {
		creds.S3 = cloud.Keys{Access: access, Secret: secret}
	}
	return provider, creds, nil
}

// Browser resolves an account to the half of a provider that can see inside a
// bucket. Read-only by construction: the interface has no verb that changes
// anything, so no endpoint above it can.
func Browser(accountID int64) (cloud.StorageObjects, cloud.Credentials, error) {
	provider, creds, err := Open(accountID)
	if err != nil {
		return nil, creds, err
	}
	browser, ok := provider.(cloud.StorageObjects)
	if !ok {
		return nil, creds, api.BadRequest(cloud.Unsupported(provider.Describe().Label, "show what is inside its storage").Error())
	}
	return browser, creds, nil
}

// KeyFor opens one account's stored key. Kept beside Open because the compute
// endpoints hold the Vultr client directly and need only this half.
func KeyFor(accountID int64) (string, error) {
	key, err := store.Get().ProviderKeyFor(accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", api.NotFound("no such cloud account")
	}
	if err != nil {
		return "", api.BadRequest("the stored key could not be opened — it was sealed with a different GUARD_SECRET_KEY. Remove this account and add it again.")
	}
	return key, nil
}

// VultrKeyFor is KeyFor for the endpoints that can only mean Vultr: the power
// switch, the snapshots, the instance listing. An account at a provider with
// no machines is refused here rather than at a URL that would otherwise be
// built out of an empty account id.
func VultrKeyFor(accountID int64) (string, error) {
	account, err := store.Get().ProviderAccount(accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", api.NotFound("no such cloud account")
	}
	if err != nil {
		return "", err
	}
	if account.Provider != model.ProviderVultr {
		return "", api.BadRequest("machines are a Vultr account's half of guard — this account is at " + account.Provider)
	}
	return KeyFor(accountID)
}

// Registries resolves an account to the half of a provider that owns images.
func Registries(accountID int64) (cloud.Registries, cloud.Credentials, error) {
	provider, creds, err := Open(accountID)
	if err != nil {
		return nil, creds, err
	}
	registries, ok := provider.(cloud.Registries)
	if !ok {
		return nil, creds, api.BadRequest(cloud.Unsupported(provider.Describe().Label, "show registries").Error())
	}
	return registries, creds, nil
}

// Storages resolves an account to the half of a provider that owns object
// storage.
func Storages(accountID int64) (cloud.Storages, cloud.Credentials, error) {
	provider, creds, err := Open(accountID)
	if err != nil {
		return nil, creds, err
	}
	storages, ok := provider.(cloud.Storages)
	if !ok {
		return nil, creds, api.BadRequest(cloud.Unsupported(provider.Describe().Label, "show object storage").Error())
	}
	return storages, creds, nil
}

// Fail turns a provider error into the status it deserves. "That instance is
// gone" and "the provider is unhappy" are different sentences, and a 400 for
// the first one sends people looking for a bug in guard.
func Fail(err error) error {
	if errors.Is(err, cloud.ErrNotFound) {
		return api.NotFound("the provider has no such thing — it may have been deleted")
	}
	return api.BadRequest(err.Error())
}

// Verify proves a key before it is stored, by asking the provider it claims
// to be for. It is the same rule the SSH logins keep: a key saved with a typo
// looks exactly like a key saved correctly, and the difference should not be
// discovered by an empty page next week.
func Verify(ctx context.Context, account model.ProviderAccount, key string) error {
	provider, err := cloud.For(account.Provider)
	if err != nil {
		return api.BadRequest(err.Error())
	}
	described := provider.Describe()
	if described.AccountLabel != "" && account.ExternalID == "" {
		return api.Invalid("external_id", fmt.Sprintf("%s needs the account's %s", described.Label, described.AccountLabel))
	}
	creds := cloud.Credentials{Key: key, Account: account.ExternalID}
	if account.S3Secret != nil && *account.S3Secret != "" {
		creds.S3 = cloud.Keys{Access: account.S3Access, Secret: *account.S3Secret}
	}
	if err := provider.Verify(ctx, creds); err != nil {
		return api.BadRequest(err.Error())
	}
	// The S3 pair is a second credential and gets a second proof, for the same
	// reason as the first: a secret saved with a typo looks exactly like one
	// saved correctly, and finding out means an empty bucket next week.
	if creds.S3.Access != "" {
		browser, ok := provider.(cloud.StorageObjects)
		if !ok {
			return api.Invalid("s3_access_key", described.Label+" has nothing to use an S3 key for")
		}
		if err := browser.VerifyObjects(ctx, creds); err != nil {
			return api.Invalid("s3_access_key", "the S3 credentials were refused: "+err.Error())
		}
	}
	return nil
}
