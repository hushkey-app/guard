// Package build carries what the binary knows about itself.
package build

// Version is what the sidebar shows and what the OpenAPI document reports, so
// the two cannot drift. A var rather than a const so a release build can set
// it: go build -ldflags "-X github.com/hushkey-app/guard/internal/build.Version=1.2.3".
var Version = "0.1.0"
