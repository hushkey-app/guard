// Package build carries what the binary knows about itself.
package build

import (
	"regexp"
	"strings"
)

// Version is what the sidebar shows, what `-version` prints and what the
// OpenAPI document reports, so the three cannot drift. A var rather than a
// const so a build can set it:
//
//	go build -ldflags "-X github.com/hushkey-app/guard/internal/build.Version=v1.2.3"
//
// The release workflow stamps the tag; the Makefile stamps `git describe`, so a
// local build says which commit it is. The default below is what is left when
// nobody stamped anything — `go run .`, or a `go install` from a checkout — and
// it is deliberately not a version that was ever released.
//
// It used to be the current release number, hardcoded, and that is the bug this
// wording is here to prevent coming back: every release made the constant one
// version staler, so a development binary reported an older release than the one
// it was built from, the sidebar showed that number, and the update card offered
// an upgrade to a version the checkout was already ahead of. A version guard has
// to be told is a version somebody forgets to tell it.
var Version = "0.0.0-dev"

// Tag is Version with exactly one leading "v" — what a git tag looks like, and
// what everything that compares versions must use.
//
// It exists because the two forms met and produced "vv0.1.0". The default here
// carries no v and the sidebar added one; a release stamps the tag, which
// already has one. On a development build both spellings looked fine, and the
// first stamped binary rendered a doubled prefix and told the update card it
// was running a version no release would ever match — so it offered an update
// to the release it was already on, permanently.
//
// One function, used by the sidebar, the update watcher and -version, so
// stamping with or without the v both land in the same place. A human types
// that tag; the pipeline should not care which way they typed it.
func Tag() string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(Version), "v")
}

// release is what a stamped release looks like and nothing else: v1.2.3, or
// v1.2.3-rc1. What `git describe` adds to a tag — the commit count, the abbrev
// sha, -dirty — fails it, and so does the unstamped default.
var release = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// described is what `git describe` adds to a tag: seven commits past it, and
// the abbreviated sha. The count-and-sha suffix is the part a real pre-release
// tag never has.
var described = regexp.MustCompile(`-\d+-g[0-9a-f]{6,}`)

// Development reports a build that is not a published release: a working tree,
// a commit past a tag, or nothing stamped at all.
//
// It exists so the update card can stay quiet. That card compares its version
// against the newest release by *difference* rather than by ordering, on
// purpose — republishing an older tag is how a fleet is rolled back, and
// ordering would argue with that. But a development build differs from every
// release by construction, so without this the card would sit in the sidebar of
// every checkout, permanently offering to "update" a binary that is ahead of
// the release it is pointing at.
func Development() bool { return IsDevelopment(Tag()) }

// IsDevelopment is the same question about a version somebody hands in, which
// is what the update watcher asks: it compares the version it was *given*, so
// the test has to be about that string rather than about this package's var.
func IsDevelopment(version string) bool {
	if strings.TrimSpace(version) == "" {
		return true
	}
	// Normalised the way Tag does it, so "0.1.0" and "v0.1.0" are one answer
	// here too — the two spellings are exactly what produced "vv0.1.0" once.
	version = "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	if strings.Contains(version, "dirty") || strings.Contains(version, "dev") {
		return true
	}
	if described.MatchString(version) {
		return true
	}
	return !release.MatchString(version)
}
