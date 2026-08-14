package storage

import (
	"log/slog"

	"github.com/mirairoad/howl-go/core/api"
)

// Remove destroys one object storage and everything in it.
//
// There is no undo and no recycle bin: the objects go with the subscription,
// and anything still pointed at that endpoint starts failing immediately. The
// dashboard asks for the label to be typed first, the same confirmation
// deleting a repository takes, because a dialog with a yes button is a dialog
// people click without reading.
var Remove = api.Define(api.Spec[Target, api.None, api.None]{
	Name:  "Delete Object Storage",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Target, api.None]) (api.None, error) {
		storages, creds, err := open(r.Query)
		if err != nil {
			return api.None{}, err
		}
		err = storages.DeleteStorage(r.Context(), creds, r.Query.Storage)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		slog.Warn("object storage deleted",
			slog.Int64("account", r.Query.Account), slog.String("storage", r.Query.Storage),
			slog.String("result", outcome))
		if err != nil {
			return api.None{}, fail(err)
		}
		return api.None{}, nil
	},
})
