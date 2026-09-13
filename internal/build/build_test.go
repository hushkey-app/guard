package build

import (
	"os"
	"testing"
)

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

// The bug this pins actually happened. v0.4.4.1 was tagged and published — the
// release workflow triggers on `v*`, so CI built it, uploaded it and the fleet
// installed it — and this package called it a development build because it had
// four components instead of three.
//
// That is the one verdict with teeth: release.State() sets Available to false
// for a development build, so every box running v0.4.4.1 could see v0.5.0 in
// the sidebar and had no button to take it. The fleet was stranded on a release
// it had installed perfectly.
//
// So: whatever the pipeline will publish, this has to recognise. The test is
// about the tag shape rather than about semver's opinion of it.
func TestAFourthComponentIsStillARelease(t *testing.T) {
	for _, version := range []string{"v0.4.4.1", "0.4.4.1", "v1.2.3.4", "v1.2.3.4.5", "v0.4.4.1-rc1"} {
		if IsDevelopment(version) {
			t.Fatalf("%q was published by the release workflow — it is not a development build", version)
		}
	}
	// And the widening must not have swallowed what git describe adds to one:
	// a commit past a four-part tag is still a development build, exactly as it
	// is for a three-part one.
	for _, version := range []string{
		"v0.4.4.1-1-gf494ce1",
		"v0.4.4.1-1-gf494ce1-dirty",
		"v0.4.4.1-dirty",
	} {
		if !IsDevelopment(version) {
			t.Fatalf("%q is a working tree, not a release", version)
		}
	}
	// Still not anything: the pipeline's `v*` is broader than a version, and
	// widening to four numbers must not have widened to prose.
	for _, version := range []string{"v0.4.4.", "v0.4", "vlatest", "v0.4.4.x", "v1.2.3.4beta"} {
		if !IsDevelopment(version) {
			t.Fatalf("%q is not a version this should have accepted", version)
		}
	}
}

// TestTheTagBeingReleasedIsARelease is what the release workflow runs before it
// builds anything, with GUARD_RELEASE_VERSION set to the tag being published.
//
// It is the same function the fleet asks, which is the entire point. The
// pipeline triggers on `v*` — far broader than a version — so until now it
// would happily build, upload and ship a tag this package cannot recognise, and
// the boxes that installed it were stranded with a quiet update card. One
// definition of "a release", asserted by the thing that makes them.
//
// Skipped when the variable is unset, because a developer running `go test
// ./...` is not publishing anything.
func TestTheTagBeingReleasedIsARelease(t *testing.T) {
	version := os.Getenv("GUARD_RELEASE_VERSION")
	if version == "" {
		t.Skip("not publishing — set GUARD_RELEASE_VERSION to check a tag")
	}
	if IsDevelopment(version) {
		t.Fatalf("%q is not a shape guard recognises as a release, so every box that installed it "+
			"would read as a development build and its update card would go quiet. "+
			"Tag it vMAJOR.MINOR.PATCH (a fourth number and a -rc suffix are fine).", version)
	}
}
