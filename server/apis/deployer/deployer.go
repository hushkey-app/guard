// Package deployer holds the deploy runner the endpoints reach.
//
// A leaf, for the same reason server/apis/runner and server/apis/scheduler are:
// the generated table lives in the root apis package and imports every endpoint
// package, so an endpoint importing the root would be a cycle.
package deployer

import "github.com/hushkey-app/guard/internal/deploy"

var current *deploy.Runner

// Use wires the runner in. main.go calls it once, before api.Register.
func Use(r *deploy.Runner) { current = r }

// Get returns the runner, or nil where guard is running without one — a test
// server, say. Callers check: reading a group must not fail because nobody
// wired the thing that would have deployed it.
func Get() *deploy.Runner { return current }
