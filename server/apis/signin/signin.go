// Package signin holds the sign-in service the endpoints ask about.
//
// A leaf, for the same reason server/apis/store is one: the generated table
// lives in the root apis package and imports every endpoint package, so an
// endpoint may not import anything that leads back there.
//
// It answers a smaller question than internal/auth does. The endpoints never
// start a flow or verify a token — that is the middleware's job, and by the
// time a handler runs it has already happened. What they need is the two things
// only the configuration knows: whether anybody has to sign in at all, and
// which addresses come from GUARD_ADMIN_EMAIL and therefore cannot be edited
// from the page.
package signin

import (
	"github.com/hushkey-app/guard/internal/auth"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

var current *auth.Service

// Use wires the service in. main.go calls it once, before api.Register.
func Use(service *auth.Service) { current = service }

// Get is the service, which may legitimately be a nil *Service on an instance
// with no credentials — every method it has answers for that case, so a nil
// here is "sign-in is off" rather than a crash.
func Get() *auth.Service { return current }

// Enabled reports whether anybody has to sign in.
func Enabled() bool { return current.Enabled() }

// Fixed reports an address that comes from the environment.
func Fixed(email string) bool { return current.Fixed(email) }

// Admins are the environment's admins as member rows.
func Admins() []model.Member { return current.Admins() }
