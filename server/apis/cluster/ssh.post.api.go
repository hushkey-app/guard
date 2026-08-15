package cluster

import (
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/mirairoad/howl-go/core/api"
)

// SSHCheck opens a session and asks the machine to identify itself.
//
// It exists because every other answer this page can give is ambiguous. A red
// dot means the health URL did not answer, which could be the service, the
// path, the firewall or the box. This runs `uname -sr; uptime` — it changes
// nothing, and what comes back is proof that guard can get in, from guard's
// network rather than from the browser's.
var SSHCheck = api.Define(api.Spec[api.None, contract.NodeRequest, model.Run]{
	Name:  "Check Cluster SSH",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.NodeRequest]) (model.Run, error) {
		return execute(r.Context(), r.Body.NodeID, remote.ProbeCommand)
	},
})
