package pages

import "testing"

func TestDashboardRoutesUseClientRenderer(t *testing.T) {
	for _, route := range FsClientRoutes() {
		// A .raw route is its own document and is not part of the dashboard —
		// /login is the only one, and it is deliberately outside all of this:
		// no shell, no wasm, no live tick, because none of that furniture
		// belongs in front of somebody who has not signed in yet.
		if route.Raw {
			if route.Client || route.Mount != nil || route.Unmount != nil {
				t.Errorf("%s is a raw route and should have no client half", route.Pattern)
			}
			continue
		}
		if !route.Client {
			t.Errorf("%s is not client-renderable", route.Pattern)
		}
		if route.Mount == nil || route.Unmount == nil {
			t.Errorf("%s must load on Mount and invalidate work on Unmount", route.Pattern)
		}
	}
}
