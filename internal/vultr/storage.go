// Object storage: the S3 buckets the same account key owns.
//
// The provider's listing hands back the S3 access key and secret in plain
// text on every read. That is convenient and it is exactly the thing this
// package exists to not do by accident, so both live in unexported fields
// and come out only through Credentials — one method, easy to grep, called
// from exactly one endpoint that says so in its name and logs the fact.
//
// What is deliberately absent: buckets and objects. Those are a different
// protocol (signed requests against the S3 hostname), and guessing at it
// here would put guard between somebody and their data for no gain.

package vultr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ObjectStorage is one storage subscription: a bucket endpoint, a region, a
// label, and a pair of keys that never leave this struct on their own.
type ObjectStorage struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	ClusterID int       `json:"cluster_id"`
	TierID    int       `json:"tier_id,omitempty"`
	Region    string    `json:"region,omitempty"`
	Status    string    `json:"status"`
	Hostname  string    `json:"s3_hostname"`
	Created   time.Time `json:"created"`

	// HasKeys says the provider gave us a credential pair for this
	// subscription — which it does once the thing finishes provisioning. The
	// page draws dots off this, and Reveal is what fills them in.
	HasKeys bool `json:"has_keys"`

	accessKey string
	secretKey string
}

// Credentials returns the S3 keys. Every caller of this is handing somebody
// their own storage credentials on purpose; there is one, and it logs.
func (o ObjectStorage) Credentials() (access, secret string) {
	return o.accessKey, o.secretKey
}

// Cluster is one region an object storage can be created in.
type Cluster struct {
	ID       int    `json:"id"`
	Region   string `json:"region"`
	Hostname string `json:"hostname"`
	// Deploy is "yes" while the provider is accepting new subscriptions here.
	// A region that has stopped taking them is still listed, and creating in
	// it fails — so the picker greys it rather than hiding it, because
	// "where did Sydney go" is a worse question than "why is it greyed".
	Deploy string `json:"deploy"`
}

// Tier is one price and one set of limits inside a cluster. Newer accounts
// must name a tier to create a subscription; older ones default.
type Tier struct {
	ID              int     `json:"id"`
	Price           float64 `json:"price"`
	RateLimitBytes  int64   `json:"ratelimit_ops_bytes,omitempty"`
	RateLimitOps    int     `json:"ratelimit_ops_secs,omitempty"`
	StorageGB       int     `json:"disk,omitempty"`
	BandwidthGB     int     `json:"bandwidth,omitempty"`
	SalesName       string  `json:"sales_name,omitempty"`
	SalesDescriptor string  `json:"slug,omitempty"`
}

type vultrObjectStorage struct {
	ID          string `json:"id"`
	DateCreated string `json:"date_created"`
	ClusterID   int    `json:"cluster_id"`
	TierID      int    `json:"tier_id"`
	Region      string `json:"region"`
	Label       string `json:"label"`
	Status      string `json:"status"`
	S3Hostname  string `json:"s3_hostname"`
	S3AccessKey string `json:"s3_access_key"`
	S3SecretKey string `json:"s3_secret_key"`
}

func (v vultrObjectStorage) storage() ObjectStorage {
	created, err := time.Parse(time.RFC3339, v.DateCreated)
	if err != nil {
		created, _ = time.Parse("2006-01-02 15:04:05", v.DateCreated)
	}
	return ObjectStorage{
		ID: v.ID, Label: v.Label, ClusterID: v.ClusterID, TierID: v.TierID,
		Region: v.Region, Status: v.Status, Hostname: v.S3Hostname, Created: created,
		HasKeys:   v.S3AccessKey != "",
		accessKey: v.S3AccessKey,
		secretKey: v.S3SecretKey,
	}
}

// ObjectStorages lists every storage subscription on the account.
func (c *Client) ObjectStorages(ctx context.Context, apiKey string) ([]ObjectStorage, error) {
	var out []ObjectStorage
	err := c.paged(ctx, apiKey, "/v2/object-storage", func(raw []byte) (string, error) {
		var page struct {
			ObjectStorages []vultrObjectStorage `json:"object_storages"`
			nextCursor
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", fmt.Errorf("vultr answered something unreadable: %w", err)
		}
		for _, v := range page.ObjectStorages {
			out = append(out, v.storage())
		}
		return page.Meta.Links.Next, nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Label) < strings.ToLower(out[b].Label)
	})
	return out, nil
}

