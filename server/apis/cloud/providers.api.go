package cloud

import (
	"github.com/hushkey-app/guard/internal/cloud"
	"github.com/mirairoad/howl-go/core/api"
)

// Providers is what guard can talk to, and what each one can be asked for.
//
// The dashboard draws the "add an account" form from this — which providers
// exist, what each calls its secret, whether the form needs a second box for
// an account id — and then hides the buttons an account cannot answer for: no
// power switch on a Cloudflare machine that does not exist, no Reveal on a
// bucket whose keys are minted somewhere guard cannot reach.
//
// Every capability here is derived from what the provider package implements,
// so this endpoint cannot promise a button that fails.
var Providers = api.Define(api.Spec[api.None, api.None, []cloud.Descriptor]{
	Name: "Cloud Providers",
	Handler: func(r *api.Request[api.None, api.None]) ([]cloud.Descriptor, error) {
		return cloud.All(), nil
	},
})
