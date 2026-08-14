package registries

import (
	"errors"
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// RegistryQuery names one registry under one account. The name rides along
// so the log entry reads like something a person did.
type RegistryQuery struct {
	Account  int64  `query:"account"`
	Registry string `query:"registry"`
	Name     string `query:"name"`
}

func (q RegistryQuery) Validate() error {
	if q.Account <= 0 {
		return errors.New("account must name a stored cloud account")
	}
	if q.Registry == "" {
		return errors.New("registry is required")
	}
	return nil
}

// Remove cancels one registry and everything in it.
//
// This is the largest delete on these pages: every repository, every tag,
// every artifact, and the subscription behind them. There is no undo and no
// recycle bin, and anything still pulling from it starts failing immediately
// — which is why the dashboard asks for the registry's name to be typed, the
// same confirmation locking a machine takes, and why this is logged whatever
// happens.
var Remove = api.Define(api.Spec[RegistryQuery, api.None, api.None]{
	Name:  "Delete Registry",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[RegistryQuery, api.None]) (api.None, error) {
		made, creds, err := maker(r.Query.Account)
		if err != nil {
			return api.None{}, err
		}
		err = made.DeleteRegistry(r.Context(), creds, r.Query.Registry)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Warn("registry deleted",
			slog.Int64("account", r.Query.Account), slog.String("registry", r.Query.Registry),
			slog.String("name", r.Query.Name), slog.String("result", outcome))
		if err != nil {
			return api.None{}, fail(err)
		}
		return api.None{}, nil
	},
})
