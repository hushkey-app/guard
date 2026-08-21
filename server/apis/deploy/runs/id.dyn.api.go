package runs

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Read is one run, with a row per machine — the thing the page polls while a
// deploy is going, and the record it reads afterwards.
var Read = api.Define(api.Spec[api.None, api.None, model.DeployRun]{
	Name:  "Deploy Run",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (model.DeployRun, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.DeployRun{}, api.Invalid("id", "must be a number")
		}
		run, err := store.Get().DeployRun(id)
		if errors.Is(err, sql.ErrNoRows) {
			return model.DeployRun{}, api.NotFound("no run with that id")
		}
		return run, err
	},
})
