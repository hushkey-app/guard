// What is inside an R2 bucket.
//
// Cloudflare's account API stops at the bucket: it will say a bucket exists,
// what it holds in total and where it lives, and nothing about the keys in it.
// Objects are S3, so this is the account's stored S3 pair signing requests
// against the account's own r2.cloudflarestorage.com — which is why an account
// without that pair lists and creates buckets perfectly well and cannot open
// one, and why the card says so instead of offering a button.
//
// "auto" is R2's region. It is not a placeholder: signing with anything else
// is refused, because the region is part of the credential scope rather than
// part of the address.

package cloudflare

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/hushkey-app/guard/internal/s3"
)

// R2Region is the region every R2 signature is scoped to.
const R2Region = "auto"

// Containers is empty for R2, and that is the answer rather than a gap: an R2
// bucket *is* the storage, so there is no level in between to list.
func (p provider) Containers(ctx context.Context, creds cloud.Credentials, storage string) ([]cloud.Container, error) {
	return nil, nil
}

// VerifyObjects proves the stored pair by listing the account's buckets over
// S3 — the narrowest thing the data-plane credential can be asked, and one an
// account with no buckets still answers.
func (p provider) VerifyObjects(ctx context.Context, creds cloud.Credentials) error {
	cfg, keys, err := p.s3For(creds)
	if err != nil {
		return err
	}
	_, err = p.objects.Buckets(ctx, cfg, keys)
	return err
}

func (p provider) Objects(ctx context.Context, creds cloud.Credentials, ref cloud.ObjectRef) (cloud.Listing, error) {
	cfg, keys, err := p.s3For(creds)
	if err != nil {
		return cloud.Listing{}, err
	}
	listing, err := p.objects.List(ctx, cfg, keys, ref.Storage, ref.Prefix, ref.Cursor)
	if err != nil {
		return cloud.Listing{}, err
	}
	return asListing(listing), nil
}

func (p provider) ObjectLink(ctx context.Context, creds cloud.Credentials, ref cloud.ObjectRef, ttl time.Duration) (string, error) {
	cfg, keys, err := p.s3For(creds)
	if err != nil {
		return "", err
	}
	return p.objects.Link(cfg, keys, ref.Storage, ref.Key, ttl, time.Now())
}

// s3For is where the two halves of a Cloudflare account meet: the account id
// decides the hostname, and the stored pair signs for it.
func (p provider) s3For(creds cloud.Credentials) (s3.Config, s3.Keys, error) {
	if strings.TrimSpace(creds.S3.Access) == "" || strings.TrimSpace(creds.S3.Secret) == "" {
		return s3.Config{}, s3.Keys{}, cloud.Unsupported("cloudflare",
			"open a bucket without a stored R2 access key — add the account again with one")
	}
	cfg := s3.Config{Endpoint: Endpoint(creds.Account), Region: R2Region}
	return cfg, s3.Keys{Access: creds.S3.Access, Secret: creds.S3.Secret}, nil
}

// asListing is the same translation both providers do, kept here because it is
// the only shared shape between internal/s3 and the vocabulary.
func asListing(listing s3.Listing) cloud.Listing {
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
