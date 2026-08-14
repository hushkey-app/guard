package cluster

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Update renames a node, repoints it, or pauses it. The path owns the identity,
// not the body.
//
// A login that changed is checked before it is stored, the same as on the way
// in. Everything else — the name, the cadence, pausing — is saved without
// dialling anything: an SSH round trip on every rename would make the settings
// page feel broken for edits that have nothing to do with SSH.
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

		current, err := store.Get().Node(id)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("node not found")
		}
		if err != nil {
			return model.Node{}, err
		}

		pin := ""
		address := strings.TrimSpace(node.SSHAddress)
		switch {
		case address == "":
			// The login was removed, or was never there. Nothing to prove.
		case node.Password != nil && strings.TrimSpace(*node.Password) == "":
			// Explicitly forgotten. Also nothing to prove.
		case node.Password != nil || address != strings.TrimSpace(current.SSHAddress):
			if err := node.Validate(); err != nil {
				return model.Node{}, api.BadRequest(err.Error())
			}
			password := ""
			if node.Password != nil {
				password = *node.Password
			} else if login, err := store.Get().SSHLoginFor(id); err == nil {
				// The address moved but the password did not: check the one
				// already stored against the new host rather than making
				// somebody retype a password they cannot see.
				password = login.Password
			}
			// The pin belongs to the old address; a new host presents a key
			// guard has never seen, and refusing it here would be refusing the
			// move itself.
			if address != strings.TrimSpace(current.SSHAddress) {
				node.SSHFingerprint = ""
			} else {
				node.SSHFingerprint = current.SSHFingerprint
			}
			if pin, err = verifySSH(r.Context(), node, password); err != nil {
				return model.Node{}, err
			}
		}

		saved, err := store.Get().SaveNode(node)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Node{}, api.NotFound("node not found")
		}
		if err != nil {
			return model.Node{}, api.BadRequest(err.Error())
		}
		// After the save, because saving a moved address clears the old pin —
		// this is the key the connection that authorised the move presented.
		if pin != "" {
			if err := store.Get().PinFingerprint(id, pin); err != nil {
				return model.Node{}, err
			}
			saved.SSHFingerprint = pin
		}
		// A cadence edited from every hour to every three seconds should not
		// wait out the hour to take effect.
		wake()
		return saved, nil
	},
})
