// Package store holds the telemetry store the endpoints read.
//
// It is a leaf on purpose. The generated table lives in the root apis package
// and imports every endpoint package, so an endpoint package importing the root
// would be an import cycle — the same constraint the page tree has, for the
// same reason. One package below both of them is where a shared dependency can
// live.
package store

import "github.com/mirairoad/guard/internal/telemetry"

var current *telemetry.Store

// Use wires the store in. main.go calls it once, before api.Register.
func Use(s *telemetry.Store) { current = s }

// Get is the store every endpoint reads. It panics when unset rather than
// returning nil: a nil store surfaces as a confusing crash inside a handler on
// the first request, and this way the mistake is obvious at startup.
func Get() *telemetry.Store {
	if current == nil {
		panic("apis: store.Use was never called")
	}
	return current
}
