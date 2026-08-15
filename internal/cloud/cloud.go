// Package cloud is the vocabulary the provider pages speak.
//
// Guard borrows one credential per cloud account and shows three surfaces
// with it: the container registries the account owns, the object storage it
// has, and — where the provider has any — the machines it runs. Every one of
// those is somebody else's state, read live and stored nowhere, which is the
// rule that made a second provider cheap: what changes between Vultr and
// Cloudflare is which API answers, not what guard keeps.
//
// So this package holds the nouns and the verbs, and nothing that talks. The
// providers live one directory over (internal/vultr, internal/cloudflare) and
// implement as much of this as their API actually has. A provider that cannot
// do a thing does not implement the interface for it, and the capability the
// dashboard reads is derived from that — not declared beside it, where the two
// could disagree.
//
// Two things are deliberately absent from every type here:
//
//   - No secrets. The provider hands back docker logins and S3 secrets, and
//     those come out only through StorageKeys, one interface with one caller
//     that logs. Nothing a listing returns can carry one, because there is no
//     field for it.
//   - No provider name inside the objects. A Registry does not know whose it
//     is; the account row above it does. That keeps "which provider" a fact
//     about the account, which is where it is stored and where it is true.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Credentials is what one stored account is, at the moment it is used.
//
// The key is the secret; Account is the provider's own account id, which some
// APIs want in the path. Cloudflare does — every one of its endpoints hangs
// off /accounts/{id} — and Vultr does not, because the key alone names the
// account there. It is not a secret and it is not a guess: it is typed in
// beside the token, and the account it points at is the one the token opens.
type Credentials struct {
	Key     string
	Account string
	// S3 is the *data-plane* pair, for the providers whose objects can only
	// be reached with one guard stores itself. It is a different credential
	// from Key in kind, not just in value: Key asks what buckets exist, this
	// one reads what is inside them. Vultr leaves it empty — its account API
	// hands the pair back per subscription, so there is nothing to store.
	S3 Keys
}

// A Provider is one API guard knows how to talk to. Everything else is an
// optional interface on top of this one.
type Provider interface {
	// Describe is what the dashboard needs to draw the "add an account" form
	// and to label the account afterwards.
	Describe() Descriptor
	// Verify is the proof taken before a key is stored. It has to be a real
	// call — a key saved with a typo looks exactly like a key saved correctly,
	// and the difference should not be discovered by an empty page next week.
	Verify(ctx context.Context, creds Credentials) error
}

// Registries is the provider half that owns images. Every method takes the
// registry's id rather than a registry object, because a registry's docker
// credentials only exist inside the provider's own answer and must never take
// a detour through a caller.
type Registries interface {
	Registries(ctx context.Context, creds Credentials) ([]Registry, error)
	Repositories(ctx context.Context, creds Credentials, registry string) ([]Repository, error)
	Tags(ctx context.Context, creds Credentials, registry, repo string) ([]Tag, error)
	DeleteRepository(ctx context.Context, creds Credentials, registry, image string) error
	DeleteTag(ctx context.Context, creds Credentials, registry, repo, tag string) error
}

// RegistryMaker is the provider half that can add and remove registries.
//
// Not every provider has one to implement. Vultr's registry is a subscription
// with a name, a region and a plan, so it can be ordered and cancelled.
// Cloudflare's is a single managed registry that comes with the account — it
// cannot be created because it is already there, and it cannot be deleted
// because the account owns it. The dashboard reads that difference off the
// capability rather than off a provider name in a template.
type RegistryMaker interface {
	RegistryOptions(ctx context.Context, creds Credentials) (RegistryOptions, error)
	CreateRegistry(ctx context.Context, creds Credentials, spec RegistrySpec) (Registry, error)
	DeleteRegistry(ctx context.Context, creds Credentials, registry string) error
}

// Storages is the provider half that owns object storage: Vultr's S3
// subscriptions, Cloudflare's R2 buckets.
type Storages interface {
	Storages(ctx context.Context, creds Credentials) ([]Storage, error)
	StorageOptions(ctx context.Context, creds Credentials) (StorageOptions, error)
	CreateStorage(ctx context.Context, creds Credentials, spec StorageSpec) (Storage, error)
	DeleteStorage(ctx context.Context, creds Credentials, storage string) error
}

