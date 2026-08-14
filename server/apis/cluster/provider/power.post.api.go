package provider

import (
	"log/slog"

	"github.com/hushkey-app/guard/internal/vultr"
	"github.com/hushkey-app/guard/server/apis/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// PowerRequest is one switch on one machine.
type PowerRequest struct {
	NodeID int64  `json:"node_id"`
	Action string `json:"action"`
}

// Power flips a machine at the provider: start, halt, reboot.
//
// This is not the stored command "sudo reboot", and the difference is the
// reason both exist. That one asks the operating system politely over SSH and
// needs the machine to be answering; this one is the power cable, and works
// on a box that stopped answering anything an hour ago. Which is exactly when
// somebody wants it.
//
// halt is a stop, not a delete: the instance keeps its address, its disk and
// its bill. Nothing in this package destroys an instance — that is a thing to
// do in the provider's own console, deliberately, once.
var Power = api.Define(api.Spec[api.None, PowerRequest, api.None]{
	Name:  "Machine Power",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, PowerRequest]) (api.None, error) {
		link, key, err := target(r.Body.NodeID, true)
		if err != nil {
			return api.None{}, err
		}
		action := r.Body.Action
		err = cloud.Client.Power(r.Context(), key, link.InstanceID, action)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
		}
		// Logged whether it worked or not: "who power-cycled the database box"
		// is a question that gets asked after the fact, and the browser tab it
		// was pressed in does not outlive this line.
		slog.Info("machine power",
			slog.Int64("node", link.NodeID), slog.String("instance", link.InstanceID),
			slog.String("action", action), slog.String("result", outcome))
		if err != nil {
			return api.None{}, cloud.Fail(err)
		}
		return api.None{}, nil
	},
})

// Validate keeps the action one of three. It is pasted into a URL path, so
// the list is closed rather than checked for shape.
func (p PowerRequest) Validate() error {
	if p.NodeID <= 0 {
		return api.Invalid("node_id", "must name a machine")
	}
	for _, candidate := range vultr.PowerActions {
		if candidate == p.Action {
			return nil
		}
	}
	return api.Invalid("action", "must be start, halt or reboot")
}
