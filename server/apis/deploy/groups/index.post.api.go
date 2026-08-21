package groups

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save creates a group or replaces one, membership and all.
//
// Editing a group changes nothing that already happened: a run stores the
// machines it touched and the group's name as it was, so adding a machine today
// does not make last month's deploy claim to have reached it.
var Save = api.Define(api.Spec[api.None, Group, model.DeployGroup]{
	Name:  "Save Deploy Group",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Group]) (model.DeployGroup, error) {
		saved, err := store.Get().SaveDeployGroup(model.DeployGroup{
			ID:        r.Body.ID,
			Name:      r.Body.Name,
			NodeIDs:   r.Body.NodeIDs,
			WebhookID: r.Body.WebhookID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return model.DeployGroup{}, api.NotFound("no group with that id")
		}
		if err != nil {
			return model.DeployGroup{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
