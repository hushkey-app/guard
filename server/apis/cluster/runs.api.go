package cluster

import (
	"errors"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// RunQuery names whose history to read: one machine's, or one command's.
type RunQuery struct {
	Node   int64 `query:"node"`
	Action int64 `query:"action"`
	Limit  int   `query:"limit"`
}

func (q RunQuery) Validate() error {
	if q.Node <= 0 && q.Action <= 0 {
		return errors.New("name a machine or an action")
	}
	return nil
}

// Runs is what a scheduled command did the last fifty times.
//
// A machine's by default, because that is the question somebody standing in
// front of a card has — "what has been running on this box" — and asking it per
// command would be one request per button.
//
// Admin, like running one. The rows carry the tail of each command's output,
// which is the machine talking, and a `docker logs` kept for a day is not a
// thing to hand to a reader who may not press the buttons that produced it.
var Runs = api.Define(api.Spec[RunQuery, api.None, []model.Run]{
	Name:  "Cluster Runs",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[RunQuery, api.None]) ([]model.Run, error) {
		return store.Get().Runs(r.Query.Node, r.Query.Action, r.Query.Limit)
	},
})
