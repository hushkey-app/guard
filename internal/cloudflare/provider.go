// The provider adapter: R2, said in the words the dashboard speaks.
//
// Cloudflare implements the storage half of the provider vocabulary and
// nothing else, and that is the whole point of the vocabulary being split
// into halves. There is no compute here — Cloudflare has no VPS to power
// cycle — so the cluster page never offers a Cloudflare account, and there
// are no registries yet, so the registries page does not draw a card for one.
// Neither of those is a special case anywhere: they are interfaces this type
// does not implement.
//
// The registry is the near miss worth writing down. Cloudflare does run a
// managed container registry, at registry.cloudflare.com, and it speaks the
// same OCI protocol guard already uses for tags and manifests — but reaching
// it needs a Workers Paid plan, and an account without one is refused at the
// credentials call. Adding it later means implementing cloud.Registries on
// this type. Nothing above it has to change.

package cloudflare

import (
	"context"
	"strings"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/s3"
)

type provider struct {
	client *Client
	// objects is the S3 client for what is inside a bucket. A second client
	// because it is a second protocol: the account API answers what buckets
	// exist, and only S3 answers what is in one.
	objects *s3.Client
}

// Provider adapts one client to the cloud interfaces.
func Provider(client *Client) cloud.Provider {
	return provider{client: client, objects: s3.New()}
}

// ProviderFor is the tests' door: the same adapter with a fake S3 behind it.
func ProviderFor(client *Client, objects *s3.Client) cloud.Provider {
	return provider{client: client, objects: objects}
}

func (p provider) Describe() cloud.Descriptor {
	return cloud.Descriptor{
		ID:           "cloudflare",
		Label:        "Cloudflare",
		KeyLabel:     "API token",
		KeyHint:      "An account API token with Workers R2 Storage read and write. User tokens work too; account-owned ones outlive the person who made them.",
		AccountLabel: "Account ID",
		AccountHint:  "The 32-character id on the account's overview page. Not a secret — every Cloudflare endpoint hangs off it.",
		S3Label:      "R2 access key",
		S3Hint:       "Optional. The S3 pair shown once when an R2 API token is created — without it guard lists buckets but cannot open one, because R2's objects are only reachable over S3.",
	}
}

// Verify proves a token by listing buckets: the narrowest read this account
// has, and one an account with no buckets still answers.
func (p provider) Verify(ctx context.Context, creds cloud.Credentials) error {
	if strings.TrimSpace(creds.Account) == "" {
		return cloud.Unsupported("cloudflare", "be used without an account id")
	}
	_, err := p.client.Buckets(ctx, creds)
	return err
}

func (p provider) Storages(ctx context.Context, creds cloud.Credentials) ([]cloud.Storage, error) {
	return p.client.Buckets(ctx, creds)
}

func (p provider) StorageOptions(ctx context.Context, creds cloud.Credentials) (cloud.StorageOptions, error) {
	// Neither list is account state, so neither costs a request. R2 has one
	// price everywhere, which is why there are no tiers to return.
	return cloud.StorageOptions{Regions: Locations(), Classes: Classes()}, nil
}

func (p provider) CreateStorage(ctx context.Context, creds cloud.Credentials, spec cloud.StorageSpec) (cloud.Storage, error) {
	return p.client.CreateBucket(ctx, creds, spec)
}

func (p provider) DeleteStorage(ctx context.Context, creds cloud.Credentials, storage string) error {
	return p.client.DeleteBucket(ctx, creds, storage)
}

// The storage half, and only the storage half. Renaming is absent because a
// bucket is its name; keys are absent because an account token cannot mint
// an S3 credential pair.
var _ cloud.Storages = provider{}
