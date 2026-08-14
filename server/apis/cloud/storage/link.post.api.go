package storage

import (
	"log/slog"
	"strings"
	"time"

	"github.com/hushkey-app/guard/internal/cloud"
	apicloud "github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// LinkRequest names one object to download.
type LinkRequest struct {
	AccountID int64  `json:"account_id"`
	Storage   string `json:"storage_id"`
	Container string `json:"container,omitempty"`
	Key       string `json:"key"`
}

func (l LinkRequest) Validate() error {
	if l.AccountID <= 0 {
		return api.Invalid("account_id", "must name a stored cloud account")
	}
	if l.Storage == "" {
		return api.Invalid("storage_id", "is required")
	}
	if strings.TrimSpace(l.Key) == "" {
		return api.Invalid("key", "is required")
	}
	return nil
}

// Download is a signed URL and when it stops working. The expiry is in the
// answer because the page shows it: a link that quietly died is worse than one
// that said when it would.
type Download struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// linkTTL is how long a download link lives. Long enough to click and for a
// large file to finish starting; short enough that a link left in a chat
// window is not a way in tomorrow.
const linkTTL = 5 * time.Minute

// Link is one signed URL for one object.
//
// It is the same bargain as revealing an S3 key, in smaller print. The link
// reads one object, from whoever holds it, until it expires — so it is admin,
// it names the object in the log, and it is minted on a press rather than
// handed out with the listing. Every row on the page having a live download
// URL in the markup would be a page that leaks by being open.
//
// The bytes do not pass through guard. The browser fetches them from the
// storage directly, which keeps a 40 GB object from becoming guard's problem.
var Link = api.Define(api.Spec[api.None, LinkRequest, Download]{
	Name:  "Link Storage Object",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, LinkRequest]) (Download, error) {
		browser, creds, err := apicloud.Browser(r.Body.AccountID)
		if err != nil {
			return Download{}, err
		}
		url, err := browser.ObjectLink(r.Context(), creds, cloud.ObjectRef{
			Storage:   r.Body.Storage,
			Container: r.Body.Container,
			Key:       r.Body.Key,
		}, linkTTL)
		if err != nil {
			return Download{}, fail(err)
		}
		slog.Info("storage object link issued",
			slog.Int64("account", r.Body.AccountID), slog.String("storage", r.Body.Storage),
			slog.String("container", r.Body.Container), slog.String("key", r.Body.Key),
			slog.Duration("expires_in", linkTTL))
		return Download{URL: url, ExpiresAt: time.Now().UTC().Add(linkTTL)}, nil
	},
})
