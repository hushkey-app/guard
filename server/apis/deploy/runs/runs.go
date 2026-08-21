// Package runs is the deploy history: what was pressed, and what became of it.
package runs

import "github.com/hushkey-app/guard/internal/telemetry/model"

// Query bounds the list: one page of it, newest first.
type Query struct {
	Limit  int `query:"limit"`
	Offset int `query:"offset"`
	// Active narrows it to the runs still going or still stopped at a failure
	// — what a page polls while something is happening, so a live deploy is not
	// fifty rows of history away.
	Active bool `query:"active"`
}

// Page is one page of history and how much there is behind it.
//
// The total comes back with the rows rather than from a second endpoint,
// because a pager that has to make two requests to know whether to enable a
// button will sometimes draw the wrong one.
type Page struct {
	Runs  []model.DeployRun `json:"runs"`
	Total int               `json:"total"`
}
