// Package update is what the sidebar reads: whether a newer guard exists, and
// the one press that asks for it.
//
// Reading is open to anybody who may see the dashboard, because the sidebar
// draws for everybody and a 403 in the console on every page load is worse than
// telling a reader what they can already see at the bottom of the page — the
// version — plus the fact that a newer one exists.
//
// Asking for it is `admin`, and guard's part is only to write the version down.
// The install is done by a root-owned unit on a timer, which is what keeps the
// process holding every application's secrets out of the business of replacing
// binaries.
package update

import "github.com/hushkey-app/guard/internal/release"

var current *release.Watch

// Use wires the watcher in. main.go calls it once, before api.Register.
func Use(w *release.Watch) { current = w }

// Get is the watcher the endpoints read. A zero watcher rather than a panic:
// this one is genuinely optional — an instance with no outbound internet, or
// GUARD_UPDATE_REPO set to nothing, has no watcher and a sidebar that shows
// nothing, which is correct rather than broken.
func Get() *release.Watch {
	if current == nil {
		return &release.Watch{}
	}
	return current
}
