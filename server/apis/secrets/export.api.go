package secrets

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Export is one environment as .env text — the other half of a paste.
//
// Formatted on the server rather than assembled in the browser, so the text
// somebody copies out is byte-for-byte what an import would read back in: one
// escaping rule, in one place, with a test that round-trips it. A page that
// built the file with join("\n") would work until the first value with a
// newline in it.
type Export struct {
	Env  string `json:"env"`
	Text string `json:"text"`
}

var ExportEnv = api.Define(api.Spec[EnvQuery, api.None, Export]{
	Name:  "Export Secrets",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[EnvQuery, api.None]) (Export, error) {
		env, err := store.Get().Env(r.Query.Env)
		if err != nil {
			return Export{}, api.NotFound("no such environment")
		}
		values, err := store.Get().Secrets(env.ID)
		if err != nil {
			return Export{}, err
		}
		return Export{Env: env.Name, Text: model.FormatEnv(values)}, nil
	},
})
