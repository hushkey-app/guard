package templates

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save writes a new template, or the next version of one. Never an update: the
// row a past run points at is the answer to "what did we deploy", and an edit
// that rewrote it would make every record describe today's file.
var Save = api.Define(api.Spec[api.None, Template, model.DeployTemplate]{
	Name:  "Save Compose Template",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Template]) (model.DeployTemplate, error) {
		saved, err := store.Get().SaveDeployTemplate(model.DeployTemplate{
			ID:          r.Body.ID,
			Name:        r.Body.Name,
			ServiceName: r.Body.ServiceName,
			Image:       r.Body.Image,
			Path:        r.Body.Path,
			ComposeYAML: r.Body.ComposeYAML,
			HealthPath:  r.Body.HealthPath,
			HealthPort:  r.Body.HealthPort,
			SecretEnvID: r.Body.SecretEnvID,
			Vars:        r.Body.Vars,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return model.DeployTemplate{}, api.NotFound("no template with that id")
		}
		if err != nil {
			return model.DeployTemplate{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
