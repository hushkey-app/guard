// Package traces serves one whole request at a time.
//
// The events endpoints answer "which spans match this filter"; this one answers
// "what happened during this request", which is a different question with a
// different shape — a tree, flattened depth-first, with each span's offset from
// the start of the trace already worked out. That is what a waterfall draws,
// and working it out here means the browser never has to hold the whole trace
// twice to figure out where a bar goes.
package traces

import (
	"database/sql"
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

var ByID = api.Define(api.Spec[api.None, api.None, model.Trace]{
	Name: "Trace",
	Handler: func(r *api.Request[api.None, api.None]) (model.Trace, error) {
		trace, err := store.Get().Trace(r.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return model.Trace{}, api.NotFound("no spans for this trace")
		}
		if err != nil {
			return model.Trace{}, api.BadRequest(err.Error())
		}
		return trace, nil
	},
})
