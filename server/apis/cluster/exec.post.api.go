package cluster

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Command is one line to run on one machine.
type Command struct {
	NodeID  int64  `json:"node_id"`
	Command string `json:"command"`
}

func (c Command) Validate() error {
	if c.NodeID <= 0 {
		return api.Invalid("node_id", "is required")
	}
	if strings.TrimSpace(c.Command) == "" {
		return api.Invalid("command", "there is nothing to run")
	}
	// A line, not a script. Anything longer is a file, and a file belongs on the
	// machine rather than in a text box — the runner would also be sending it
	// through an argv that has a limit of its own.
	if len(c.Command) > 4096 {
		return api.Invalid("command", "that is longer than a command line — put it in a script on the machine")
	}
	if strings.ContainsRune(c.Command, 0) {
		return api.Invalid("command", "a command cannot contain a NUL")
	}
	return nil
}

// Exec runs one typed command on one machine: the command line on /cluster/{id}.
//
// It is the one endpoint in guard that takes a command rather than an action id,
// and it exists because the alternative was worse in practice: everything people
// actually do over SSH started as a line they typed once, and a dashboard that
// insisted on naming and storing it first was a dashboard people kept a terminal
// open beside. The stored commands are still the vetted list — what is scheduled,
// what has a staleness budget, what a card offers as a button — and this is a
// second, narrower door beside them:
//
//   - **admin**, like everything that changes a machine;
//   - **refused on a locked machine**, which is what the lock is for: a locked box
//     runs the list somebody vetted and nothing else, and a command line that still
//     worked on it would make locking decoration;
//   - **logged**, with the machine, the login, the command and how it ended, and
//     kept in the same history the stored commands write to — so "what has been
//     running on this box" still has one answer.
//
// A command that exited non-zero is a 200 carrying the exit code: failing the
// request would hide the output, which is the only part anybody wants.
var Exec = api.Define(api.Spec[api.None, Command, model.Run]{
	Name:  "Run a Command",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, Command]) (model.Run, error) {
		command := strings.TrimSpace(r.Body.Command)
		// The lock, and the login, before anything is dialled.
		if _, err := store.Get().ExecTarget(r.Body.NodeID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return model.Run{}, api.NotFound("no machine with that id")
			}
			return model.Run{}, api.BadRequest(err.Error())
		}
		run, err := execute(r.Context(), r.Body.NodeID, command)
		if err != nil {
			return model.Run{}, err
		}
		run.NodeID = r.Body.NodeID
		run.Trigger = model.TriggerManual
		// Remembered so the machine's history includes what somebody typed. A
		// failure to record is not a failure to run — it already happened.
		if err := store.Get().RecordExec(r.Body.NodeID, run); err != nil {
			return run, nil //nolint:nilerr
		}
		return run, nil
	},
})
