package registries

import (
	"log/slog"
	"strings"

	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// CreateRequest orders one registry on one stored account.
type CreateRequest struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Plan      string `json:"plan,omitempty"`
	Public    bool   `json:"public,omitempty"`
}

func (c CreateRequest) Validate() error {
	if c.AccountID <= 0 {
		return api.Invalid("account_id", "must name a stored cloud account")
	}
	if strings.TrimSpace(c.Name) == "" {
		return api.Invalid("name", "is required")
	}
	if len(c.Name) > 64 {
		return api.Invalid("name", "must be 64 characters or fewer")
	}
	if strings.TrimSpace(c.Region) == "" {
		return api.Invalid("region", "must name a region")
	}
	return nil
}

// Create orders a registry. This one bills: it is a running cost from the
// moment the provider accepts it, which the dialog above it says in those
// words rather than leaving to be discovered on an invoice.
//
// A public registry is one anybody can pull from without a credential. It is
// a real choice — a base image somebody publishes on purpose — and it is the
// one field here that cannot be changed afterwards from this page, so the
// dialog asks for it plainly rather than defaulting it out of sight.
var Create = api.Define(api.Spec[api.None, CreateRequest, cloud.Registry]{
	Name:  "Create Registry",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, CreateRequest]) (cloud.Registry, error) {
		made, creds, err := maker(r.Body.AccountID)
		if err != nil {
			return cloud.Registry{}, err
		}
		registry, err := made.CreateRegistry(r.Context(), creds, cloud.RegistrySpec{
			Name:   strings.TrimSpace(r.Body.Name),
			Region: r.Body.Region,
			Plan:   r.Body.Plan,
			Public: r.Body.Public,
		})
		if err != nil {
			return cloud.Registry{}, fail(err)
		}
		slog.Info("registry created",
			slog.Int64("account", r.Body.AccountID), slog.String("registry", registry.ID),
			slog.String("name", registry.Name), slog.String("region", r.Body.Region),
			slog.Bool("public", r.Body.Public))
		return registry, nil
	},
})
