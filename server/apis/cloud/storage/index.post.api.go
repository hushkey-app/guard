package storage

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// CreateRequest orders one object storage.
//
// Region, tier and class are all the provider's own strings, taken from
// Options and handed back untouched. Nothing here parses them: what a region
// id means is the provider package's business, and a cluster number that this
// layer understood would be a second place to be wrong.
type CreateRequest struct {
	AccountID int64  `json:"account_id"`
	Region    string `json:"region"`
	Tier      string `json:"tier,omitempty"`
	Class     string `json:"class,omitempty"`
	Label     string `json:"label"`
}

func (c CreateRequest) Validate() error {
	if c.AccountID <= 0 {
		return api.Invalid("account_id", "must name a stored cloud account")
	}
	if strings.TrimSpace(c.Label) == "" {
		return api.Invalid("label", "is required")
	}
	if len(c.Label) > 255 {
		return api.Invalid("label", "must be 255 characters or fewer")
	}
	return nil
}

// Create orders one object storage. This one bills: it is a running cost from
// the moment the provider accepts it, which the dialog above it says in those
// words rather than leaving to be discovered on an invoice.
//
// The keys the provider mints with it are not in the answer. Where there are
// any they arrive a moment later anyway — a fresh subscription provisions for
// a few seconds — and Keys is where credentials come from, on purpose and
// with a log line.
var Create = api.Define(api.Spec[api.None, CreateRequest, cloud.Storage]{
	Name:  "Create Object Storage",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, CreateRequest]) (cloud.Storage, error) {
		storages, creds, err := storagesFor(r.Body.AccountID)
		if err != nil {
			return cloud.Storage{}, err
		}
		storage, err := storages.CreateStorage(r.Context(), creds, cloud.StorageSpec{
			Label:  strings.TrimSpace(r.Body.Label),
			Region: r.Body.Region,
			Tier:   r.Body.Tier,
			Class:  r.Body.Class,
		})
		if err != nil {
			return cloud.Storage{}, fail(err)
		}
		slog.Info("object storage created",
			slog.Int64("account", r.Body.AccountID), slog.String("storage", storage.ID),
			slog.String("label", storage.Label), slog.String("region", r.Body.Region))
		return storage, nil
	},
})
