// What is inside a Vultr object storage.
//
// The shape is one level deeper here than at Cloudflare, and that is the
// product rather than an inconsistency: a Vultr subscription is an S3 endpoint
// holding as many buckets as somebody makes, so a storage on the page opens
// into buckets and a bucket opens into prefixes. R2's storage *is* a bucket, so
// it opens straight into prefixes. `Containers` is what says which.
//
// Nothing is stored to make this work. The account API returns the
// subscription's S3 pair on every read, so the credentials for the objects are
// fetched at the moment they are used and never written down — the same pair
// Reveal hands over, taken the same way, and not logged here because listing a
// folder is not the same act as copying a key into a configuration file.

package vultr

import (
	"context"
	"path"
	"time"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/s3"
)

// s3Region is what a Vultr signature is scoped to. The provider takes the
// region from the hostname and ignores this, but a signature still has to
// name one, and every S3 client sends this when it has nothing better.
const s3Region = "us-east-1"

func (p provider) Containers(ctx context.Context, creds cloud.Credentials, storage string) ([]cloud.Container, error) {
	cfg, keys, err := p.s3For(ctx, creds, storage)
	if err != nil {
		return nil, err
	}
	buckets, err := p.objects.Buckets(ctx, cfg, keys)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Container, 0, len(buckets))
	for _, bucket := range buckets {
		out = append(out, cloud.Container{Name: bucket.Name, Created: bucket.Created})
	}
	return out, nil
}

// VerifyObjects has nothing to prove here. This provider stores no S3 pair —
// it reads one per subscription at the moment it is used — so the only
// credential involved is the account key, which Verify already proved.
func (p provider) VerifyObjects(ctx context.Context, creds cloud.Credentials) error { return nil }

func (p provider) Objects(ctx context.Context, creds cloud.Credentials, ref cloud.ObjectRef) (cloud.Listing, error) {
	cfg, keys, err := p.s3For(ctx, creds, ref.Storage)
	if err != nil {
		return cloud.Listing{}, err
	}
	listing, err := p.objects.List(ctx, cfg, keys, ref.Container, ref.Prefix, ref.Cursor)
	if err != nil {
		return cloud.Listing{}, err
	}
	return objectListing(listing), nil
}

func (p provider) ObjectLink(ctx context.Context, creds cloud.Credentials, ref cloud.ObjectRef, ttl time.Duration) (string, error) {
	cfg, keys, err := p.s3For(ctx, creds, ref.Storage)
	if err != nil {
		return "", err
	}
	return p.objects.Link(cfg, keys, ref.Container, ref.Key, ttl, time.Now())
}

// s3For reads one subscription and takes its endpoint and its credentials
// straight out of the answer, so neither is ever stored.
func (p provider) s3For(ctx context.Context, creds cloud.Credentials, storage string) (s3.Config, s3.Keys, error) {
	found, err := p.client.ObjectStorage(ctx, creds.Key, storage)
	if err != nil {
		return s3.Config{}, s3.Keys{}, err
	}
	access, secret := found.Credentials()
	host := found.Hostname
	if host == "" {
		return s3.Config{}, s3.Keys{}, cloud.ErrNotFound
	}
	return s3.Config{Endpoint: "https://" + host, Region: s3Region},
		s3.Keys{Access: access, Secret: secret}, nil
}

// objectListing is the translation into the vocabulary's shape.
func objectListing(listing s3.Listing) cloud.Listing {
	out := cloud.Listing{
		Folders: listing.Folders,
		Objects: make([]cloud.Object, 0, len(listing.Objects)),
		Cursor:  listing.Cursor,
	}
	if out.Folders == nil {
		out.Folders = []string{}
	}
	for _, object := range listing.Objects {
		out.Objects = append(out.Objects, cloud.Object{
			Key: object.Key, Name: path.Base(object.Key), Size: object.Size,
			Modified: object.Modified, Class: object.Class, ETag: object.ETag,
		})
	}
	return out
}

var _ cloud.StorageObjects = provider{}
