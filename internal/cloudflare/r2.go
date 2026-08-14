// R2: the buckets on one account, and what each is holding.
//
// The listing is deliberately two calls deep. Cloudflare's list answers with
// a name and a creation date and nothing else — no location, no class, no
// size — so each bucket is read again for the rest. That is a fan-out against
// somebody's rate limit, which is why it is bounded, best-effort per bucket,
// and only ever runs when the storage page is opened or refreshed.
//
// What is deliberately absent is what is absent for the other provider too:
// objects. Those are a different protocol against the S3 hostname, and
// putting guard between somebody and their data buys nothing here.

package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/cloud"
)

// bucketWorkers bounds the per-bucket fan-out. An account with two hundred
// buckets should occupy a handful of connections, not two hundred.
const bucketWorkers = 8

// bucket is the account API's row.
type bucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date"`
	Location     string `json:"location"`
	StorageClass string `json:"storage_class"`
	Jurisdiction string `json:"jurisdiction"`
}

func (b bucket) storage(account string) cloud.Storage {
	created, _ := time.Parse(time.RFC3339, b.CreationDate)
	return cloud.Storage{
		ID:    b.Name,
		Label: b.Name,
		// The location is a hint code on the way in ("apac") and a region on
		// the way out ("APAC"). Both are Cloudflare's own words, so neither
		// is translated here.
		Region:   b.Location,
		Class:    b.StorageClass,
		Created:  created,
		Hostname: Endpoint(account),
		// R2's S3 credentials are API tokens, minted on Cloudflare's token
		// screen and hashed into an access key. An account token cannot mint
		// another, so there is no pair here to reveal and the card says so by
		// drawing nothing rather than dots over something that will not come.
		HasKeys: false,
	}
}

// Endpoint is the S3 host an account's buckets answer on. It is derived from
// the account id rather than read, because that is what it is.
func Endpoint(account string) string {
	if account == "" {
		return ""
	}
	return "https://" + account + ".r2.cloudflarestorage.com"
}

// Buckets lists every bucket on the account, each with its location, class
// and current usage.
func (c *Client) Buckets(ctx context.Context, creds cloud.Credentials) ([]cloud.Storage, error) {
	var names []bucket
	err := c.paged(ctx, creds, "/r2/buckets", func(raw json.RawMessage) error {
		var page struct {
			Buckets []bucket `json:"buckets"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("cloudflare answered something unreadable: %w", err)
		}
		names = append(names, page.Buckets...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Storage, len(names))
	var wg sync.WaitGroup
	slots := make(chan struct{}, bucketWorkers)
	for i, found := range names {
		out[i] = found.storage(creds.Account)
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			// Best effort per bucket: one that cannot be read leaves its name
			// and date on the page rather than sinking the whole listing.
			if detail, err := c.Bucket(ctx, creds, name); err == nil {
				out[i] = detail
			}
			if used, objects, err := c.usage(ctx, creds, name); err == nil {
				out[i].UsedBytes = used
				out[i].Objects = objects
			}
		}(i, found.Name)
	}
	wg.Wait()
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Label) < strings.ToLower(out[b].Label)
	})
	return out, nil
}

// Bucket reads one bucket, which is the only way to learn its location and
// storage class — the listing does not carry them.
func (c *Client) Bucket(ctx context.Context, creds cloud.Credentials, name string) (cloud.Storage, error) {
	var found bucket
	path := "/r2/buckets/" + url.PathEscape(name)
	if err := c.call(ctx, creds, http.MethodGet, path, nil, &found); err != nil {
		return cloud.Storage{}, err
	}
	if found.Name == "" {
		found.Name = name
	}
	return found.storage(creds.Account), nil
}

// usage is what the bucket is holding. Cloudflare answers the sizes as
// strings, so they are parsed rather than assumed.
func (c *Client) usage(ctx context.Context, creds cloud.Credentials, name string) (int64, int64, error) {
	var answer struct {
		PayloadSize string `json:"payloadSize"`
		ObjectCount string `json:"objectCount"`
	}
	path := "/r2/buckets/" + url.PathEscape(name) + "/usage"
	if err := c.call(ctx, creds, http.MethodGet, path, nil, &answer); err != nil {
		return 0, 0, err
	}
	used, _ := strconv.ParseInt(answer.PayloadSize, 10, 64)
	objects, _ := strconv.ParseInt(answer.ObjectCount, 10, 64)
	return used, objects, nil
}

// CreateBucket makes one bucket. The location is a hint rather than an
// instruction — Cloudflare places the data near where it is written and the
// hint only nudges that — which is why the picker says "hint" too.
func (c *Client) CreateBucket(ctx context.Context, creds cloud.Credentials, spec cloud.StorageSpec) (cloud.Storage, error) {
	body := map[string]any{"name": spec.Label}
	if spec.Region != "" {
		body["locationHint"] = spec.Region
	}
	if spec.Class != "" {
		body["storageClass"] = spec.Class
	}
	var created bucket
	if err := c.call(ctx, creds, http.MethodPost, "/r2/buckets", body, &created); err != nil {
		return cloud.Storage{}, err
	}
	if created.Name == "" {
		created.Name = spec.Label
	}
	return created.storage(creds.Account), nil
}

// DeleteBucket removes one bucket. Cloudflare refuses while objects are still
// in it, and its refusal says so, so nothing here tries to guess ahead of it.
func (c *Client) DeleteBucket(ctx context.Context, creds cloud.Credentials, name string) error {
	return c.call(ctx, creds, http.MethodDelete, "/r2/buckets/"+url.PathEscape(name), nil, nil)
}

// Locations are the hints R2 accepts, with the words a person picks between.
// A static list on purpose: it is part of the API's shape rather than account
// state, and asking the provider for it every time the form opens would be a
// request that can only ever come back with these six.
func Locations() []cloud.Region {
	return []cloud.Region{
		{ID: "", Name: "Automatic — nearest to first write", Available: true},
		{ID: "apac", Name: "APAC — Asia-Pacific", Available: true},
		{ID: "eeur", Name: "EEUR — Eastern Europe", Available: true},
		{ID: "enam", Name: "ENAM — Eastern North America", Available: true},
		{ID: "oc", Name: "OC — Oceania", Available: true},
		{ID: "weur", Name: "WEUR — Western Europe", Available: true},
		{ID: "wnam", Name: "WNAM — Western North America", Available: true},
	}
}

// Classes are what R2 charges by. Standard is the default; Infrequent Access
// is cheaper to keep and dearer to read, which is a decision about the data
// rather than about the bucket, so both are offered and neither is chosen.
func Classes() []string { return []string{"Standard", "InfrequentAccess"} }
