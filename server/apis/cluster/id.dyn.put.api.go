package cluster

import (
	"database/sql"
	"errors"
	"strconv"

	"github.com/mirairoad/guard/internal/telemetry/model"
	"github.com/mirairoad/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Update renames a node, repoints it, or pauses it. The path owns the identity,
// not the body.
var Update = api.Define(api.Spec[api.None, model.Node, model.Node]{
	Name:  "Update Cluster Node",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, model.Node]) (model.Node, error) {
		id, err := strconv.ParseInt(r.Param("id"), 10, 64)
		if err != nil || id <= 0 {
			return model.Node{}, api.Invalid("id", "must be a number")
		}
		node := r.Body
		node.ID = id
		saved, err := store.Get().SaveNode(node)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("node not found")
		}
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		return saved, nil
	},
})
