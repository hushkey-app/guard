// Package info answers what the box guard is running on looks like.
//
// Guard samples every machine it *watches* and has never said anything about
// the one it is on. That is the box the dashboard is served from, so "is guard
// itself short of memory" and "how big has the database got" were questions
// that needed an SSH session — while the same figures for every other machine
// were one click away.
//
// Read-only, and open to anybody who may see the dashboard, like the update
// state beside it. It names a hostname, a kernel and a database path, which is
// the same class of thing /cluster already shows a member for every machine
// guard watches; there is nothing here a reader could act with.
package info

import (
	"github.com/hushkey-app/guard/internal/build"
	"github.com/hushkey-app/guard/internal/hostinfo"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Instance is read when asked rather than sampled on a timer: it describes
// right now, and a history of the box guard runs on is what the telemetry guard
// already collects is for.
//
// Every read is a handful of files under /proc plus one statfs, so this is
// cheap enough to answer per request and needs no privilege — which is the
// reason it can be a request at all rather than another loop.
var Instance = api.Define(api.Spec[api.None, api.None, hostinfo.Instance]{
	Name: "Instance Info",
	Handler: func(r *api.Request[api.None, api.None]) (hostinfo.Instance, error) {
		path := ""
		if settings, err := store.Get().Settings(); err == nil {
			path = settings.DatabasePath
		}
		return hostinfo.Read(build.Tag(), path), nil
	},
})
