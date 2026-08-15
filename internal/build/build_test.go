package build

import "testing"

// The bug this exists to prevent: a stamped release carries "v0.1.0", the
// sidebar added another, and the update watcher then compared "vv0.1.0" against
// every release tag and never matched one.
func TestTagHasExactlyOneV(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	for _, stamped := range []string{"0.1.0", "v0.1.0", " v0.1.0 ", "v1.2.3-rc1"} {
		Version = stamped
		got := Tag()
		if got[0] != 'v' || got[1] == 'v' {
			t.Fatalf("%q stamped produced %q", stamped, got)
		}
	}

	Version = "v0.1.0"
	if Tag() != "v0.1.0" {
		t.Fatalf("a tag-shaped version came back as %q", Tag())
	}
	Version = "0.1.0"
	if Tag() != "v0.1.0" {
		t.Fatalf("a bare version came back as %q", Tag())
	}
}
