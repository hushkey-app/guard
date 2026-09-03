package secrets

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// CompareQuery names the environments to lay side by side, in the order they
// should be drawn — `?envs=3,1,7`.
//
// A comma-separated string rather than a repeated parameter because the query
// decoder takes scalars, and because the order is part of the request: the
// columns are drawn in the order they were asked for, so "staging then
// production" and "production then staging" are two different tables and
// somebody reading one of them meant one of them.
type CompareQuery struct {
	Envs string `query:"envs"`
}

func (q CompareQuery) Validate() error {
	_, err := q.ids()
	return err
}

// ids parses the list, and Validate is one call to it — so the 400 somebody
// gets and the ids the handler reads can never be decided differently.
func (q CompareQuery) ids() ([]int64, error) {
	parts := strings.Split(q.Envs, ",")
	ids := make([]int64, 0, len(parts))
	seen := map[int64]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not an environment", part)
		}
		// An environment compared with itself is three green columns that
		// prove nothing, so it is a mistake worth naming rather than folding.
		if seen[id] {
			return nil, errors.New("name each environment once")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) < 2 {
		return nil, errors.New("a comparison needs at least two environments")
	}
	if len(ids) > model.MaxCompare {
		return nil, fmt.Errorf("compare up to %d environments at a time", model.MaxCompare)
	}
	return ids, nil
}

// Compare answers with several environments as one table: a row per key, a
// cell per environment, and a state on every cell.
//
// One endpoint for both things the page does with it. Reading two environments
// against each other and copying between them are the same question — which
// keys disagree — and the copy is then the PUT that already exists, key by
// key, the same call the row's own Save button makes. A bulk-copy endpoint
// would be a second way to write a secret, and the one that runs rarely is the
// one that is wrong.
//
// It is `admin` and it does hand values back, like the values endpoint beside
// it: this page is where somebody reads what production is actually configured
// with. Note that the states arrive decided, so the table is legible with
// every value still masked.
var CompareEnvs = api.Define(api.Spec[CompareQuery, api.None, model.Comparison]{
	Name:  "Compare Secrets",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[CompareQuery, api.None]) (model.Comparison, error) {
		ids, err := r.Query.ids()
		if err != nil {
			return model.Comparison{}, api.BadRequest(err.Error())
		}
		comparison, err := store.Get().Compare(ids)
		if err != nil {
			return model.Comparison{}, api.BadRequest(err.Error())
		}
		return comparison, nil
	},
})
