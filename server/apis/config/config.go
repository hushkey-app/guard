// Package config is the configuration page: every environment variable guard
// reads, as a form, stored in guard's own database and applied at the next
// start.
//
// Every endpoint here is `admin`, reads included. Half the catalogue is a
// credential — an OAuth client secret, a webhook token, the operator's bearer
// token — and the values come back in the clear, because the alternative is a
// form of forty masked fields that nobody can check against the provider's
// console. Reading is therefore exactly as privileged as writing.
package config

import "github.com/hushkey-app/guard/internal/config"

var current *config.Set

// Use wires the configuration in. main.go calls it once, before api.Register.
func Use(set *config.Set) { current = set }

// Get is what the endpoints read. Nil means an instance that never loaded any —
// which cannot happen in guard's main, and is a refusal in words rather than a
// panic if it ever does.
func Get() *config.Set { return current }

// Values is what a save sends: only the names it is changing.
//
// A partial map on purpose. Two people with the page open should not overwrite
// each other's untouched fields, and a form that posts all forty every time
// makes every save a rewrite of the whole configuration.
type Values struct {
	Values map[string]string `json:"values"`
}
