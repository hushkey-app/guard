package pages

import "testing"

func TestDashboardRoutesUseClientRenderer(t *testing.T) {
	for _, route := range FsClientRoutes() {
		if !route.Client {
			t.Errorf("%s is not client-renderable", route.Pattern)
		}
		if route.Mount == nil || route.Unmount == nil {
			t.Errorf("%s must load on Mount and invalidate work on Unmount", route.Pattern)
		}
	}
}
