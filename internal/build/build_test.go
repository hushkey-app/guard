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

// The bug the default is here to prevent: the constant used to be the current
// release number, so every release made it staler and a development binary
// reported a version older than the checkout it came from.
func TestTheUnstampedDefaultIsNotAReleasedVersion(t *testing.T) {
	if !Development() {
		t.Fatalf("the default %q reads as a published release", Tag())
	}
}

func TestWhatCountsAsADevelopmentBuild(t *testing.T) {
	development := []string{
		"0.0.0-dev",               // nothing stamped
		"v0.1.0-7-g741bc0a",       // seven commits past a tag
		"v0.1.0-7-g741bc0a-dirty", // ...with local edits
		"v0.1.0-dirty",            // exactly a tag, edited
		"741bc0a",                 // a repository with no tags at all
		"",                        // stamped with nothing
	}
	for _, version := range development {
		if !IsDevelopment(version) {
			t.Fatalf("%q should read as a development build", version)
		}
	}
	// And what the release workflow stamps must not, or the sidebar would go
	// quiet on exactly the boxes the update card is for.
	for _, version := range []string{"v0.1.0", "0.1.0", "v1.2.3-rc1", "v10.0.4"} {
		if IsDevelopment(version) {
			t.Fatalf("%q is a release", version)
		}
	}
}
