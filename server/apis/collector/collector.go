// Package collector holds the host stats collector the endpoints reach.
//
// A leaf, for the same reason server/apis/prober and server/apis/runner are:
// the generated table lives in the root apis package and imports every
// endpoint package, so an endpoint importing the root would be a cycle.
package collector

import "github.com/hushkey-app/guard/internal/cluster"

var current *cluster.Collector

// Use wires the collector in. main.go calls it once, before api.Register.
func Use(c *cluster.Collector) { current = c }

// Get returns the collector, or nil when guard is running without one — a test
// server, say. Callers check: an endpoint that assumed a background sampler
// exists would take a request down over a feature nobody wired.
func Get() *cluster.Collector { return current }
