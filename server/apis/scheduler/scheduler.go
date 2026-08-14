// Package scheduler holds the cluster scheduler the endpoints reach.
//
// A leaf, for the same reason server/apis/prober and server/apis/collector are:
// the generated table lives in the root apis package and imports every endpoint
// package, so an endpoint importing the root would be a cycle.
package scheduler

import "github.com/hushkey-app/guard/internal/cluster"

var current *cluster.Scheduler

// Use wires the scheduler in. main.go calls it once, before api.Register.
func Use(s *cluster.Scheduler) { current = s }

// Get returns the scheduler, or nil when guard is running without one — a test
// server, say. Callers check: saving a command must not fail because nobody
// wired the loop that would have run it.
func Get() *cluster.Scheduler { return current }
