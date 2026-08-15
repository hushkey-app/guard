// Package build carries what the binary knows about itself.
package build

import "strings"

// Version is what the sidebar shows and what the OpenAPI document reports, so
// the two cannot drift. A var rather than a const so a release build can set
// it: go build -ldflags "-X github.com/hushkey-app/guard/internal/build.Version=1.2.3".
var Version = "0.1.0"

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
