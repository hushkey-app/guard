// The provider adapter: this package's own vocabulary, said in the words the
// dashboard speaks.
//
// Everything below is a translation and nothing below decides anything. The
// calls, the pagination and the docker token flow live in the files beside
// this one; what this adds is the shape internal/cloud asks for, so that the
// endpoint layer can hold a Vultr account and a Cloudflare account in the same
// variable.
//
// One thing is worth saying out loud. The cloud interfaces address a registry
// by its id, and Vultr's tag calls need the registry's docker credentials —
// which exist only inside the account API's answer. So the adapter looks the
// registry up again on the way through rather than letting a caller hand one
// in. That is a second HTTP call on a tag listing, and it is the price of the
// rule that a credential never takes a detour through a caller.

package vultr

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/s3"
)

// provider is the adapter. It holds the client rather than embedding it, so
// the wide Client API stays out of the interface guard's endpoints see.
type provider struct {
	client *Client
	// objects is the S3 client for what is inside a bucket — a second
	// protocol, and the only part of this provider the account API cannot
	// answer for.
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
		ID:       "vultr",
		Label:    "Vultr",
		KeyLabel: "API key",
		KeyHint:  "Account → API in the Vultr customer portal. The key must be allowed from this server's address.",
		// One key names one account at Vultr, so there is nothing else to ask
		// for — and the compute half of guard is this provider's.
		Capabilities: cloud.Capabilities{Compute: true},
	}
}

// Verify proves a key by listing registries: the narrowest read the account
// API has, and one that says the key is real without needing the account to
// own anything in particular.
func (p provider) Verify(ctx context.Context, creds cloud.Credentials) error {
	_, err := p.client.Registries(ctx, creds.Key)
	return err
}

func (p provider) Registries(ctx context.Context, creds cloud.Credentials) ([]cloud.Registry, error) {
	found, err := p.client.Registries(ctx, creds.Key)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Registry, 0, len(found))
	for _, registry := range found {
		out = append(out, cloud.Registry{
			ID: registry.ID, Name: registry.Name, URN: registry.URN,
			Region: registry.Region, Public: registry.Public, Created: registry.Created,
			StorageUsedBytes:    registry.StorageUsedBytes,
			StorageAllowedBytes: registry.StorageAllowedBytes,
		})
	}
	return out, nil
}

func (p provider) Repositories(ctx context.Context, creds cloud.Credentials, registry string) ([]cloud.Repository, error) {
	found, err := p.client.Repositories(ctx, creds.Key, registry)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Repository, 0, len(found))
	for _, repo := range found {
		out = append(out, cloud.Repository{
			Name: repo.Name, Image: repo.Image, Description: repo.Description,
			AddedAt: repo.AddedAt, UpdatedAt: repo.UpdatedAt,
			PullCount: repo.PullCount, ArtifactCount: repo.ArtifactCount,
		})
	}
	return out, nil
}

func (p provider) Tags(ctx context.Context, creds cloud.Credentials, registry, repo string) ([]cloud.Tag, error) {
	found, err := p.resolve(ctx, creds.Key, registry)
	if err != nil {
		return nil, err
	}
	tags, err := p.client.Tags(ctx, found, repo)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Tag, 0, len(tags))
	for _, tag := range tags {
		out = append(out, cloud.Tag{Name: tag.Name, Digest: tag.Digest, SizeBytes: tag.SizeBytes})
	}
	return out, nil
}

func (p provider) DeleteRepository(ctx context.Context, creds cloud.Credentials, registry, image string) error {
	return p.client.DeleteRepository(ctx, creds.Key, registry, image)
}

func (p provider) DeleteTag(ctx context.Context, creds cloud.Credentials, registry, repo, tag string) error {
	found, err := p.resolve(ctx, creds.Key, registry)
	if err != nil {
		return err
	}
	return p.client.DeleteTag(ctx, found, repo, tag)
}

// resolve finds one registry under one key, which is the only way to reach
// its docker credentials.
func (p provider) resolve(ctx context.Context, apiKey, registryID string) (Registry, error) {
	list, err := p.client.Registries(ctx, apiKey)
	if err != nil {
		return Registry{}, err
	}
	for _, registry := range list {
		if registry.ID == registryID {
			return registry, nil
		}
	}
	return Registry{}, cloud.ErrNotFound
}

func (p provider) RegistryOptions(ctx context.Context, creds cloud.Credentials) (cloud.RegistryOptions, error) {
	regions, err := p.client.RegistryRegions(ctx, creds.Key)
	if err != nil {
		return cloud.RegistryOptions{}, err
	}
	plans, err := p.client.RegistryPlans(ctx, creds.Key)
	if err != nil {
		return cloud.RegistryOptions{}, err
	}
	options := cloud.RegistryOptions{
		Regions: make([]cloud.Region, 0, len(regions)),
		Plans:   make([]cloud.Tier, 0, len(plans)),
	}
	for _, region := range regions {
		// The provider's region name is a code ("sjc"); the data centre says
		// where that is. Both, because the code is what a URN carries and the
		// city is what a person is choosing between.
		name := region.Name
		if region.City != "" {
			name = fmt.Sprintf("%s — %s", region.Name, region.City)
		}
		options.Regions = append(options.Regions, cloud.Region{
			ID: region.Name, Name: name, Hostname: region.BaseURL, Available: true,
		})
	}
	for _, plan := range plans {
		options.Plans = append(options.Plans, cloud.Tier{
			ID: plan.Name, Name: plan.VanityName, Price: plan.MonthlyPrice,
			StorageGB: plan.MaxStorageMB / 1024,
		})
	}
	return options, nil
}

