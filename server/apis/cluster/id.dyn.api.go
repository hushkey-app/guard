package cluster

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// One machine, by id — what the machine page reads.
//
// The list endpoint already answers with every machine, and a page about one of
// them could filter it. It does not, for the reason a page like this exists at
// all: /cluster/{id} is a URL somebody keeps open, links to and reloads, and a
// fleet of forty machines is forty times the payload, forty times the host-stat
// joins and forty rows of somebody else's tags to draw one.
//
// Public, exactly like the list. It carries what the list carries — status, the
// figures, the declared commands and how many environment variables there are —
// and none of the values behind them: an SSH password is `has_password`, and the
// environment is a count and two dates. Everything private about a machine is a
// separate, admin request.
var Read = api.Define(api.Spec[api.None, api.None, model.Node]{
	Name: "Cluster Node",
	Handler: func(r *api.Request[api.None, api.None]) (model.Node, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.Node{}, api.Invalid("id", "must be a number")
		}
		node, err := store.Get().Node(id)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("no machine with that id")
		}
		return node, err
	},
})
