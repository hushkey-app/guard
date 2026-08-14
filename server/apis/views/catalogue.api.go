package views

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/contract"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Catalogue is what the builder populates its controls from: the panels this
// binary renders, the aggregations it implements, and the fields this instance
// has seen. Reading it from the running binary rather than hardcoding a copy in
// the JavaScript is the point — a panel the server cannot compile can never
// appear in the picker.
var Catalogue = api.Define(api.Spec[api.None, api.None, contract.Catalogue]{
	Name: "View Catalogue",
	Handler: func(r *api.Request[api.None, api.None]) (contract.Catalogue, error) {
		fields, err := store.Get().FieldCatalog()
		if err != nil {
			return contract.Catalogue{}, err
		}
		return contract.Catalogue{
			Panels:       model.Panels,
			Aggregations: model.Aggregations,
			Fields:       fields,
		}, nil
	},
})
