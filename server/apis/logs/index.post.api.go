package logs

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Write ingests a single log line.
var Write = api.Define(api.Spec[api.None, contract.LogInput, contract.Accepted]{
	Name:  "Write Log",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, contract.LogInput]) (contract.Accepted, error) {
		in := r.Body
		return contract.Accepted{}, store.Get().Add(model.Event{
			Signal: "logs", Timestamp: in.Timestamp, Service: in.Service, Instance: in.Instance,
			Severity: in.Severity, Message: in.Message, TraceID: in.TraceID, SpanID: in.SpanID,
			Attributes: in.Attributes,
		})
	},
})
