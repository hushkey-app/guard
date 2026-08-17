// Package access is the settings card that rotates the two credentials guard
// is reached with — and the restart that makes a rotation real.
//
// Every endpoint here is `admin`, reads included. The values come back in the
// clear, which is the point: the person rotating the collector's secret has to
// paste it into a collector on another box, and one that comes back as dots
// sends them to a shell. So reading is exactly as privileged as writing.
package access

import "github.com/hushkey-app/guard/internal/access"

var current *access.Keys

// Use wires the file in. main.go calls it once, before api.Register.
func Use(k *access.Keys) { current = k }

// Get is what the endpoints read. A zero Keys rather than a panic, for the
// same reason as the release watcher: an instance that cannot write an env
// file is a normal instance, and it should draw a card that says so.
func Get() *access.Keys {
	if current == nil {
		return &access.Keys{}
	}
	return current
}

// Request names which of the two credentials a press is about.
type Request struct {
	Name string `json:"name"`
}