func (p provider) CreateRegistry(ctx context.Context, creds cloud.Credentials, spec cloud.RegistrySpec) (cloud.Registry, error) {
	created, err := p.client.CreateRegistry(ctx, creds.Key, spec.Name, spec.Region, spec.Plan, spec.Public)
	if err != nil {
		return cloud.Registry{}, err
	}
	return cloud.Registry{
		ID: created.ID, Name: created.Name, URN: created.URN, Region: created.Region,
		Public: created.Public, Created: created.Created,
		StorageUsedBytes:    created.StorageUsedBytes,
		StorageAllowedBytes: created.StorageAllowedBytes,
	}, nil
}

func (p provider) DeleteRegistry(ctx context.Context, creds cloud.Credentials, registry string) error {
	return p.client.DeleteRegistry(ctx, creds.Key, registry)
}

func (p provider) Storages(ctx context.Context, creds cloud.Credentials) ([]cloud.Storage, error) {
	found, err := p.client.ObjectStorages(ctx, creds.Key)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Storage, 0, len(found))
	for _, storage := range found {
		out = append(out, storage.storage())
	}
	return out, nil
}

// storage is the one translation with a decision in it: the cluster id is
// this provider's address for a region, and it becomes the region id the
// dashboard sends back on create. The human name comes from the region field
// the listing carries.
func (o ObjectStorage) storage() cloud.Storage {
	return cloud.Storage{
		ID: o.ID, Label: o.Label, Region: o.Region, Status: o.Status,
		Hostname: o.Hostname, Created: o.Created, HasKeys: o.HasKeys,
	}
}

func (p provider) StorageOptions(ctx context.Context, creds cloud.Credentials) (cloud.StorageOptions, error) {
	clusters, err := p.client.ObjectStorageClusters(ctx, creds.Key)
	if err != nil {
		return cloud.StorageOptions{}, err
	}
	options := cloud.StorageOptions{Regions: make([]cloud.Region, 0, len(clusters))}
	for _, cluster := range clusters {
		options.Regions = append(options.Regions, cloud.Region{
			ID:        strconv.Itoa(cluster.ID),
			Name:      cluster.Region,
			Hostname:  cluster.Hostname,
			Available: cluster.Deploy == "yes",
		})
		// Tiers are per region here, and an account old enough to predate
		// them gets none — which is not an error, it is an account that
		// creates without naming one.
		tiers, err := p.client.ObjectStorageTiers(ctx, creds.Key, cluster.ID)
		if err != nil {
			continue
		}
		for _, tier := range tiers {
			name := tier.SalesName
			if name == "" {
				name = tier.SalesDescriptor
			}
			options.Tiers = append(options.Tiers, cloud.Tier{
				ID: strconv.Itoa(tier.ID), Name: name, Price: tier.Price,
				StorageGB: tier.StorageGB, BandwidthGB: tier.BandwidthGB,
				Region: strconv.Itoa(cluster.ID),
			})
		}
	}
	return options, nil
}

func (p provider) CreateStorage(ctx context.Context, creds cloud.Credentials, spec cloud.StorageSpec) (cloud.Storage, error) {
	cluster, err := strconv.Atoi(spec.Region)
	if err != nil || cluster <= 0 {
		return cloud.Storage{}, errors.New("that is not a Vultr storage region")
	}
	tier := 0
	if spec.Tier != "" {
		if tier, err = strconv.Atoi(spec.Tier); err != nil {
			return cloud.Storage{}, errors.New("that is not a Vultr storage tier")
		}
	}
	created, err := p.client.CreateObjectStorage(ctx, creds.Key, cluster, tier, spec.Label)
	if err != nil {
		return cloud.Storage{}, err
	}
	return created.storage(), nil
}

func (p provider) DeleteStorage(ctx context.Context, creds cloud.Credentials, storage string) error {
	return p.client.DeleteObjectStorage(ctx, creds.Key, storage)
}

func (p provider) RenameStorage(ctx context.Context, creds cloud.Credentials, storage, label string) error {
	return p.client.LabelObjectStorage(ctx, creds.Key, storage, label)
}

func (p provider) StorageCredentials(ctx context.Context, creds cloud.Credentials, storage string) (cloud.Keys, error) {
	found, err := p.client.ObjectStorage(ctx, creds.Key, storage)
	if err != nil {
		return cloud.Keys{}, err
	}
	access, secret := found.Credentials()
	return cloud.Keys{Access: access, Secret: secret, Hostname: found.Hostname}, nil
}

func (p provider) RotateStorageKeys(ctx context.Context, creds cloud.Credentials, storage string) (cloud.Keys, error) {
	rotated, err := p.client.RegenerateObjectStorageKeys(ctx, creds.Key, storage)
	if err != nil {
		return cloud.Keys{}, err
	}
	access, secret := rotated.Credentials()
	return cloud.Keys{Access: access, Secret: secret, Hostname: rotated.Hostname}, nil
}

// The adapter implements every optional half of the provider vocabulary; the
// assertions are here so that removing a method is a compile error rather
// than a button that silently stops existing.
var (
	_ cloud.Registries     = provider{}
	_ cloud.RegistryMaker  = provider{}
	_ cloud.Storages       = provider{}
	_ cloud.StorageRenamer = provider{}
	_ cloud.StorageKeys    = provider{}
	_ cloud.StorageObjects = provider{}
)
