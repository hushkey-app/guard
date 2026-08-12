package views

import (
	"database/sql"
	"errors"

	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/contract"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Data runs a saved view and returns its Frame.
//
// A GET, deliberately: the dashboard refreshes every panel on a timer, and
// mw.Coalesce only shares identical concurrent GETs. Two tabs watching the same
// wall board therefore cost one query rather than two.
var Data = api.Define(api.Spec[contract.ViewDataQuery, api.None, model.Frame]{
	Name: "View Data",
	Handler: func(r *api.Request[contract.ViewDataQuery, api.None]) (model.Frame, error) {
		view, err := store.Get().View(r.Query.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Frame{}, api.NotFound("view not found")
		}
		if err != nil {
			return model.Frame{}, err
		}
		frame, err := store.Get().RunView(view.Panel, r.Query.Apply(view.Query))
		if err != nil {
			// The view is stored, so a compile failure here is guard's problem,
			// not the caller's — but it is also the only place the author will
			// ever see it, so the message travels.
			return model.Frame{}, api.BadRequest(err.Error())
		}
		return frame, nil
	},
})