// ObjectStorage reads one subscription — the call behind Reveal, and the one
// place the credentials are wanted rather than merely present.
func (c *Client) ObjectStorage(ctx context.Context, apiKey, id string) (ObjectStorage, error) {
	var answer struct {
		ObjectStorage vultrObjectStorage `json:"object_storage"`
	}
	address := c.base + "/v2/object-storage/" + url.PathEscape(id)
	if err := c.vultr(ctx, apiKey, http.MethodGet, address, &answer); err != nil {
		return ObjectStorage{}, err
	}
	return answer.ObjectStorage.storage(), nil
}

// ObjectStorageClusters lists the regions a subscription can live in.
func (c *Client) ObjectStorageClusters(ctx context.Context, apiKey string) ([]Cluster, error) {
	var out []Cluster
	err := c.paged(ctx, apiKey, "/v2/object-storage/clusters", func(raw []byte) (string, error) {
		var page struct {
			Clusters []Cluster `json:"clusters"`
			nextCursor
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", fmt.Errorf("vultr answered something unreadable: %w", err)
		}
		out = append(out, page.Clusters...)
		return page.Meta.Links.Next, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ObjectStorageTiers lists what one cluster offers. Accounts that predate
// tiers get an empty list and a create call without one, which is the same
// thing the provider's own console does.
func (c *Client) ObjectStorageTiers(ctx context.Context, apiKey string, clusterID int) ([]Tier, error) {
	var answer struct {
		Tiers []Tier `json:"tiers"`
	}
	address := fmt.Sprintf("%s/v2/object-storage/clusters/%d/tiers", c.base, clusterID)
	if err := c.vultr(ctx, apiKey, http.MethodGet, address, &answer); err != nil {
		return nil, err
	}
	return answer.Tiers, nil
}

// CreateObjectStorage orders one subscription. This one bills: it is a
// running cost from the moment the provider accepts it, which the endpoint
// above says out loud before asking.
func (c *Client) CreateObjectStorage(ctx context.Context, apiKey string, clusterID, tierID int, label string) (ObjectStorage, error) {
	body := map[string]any{"cluster_id": clusterID, "label": label}
	if tierID > 0 {
		body["tier_id"] = tierID
	}
	var answer struct {
		ObjectStorage vultrObjectStorage `json:"object_storage"`
	}
	if err := c.call(ctx, apiKey, http.MethodPost, c.base+"/v2/object-storage", body, &answer); err != nil {
		return ObjectStorage{}, err
	}
	return answer.ObjectStorage.storage(), nil
}

// LabelObjectStorage renames one subscription. The label is the only thing
// about it that can be edited, and the only thing that is guard's business
// to edit — the keys and the region are not opinions.
func (c *Client) LabelObjectStorage(ctx context.Context, apiKey, id, label string) error {
	address := c.base + "/v2/object-storage/" + url.PathEscape(id)
	return c.call(ctx, apiKey, http.MethodPut, address, map[string]string{"label": label}, nil)
}

// DeleteObjectStorage destroys one subscription and everything in it.
func (c *Client) DeleteObjectStorage(ctx context.Context, apiKey, id string) error {
	address := c.base + "/v2/object-storage/" + url.PathEscape(id)
	return c.vultr(ctx, apiKey, http.MethodDelete, address, nil)
}

// RegenerateObjectStorageKeys issues a new pair and invalidates the old one.
// Everything holding the previous secret stops working the moment this
// returns — which is the point of pressing it, and worth a sentence in the
// dialog rather than a surprise at deploy time.
func (c *Client) RegenerateObjectStorageKeys(ctx context.Context, apiKey, id string) (ObjectStorage, error) {
	var answer struct {
		S3Credentials struct {
			S3Hostname  string `json:"s3_hostname"`
			S3AccessKey string `json:"s3_access_key"`
			S3SecretKey string `json:"s3_secret_key"`
		} `json:"s3_credentials"`
	}
	address := c.base + "/v2/object-storage/" + url.PathEscape(id) + "/regenerate-keys"
	if err := c.call(ctx, apiKey, http.MethodPost, address, nil, &answer); err != nil {
		return ObjectStorage{}, err
	}
	return ObjectStorage{
		ID:        id,
		Hostname:  answer.S3Credentials.S3Hostname,
		HasKeys:   answer.S3Credentials.S3AccessKey != "",
		accessKey: answer.S3Credentials.S3AccessKey,
		secretKey: answer.S3Credentials.S3SecretKey,
	}, nil
}
