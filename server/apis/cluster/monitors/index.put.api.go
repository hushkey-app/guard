package monitors

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Save adds a rule or edits one.
//
// An edited rule forgets where it stood: a threshold that just moved from 90 to
// 95 is a different question, and carrying the old "already told them" flag
// across would answer the new question with the old one's silence.
var Save = api.Define(api.Spec[api.None, model.Monitor, model.Monitor]{
	Name:  "Save Cluster Monitor",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Monitor]) (model.Monitor, error) {
		saved, err := store.Get().SaveMonitor(r.Body)
		if err != nil {
			return model.Monitor{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
