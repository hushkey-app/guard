package templates

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Read is one template at one version — what a run's record links to, so that
// "what did we actually deploy" is a link rather than an archaeology exercise.
var Read = api.Define(api.Spec[Query, api.None, model.DeployTemplate]{
	Name:  "Compose Template",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[Query, api.None]) (model.DeployTemplate, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.DeployTemplate{}, api.Invalid("id", "must be a number")
		}
		template, err := store.Get().DeployTemplate(id, r.Query.Version)
		if errors.Is(err, sql.ErrNoRows) {
			return model.DeployTemplate{}, api.NotFound("no template with that id and version")
		}
		return template, err
	},
})
