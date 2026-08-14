// Package runner holds the SSH runner the endpoints reach.
//
// A leaf, for the same reason server/apis/store and server/apis/prober are:
// the generated table lives in the root apis package and imports every
// endpoint package, so an endpoint importing the root would be a cycle.
package runner

import "github.com/hushkey-app/guard/internal/remote"

var current *remote.Runner

// Use wires the runner in. main.go calls it once, before api.Register.
func Use(r *remote.Runner) { current = r }

// Get returns the runner, or nil when guard is running without one. Callers
// check: an instance built without the ability to reach machines should answer
// "not available here" rather than crash a request.
func Get() *remote.Runner { return current }