// StorageRenamer is a provider whose storage has a label separate from its
// identity. Vultr's subscription has one; an R2 bucket is its name, and
// renaming it is not a thing the API offers or a thing that would mean
// anything if it did.
type StorageRenamer interface {
	RenameStorage(ctx context.Context, creds Credentials, storage, label string) error
}

// StorageKeys is the one interface in this package that returns a secret.
//
// It exists because copying an access key and a secret into an application's
// configuration is the reason to have guard's storage page instead of another
// tab on the provider's console. Every implementation is reached from exactly
// one endpoint, which is admin and which logs that it happened.
//
// Cloudflare does not implement it: R2's S3 credentials are API tokens, minted
// in the dashboard's token screen and hashed into an access key, and an
// account token cannot mint another. So the R2 cards say where the endpoint is
// and stop, which is the truth rather than a button that fails.
type StorageKeys interface {
	StorageCredentials(ctx context.Context, creds Credentials, storage string) (Keys, error)
	RotateStorageKeys(ctx context.Context, creds Credentials, storage string) (Keys, error)
}

// StorageObjects is the provider half that can see inside a bucket.
//
// It is read-only on purpose, and that is a decision rather than an omission.
// Browsing is worth having — "what is actually in there" is otherwise a trip
// to the provider's console — but guard holding a credential that can delete
// somebody's objects would mean a guard session is a way to destroy their
// data. So there are three verbs: what buckets a storage holds, what is under
// one prefix, and a link to one object that expires.
//
// The link is the download. It is signed here and followed by the browser, so
// the bytes never pass through guard, and it stops working on its own.
type StorageObjects interface {
	// Containers is the buckets inside one storage, for providers whose
	// storage is a subscription holding several. An empty answer means the
	// storage is itself the bucket, which is what R2 is, and callers pass ""
	// as the container from then on.
	Containers(ctx context.Context, creds Credentials, storage string) ([]Container, error)
	// VerifyObjects proves whatever credential this half needs, before it is
	// stored. It is its own verb because Containers cannot be one: at a
	// provider whose storage is itself a bucket, Containers is correctly a
	// no-op, and a proof that always passes is not a proof.
	VerifyObjects(ctx context.Context, creds Credentials) error
	Objects(ctx context.Context, creds Credentials, ref ObjectRef) (Listing, error)
	ObjectLink(ctx context.Context, creds Credentials, ref ObjectRef, ttl time.Duration) (string, error)
}

// ObjectRef addresses one place inside one storage: a bucket, a prefix, and
// where a previous page stopped. Key is set only when one object is meant.
type ObjectRef struct {
	Storage   string `json:"storage"`
	Container string `json:"container,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Key       string `json:"key,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

// Container is one bucket inside a storage.
type Container struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created,omitempty"`
}

// Listing is one page of one prefix.
type Listing struct {
	Folders []string `json:"folders"`
	Objects []Object `json:"objects"`
	// Cursor is where the next page starts, empty when this was the last one.
	Cursor string `json:"cursor,omitempty"`
}

// Object is one stored object, as a row.
type Object struct {
	// Key is the whole key; Name is the last segment, which is what a folder
	// view shows.
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified,omitempty"`
	Class    string    `json:"class,omitempty"`
	ETag     string    `json:"etag,omitempty"`
}

