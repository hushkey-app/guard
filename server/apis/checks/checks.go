// Package checks is the endpoint layer for HTTP services watched by Guard.
package checks

import (
	"github.com/hushkey-app/guard/server/apis/prober"
)

func wake() {
	if p := prober.Get(); p != nil {
		p.Wake()
	}
}
