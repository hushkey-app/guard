// Package deploy is the endpoints behind the Deploys page: the groups, the
// versioned templates, the runs, and the press that starts one.
//
// Everything here is admin. A deploy replaces what is running on somebody's
// production, and the reads are not much softer — a template holds the compose
// file and the list of variables an application boots with, which is a map of
// the system whether or not any value is in it.
//
// Two things are deliberately *not* endpoints:
//
//   - **There is no rollback endpoint.** A rollback is a deploy of a machine's
//     last known good tag, so it is this same POST with a different tag and one
//     machine named. A second path would be a second thing to get wrong, and it
//     would be the one used least often and tested least.
//   - **There is no "run this compose file" call.** The body carries a template
//     id and a tag; the file comes out of the database. That is what stops a
//     deploy being a way to run chosen content on a chosen box.
package deploy

import (
	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/mirairoad/howl-go/core/api"
)

// Start is one press: which group, which template version, which tag, and how.
type Start struct {
	GroupID    int64 `json:"group_id"`
	TemplateID int64 `json:"template_id"`
	// TemplateVersion pins the revision. Zero deploys the newest and the run
	// records which that was.
	TemplateVersion int    `json:"template_version,omitempty"`
	Tag             string `json:"tag"`
	// Mode is sequential (the default, and the one that protects anything) or
	// parallel. Anything else is read as sequential rather than refused: the
	// safe reading of an unknown mode is the safe mode.
	Mode string `json:"mode,omitempty"`
	// NodeIDs narrows the run to some of the group's machines. Empty is the
	// whole group, and one id is how a rollback of a single machine is said.
	NodeIDs []int64 `json:"node_ids,omitempty"`
	// Rollback marks the run as one for the history's sake. It changes nothing
	// about what happens.
	Rollback bool `json:"rollback,omitempty"`
}

func (s Start) Validate() error {
	if s.GroupID <= 0 {
		return api.Invalid("group_id", "no group chosen")
	}
	if s.TemplateID <= 0 {
		return api.Invalid("template_id", "no template chosen")
	}
	if err := model.ValidateTag(s.Tag); err != nil {
		return api.Invalid("tag", err.Error())
	}
	return nil
}

// Answer is what a stopped run is told: retry, skip or stop.
type Answer struct {
	RunID    int64  `json:"run_id"`
	Decision string `json:"decision"`
}

func (a Answer) Validate() error {
	if a.RunID <= 0 {
		return api.Invalid("run_id", "no run named")
	}
	if a.Decision == "" {
		return api.Invalid("decision", "no answer given")
	}
	return nil
}

// StateQuery asks what one machine is running.
type StateQuery struct {
	NodeID int64 `query:"node_id"`
}

func (q StateQuery) Validate() error {
	if q.NodeID <= 0 {
		return api.Invalid("node_id", "no machine named")
	}
	return nil
}
