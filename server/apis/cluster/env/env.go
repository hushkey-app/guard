// Package env is one machine's environment: the variables guard keeps for it, and
// the press that puts them on the box.
//
// Three endpoints and no file management. A machine has *an* environment — not a
// list of files somebody has to declare first — and the two things anybody wants
// to do with it are edit it and push it. Where it lands is fixed
// (`internal/envfile`): /etc/environment and a systemd drop-in, so everything on
// the machine that takes an environment gets the same one.
//
// Everything here is admin, reads included: this is where the database password is.
package env

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/api"
)

// Query selects the machine on a read.
type Query struct {
	NodeID int64 `query:"node_id"`
}

// Target names the machine an inject is for, and is the whole request: the
// variables come from the database and the paths are fixed in internal/envfile,
// so there is no shape of this call that writes chosen content to a chosen place.
type Target struct {
	NodeID int64 `json:"node_id"`
}

func (t Target) Validate() error {
	if t.NodeID <= 0 {
		return api.Invalid("node_id", "is required")
	}
	return nil
}

// Vars is what a save sends: the whole set, the way the box is edited.
type Vars struct {
	NodeID int64              `json:"node_id"`
	Vars   []model.NodeEnvVar `json:"vars"`
	// Text is the alternative: the block somebody pasted. Sent instead of Vars,
	// parsed on the server with the same dialect the vault's .env import uses, so
	// the browser never has to know what a quoted multi-line value looks like.
	Text string `json:"text,omitempty"`
}

func (v Vars) Validate() error {
	if v.NodeID <= 0 {
		return api.Invalid("node_id", "is required")
	}
	return nil
}

// Saved is the stored environment plus what could not be read from a paste.
type Saved struct {
	Vars []model.NodeEnvVar `json:"vars"`
	// Text is the same variables as the box should show them — rendered here
	// rather than in the browser, so what is on screen is exactly what a save
	// will read back. A value containing ` #` or a leading quote is the case that
	// makes the difference: printed raw it comes back truncated, and quoted by
	// the same function that writes the file it survives a round trip.
	Text    string             `json:"text"`
	Skipped []model.ImportSkip `json:"skipped,omitempty"`
	State   model.NodeEnvState `json:"state"`
}
