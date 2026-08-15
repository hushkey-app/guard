package storage

import (
	"log/slog"
	"strings"

	"github.com/mirairoad/howl-go/core/api"
)

// LabelRequest renames one subscription.
type LabelRequest struct {
	Target
	Label string `json:"label"`
}

func (l LabelRequest) Validate() error {
	if err := l.Target.Validate(); err != nil {
		return api.BadRequest(err.Error())
	}
	if strings.TrimSpace(l.Label) == "" {
		return api.Invalid("label", "is required")
	}
	if len(l.Label) > 255 {
		return api.Invalid("label", "must be 255 characters or fewer")
	}
	return nil
}

// Label renames one subscription — the only thing about it that is an opinion
// rather than a fact, and so the only thing here that can be edited. The
// region, the endpoint and the keys are not preferences.
var Label = api.Define(api.Spec[api.None, LabelRequest, api.None]{
	Name:  "Rename Object Storage",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, LabelRequest]) (api.None, error) {
		rename, creds, err := renamer(r.Body.Target)
		if err != nil {
			return api.None{}, err
		}
		if err := rename.RenameStorage(r.Context(), creds, r.Body.Storage, strings.TrimSpace(r.Body.Label)); err != nil {
			return api.None{}, fail(err)
		}
		slog.Info("object storage renamed",
			slog.Int64("account", r.Body.Account), slog.String("storage", r.Body.Storage),
			slog.String("label", r.Body.Label))
		return api.None{}, nil
	},
})