// Descriptor is what the dashboard knows about a provider before any account
// exists: what to call it, what to call its secret, and whether the form needs
// a second box for the account id.
type Descriptor struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// KeyLabel is what the provider calls its own secret — "API key" at
	// Vultr, "API token" at Cloudflare. Using the provider's word is the
	// difference between finding the page it is minted on and not.
	KeyLabel string `json:"key_label"`
	KeyHint  string `json:"key_hint,omitempty"`
	// AccountLabel is empty for a provider whose key names its own account.
	// Non-empty means the form asks for it and Credentials.Account carries it.
	AccountLabel string `json:"account_label,omitempty"`
	AccountHint  string `json:"account_hint,omitempty"`
	// S3Label is non-empty for a provider that needs an S3 key pair stored to
	// browse objects — the data-plane credential its account key cannot mint.
	// The form asks for it as optional: an account without one still lists
	// buckets, creates them and deletes them, and simply cannot look inside.
	S3Label string `json:"s3_label,omitempty"`
	S3Hint  string `json:"s3_hint,omitempty"`
	// Capabilities is filled in by Describe on the registry, from what the
	// provider actually implements — never by the provider itself.
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities is what a provider can be asked to do. It is derived, not
// declared: each field is one interface assertion, so a provider that grows a
// method grows the button that calls it and cannot claim one it lacks.
type Capabilities struct {
	Registries    bool `json:"registries"`
	RegistryMaker bool `json:"registry_maker"`
	Storage       bool `json:"storage"`
	StorageRename bool `json:"storage_rename"`
	StorageKeys   bool `json:"storage_keys"`
	// StorageObjects says the provider can show what is inside a bucket.
	StorageObjects bool `json:"storage_objects"`
	// Compute is the one field a provider declares rather than proves. The
	// machines half of guard is Vultr-shaped — instances, snapshots, a power
	// switch — and lives in that package rather than behind an interface
	// here, because there is nothing yet to make it general against. What
	// this field is for is the account picker on the cluster page, which must
	// not offer an account that has no machines to link to.
	Compute        bool `json:"compute"`
	NeedsAccountID bool `json:"needs_account_id"`
}

// Registry is one container registry, as much of it as every provider can
// answer for. Storage figures are zero where the provider does not say.
type Registry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URN is the prefix an image is pulled from, which is the one string on
	// the card somebody actually copies.
	URN                 string    `json:"urn,omitempty"`
	Region              string    `json:"region,omitempty"`
	Public              bool      `json:"public"`
	Created             time.Time `json:"created"`
	StorageUsedBytes    int64     `json:"storage_used_bytes"`
	StorageAllowedBytes int64     `json:"storage_allowed_bytes"`
}

// Repository is one image line in a registry. Image is the opaque token the
// delete call takes; Name is for people, and the two are the same string at
// providers that have no separate token.
type Repository struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	Description   string `json:"description,omitempty"`
	AddedAt       string `json:"added_at,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	PullCount     int64  `json:"pull_count"`
	ArtifactCount int64  `json:"artifact_count"`
}

// Tag is one tag with what one manifest request could say about it. Size is
// zero for a multi-arch index, which names other manifests rather than layers.
type Tag struct {
	Name      string `json:"name"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

// Storage is one object storage: a Vultr subscription or an R2 bucket.
type Storage struct {
	// ID addresses it at the provider — a subscription id at Vultr, the
	// bucket name at Cloudflare, where the name is the address.
	ID    string `json:"id"`
	Label string `json:"label"`
	// Region is the provider's own word for where it lives, already
	// humanised: "Sydney" or "APAC", not a cluster number.
	Region string `json:"region,omitempty"`
	// Class is the storage class where the provider has more than one.
	Class    string    `json:"class,omitempty"`
	Status   string    `json:"status,omitempty"`
	Hostname string    `json:"s3_hostname,omitempty"`
	Created  time.Time `json:"created"`
	// HasKeys says a credential pair exists to be revealed. False at a
	// provider that does not hand them out, which is why the card draws
	// nothing rather than dots.
	HasKeys bool `json:"has_keys"`
	// UsedBytes and Objects are filled where the provider reports usage.
	UsedBytes int64 `json:"used_bytes,omitempty"`
	Objects   int64 `json:"objects,omitempty"`
}

// Keys is one S3 credential pair and where it is used.
type Keys struct {
	Access   string `json:"access_key"`
	Secret   string `json:"secret_key"`
	Hostname string `json:"s3_hostname,omitempty"`
}

// Region is one place a thing can be created, with whatever the provider
// charges for inside it.
type Region struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname,omitempty"`
	// Available is false for a region the provider still lists but has
	// stopped taking new subscriptions in. The picker greys it rather than
	// hiding it: "why is Sydney greyed" is a better question than "where did
	// Sydney go".
	Available bool `json:"available"`
}

