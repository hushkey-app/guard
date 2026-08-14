package registries

import (
	"errors"
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// TagDeleteQuery addresses one tag.
type TagDeleteQuery struct {
	Account  int64  `query:"account"`
	Registry string `query:"registry"`
	Repo     string `query:"repo"`
	Tag      string `query:"tag"`
}

func (q TagDeleteQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored registry account")
	}
	if q.Registry == "" {
		return errors.New("registry is required")
	}
	if q.Repo == "" {
		return errors.New("repo is required")
	}
	if q.Tag == "" {
		return errors.New("tag is required")
	}
	return nil
}

// DeleteTag removes the manifest behind one tag. The registry API deletes
// by digest — that is the only delete it has — so every tag pointing at the
// same digest disappears with it. The dashboard says so before asking for
// confirmation, because "v1.2-rc" and "v1.2" are often the same bytes.
var DeleteTag = api.Define(api.Spec[TagDeleteQuery, api.None, api.None]{
	Name:  "Delete Registry Tag",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[TagDeleteQuery, api.None]) (api.None, error) {
		registries, creds, err := open(r.Query.Account)
		if err != nil {
			return api.None{}, err
		}
		err = registries.DeleteTag(r.Context(), creds, r.Query.Registry, r.Query.Repo, r.Query.Tag)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Info("registry tag delete",
			slog.Int64("account", r.Query.Account), slog.String("registry", r.Query.Registry),
			slog.String("repository", r.Query.Repo), slog.String("tag", r.Query.Tag),
			slog.String("result", outcome))
		if err != nil {
			return api.None{}, fail(err)
		}
		return api.None{}, nil
	},
})
