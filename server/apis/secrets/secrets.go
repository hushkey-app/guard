// Package secrets is the dashboard's half of the secrets store: the
// environments, the values in them, and the keys that read them back.
//
// Every endpoint here is `admin`, and every one of them is guard's — the
// applications never come through this door. They talk to `guard-vault`, a
// separate binary on the same database, so a bad dashboard release cannot stop
// a container from booting. That split is the whole feature; nothing in this
// package should ever grow a way for an application to read a secret, because
// the day it does is the day guard's uptime becomes everybody's uptime.
//
// The values do come back here, unlike every other secret in guard. An SSH
// password is a credential guard uses on somebody's behalf and is never read
// out; a secret is a value somebody is going to paste into a deployment, and a
// store that could only be written to would just mean the real copy lives in a
// file on a laptop.
package secrets

import "errors"

// EnvQuery names which environment a call is about. One field, because
// everything on this page is "in this group".
type EnvQuery struct {
	Env int64 `query:"env"`
}

func (q EnvQuery) Validate() error {
	if q.Env <= 0 {
		return errors.New("name an environment")
	}
	return nil
}
