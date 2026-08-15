package views

import (
	"errors"
	"database/sql"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Update replaces a view. The path owns the identity, not the body — a PUT to
// /api/views/3 carrying id 7 is a mistake, and picking either one silently
// would edit the wrong panel.
var Update = api.Define(api.Spec[api.None, model.View, model.View]{
	Name:  "Update View",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.View]) (model.View, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.View{}, api.Invalid("id", "must be a number")
		}
		view := r.Body
		view.ID = id
		saved, err := store.Get().SaveView(view)
		if errors.Is(err, sql.ErrNoRows) {
			return model.View{}, api.NotFound("view not found")
		}
		if err != nil {
			return model.View{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
