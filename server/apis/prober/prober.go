// Package prober holds the cluster prober the endpoints reach.
//
// A leaf, for the same reason server/apis/store is one: the generated table
// lives in the root apis package and imports every endpoint package, so an
// endpoint importing the root would be a cycle. One package below both is where
// a shared dependency can live.
package prober

import "github.com/hushkey-app/guard/internal/cluster"

var current *cluster.Prober

// Use wires the prober in. main.go calls it once, before api.Register.
func Use(p *cluster.Prober) { current = p }

// Get returns the prober, or nil when guard is running without one — a test
// server, say. Callers check: an endpoint that panicked because nobody wired a
// background poller would take the whole API down with it.
func Get() *cluster.Prober { return current }