// Tier is one price and one set of limits — a Vultr storage tier, a registry
// plan. Providers with a single flat offering return none.
type Tier struct {
	ID          string  `json:"id"`
	Name        string  `json:"name,omitempty"`
	Price       float64 `json:"price,omitempty"`
	StorageGB   int     `json:"storage_gb,omitempty"`
	BandwidthGB int     `json:"bandwidth_gb,omitempty"`
	// Region scopes a tier to one region, for providers that price per
	// region. Empty means it is offered everywhere.
	Region string `json:"region,omitempty"`
}

// RegistryOptions is what a create form has to offer.
type RegistryOptions struct {
	Regions []Region `json:"regions"`
	Plans   []Tier   `json:"plans"`
	// Classes is empty for registries; the field exists so the two options
	// shapes read the same way in the dashboard.
	Classes []string `json:"classes,omitempty"`
}

// StorageOptions is the same, for object storage.
type StorageOptions struct {
	Regions []Region `json:"regions"`
	Tiers   []Tier   `json:"tiers,omitempty"`
	Classes []string `json:"classes,omitempty"`
}

// RegistrySpec orders one registry.
type RegistrySpec struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Plan   string `json:"plan,omitempty"`
	Public bool   `json:"public,omitempty"`
}

// StorageSpec orders one object storage.
type StorageSpec struct {
	Label  string `json:"label"`
	Region string `json:"region"`
	Tier   string `json:"tier,omitempty"`
	Class  string `json:"class,omitempty"`
}

// ErrNotFound is a provider saying it has no such thing. Named because the
// endpoint layer turns it into a 404 and everything else into a 400: "that
// registry is gone" and "the provider is unhappy" are different sentences.
var ErrNotFound = errors.New("the provider has no such thing")

// Unsupported is what a caller gets for asking a provider to do something its
// API has no way to do. It carries both names because the sentence is only
// useful with them: "cloudflare cannot create registries".
func Unsupported(provider, what string) error {
	return fmt.Errorf("%s cannot %s", provider, what)
}

// providers is every provider guard knows how to talk to, keyed by the string
// stored in the account row. Registration is explicit, from one wiring file,
// rather than an init in each package: the set of providers guard supports is
// worth being able to read in one place.
var providers = map[string]Provider{}

// Register adds a provider. Called once per provider at startup; a duplicate
// id is a wiring mistake rather than a runtime condition, so it panics.
func Register(p Provider) {
	id := p.Describe().ID
	if id == "" {
		panic("cloud: a provider must have an id")
	}
	if _, taken := providers[id]; taken {
		panic("cloud: two providers claim the id " + id)
	}
	providers[id] = p
}

// For resolves the provider one account speaks to.
func For(id string) (Provider, error) {
	provider, ok := providers[strings.TrimSpace(id)]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", id)
	}
	return provider, nil
}

// Known says whether an id names a provider, which is what account validation
// asks — and the reason a new provider does not need a line in the model.
func Known(id string) bool {
	_, ok := providers[strings.TrimSpace(id)]
	return ok
}

// Describe fills in a provider's capabilities from what it implements.
func Describe(p Provider) Descriptor {
	described := p.Describe()
	_, registries := p.(Registries)
	_, maker := p.(RegistryMaker)
	_, storages := p.(Storages)
	_, renamer := p.(StorageRenamer)
	_, keys := p.(StorageKeys)
	_, objects := p.(StorageObjects)
	described.Capabilities = Capabilities{
		Registries:     registries,
		RegistryMaker:  maker,
		Storage:        storages,
		StorageRename:  renamer,
		StorageKeys:    keys,
		StorageObjects: objects,
		Compute:        described.Capabilities.Compute,
		NeedsAccountID: described.AccountLabel != "",
	}
	return described
}

// All is every registered provider, described, in a stable order — the list
// the "add an account" form is drawn from.
func All() []Descriptor {
	out := make([]Descriptor, 0, len(providers))
	for _, provider := range providers {
		out = append(out, Describe(provider))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Label < out[b].Label })
	return out
}
