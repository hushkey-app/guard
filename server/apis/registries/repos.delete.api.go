package registries

import (
	"errors"
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// RepoDeleteQuery addresses one repository by the opaque image token the
// provider handed out in the listing — that token, not the name, is what
// the delete endpoint takes. The name rides along so the log entry reads
// like something a person did.
type RepoDeleteQuery struct {
	Account  int64  `query:"account"`
	Registry string `query:"registry"`
	Image    string `query:"image"`
	Name     string `query:"name"`
}

func (q RepoDeleteQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored registry account")
	}
	if q.Registry == "" {
		return errors.New("registry is required")
	}
	if q.Image == "" {
		return errors.New("image is required")
	}
	return nil
}

// DeleteRepo removes one repository — every tag and artifact in it —
// through the provider's API. Logged whatever happens: this is the part of
// guard that destroys somebody's images, and the log is where that fact
// outlives the browser tab it was done from.
var DeleteRepo = api.Define(api.Spec[RepoDeleteQuery, api.None, api.None]{
	Name:  "Delete Registry Repository",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[RepoDeleteQuery, api.None]) (api.None, error) {
		registries, creds, err := open(r.Query.Account)
		if err != nil {
			return api.None{}, err
		}
		err = registries.DeleteRepository(r.Context(), creds, r.Query.Registry, r.Query.Image)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Info("registry repository delete",
			slog.Int64("account", r.Query.Account), slog.String("registry", r.Query.Registry),
			slog.String("repository", r.Query.Name), slog.String("result", outcome))
		if err != nil {
			return api.None{}, fail(err)
		}
		return api.None{}, nil
	},
})
