package cluster

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// fakeScheduleStore is the store the scheduler asks: a list of actions, a
// login, and somewhere for the runs to land.
type fakeScheduleStore struct {
	mu      sync.Mutex
	actions []model.NodeAction
	runs    []model.Run
	noLogin bool
	pinned  string
}

func (f *fakeScheduleStore) ScheduledActions() ([]model.NodeAction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.NodeAction(nil), f.actions...), nil
}

func (f *fakeScheduleStore) SSHLoginFor(int64) (remote.Login, error) {
	if f.noLogin {
		return remote.Login{}, errTestNoLogin
	}
	return remote.Login{User: "root", Address: "10.0.0.4:22", Password: "x"}, nil
}

func (f *fakeScheduleStore) RecordRun(actionID int64, run model.Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run.ActionID = actionID
	f.runs = append(f.runs, run)
	// The store advances the action's last run, and so does this: without it a
	// due action stays due and the next pass starts it again.
	for i := range f.actions {
		if f.actions[i].ID == actionID && run.Outcome != model.OutcomeSkipped {
			f.actions[i].LastRunAt = run.RanAt
		}
	}
	return nil
}

func (f *fakeScheduleStore) PinFingerprint(_ int64, fingerprint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinned = fingerprint
	return nil
}

func (f *fakeScheduleStore) recorded() []model.Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Run(nil), f.runs...)
}

type errString string

func (e errString) Error() string { return string(e) }

const errTestNoLogin = errString("this machine has no stored password")

// blockingRunner is an executor that holds a run open until it is released, so
// a test can ask what happens to the next pass while one is still going.
type blockingRunner struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	ran     []string
	result  remote.Result
	err     error
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (b *blockingRunner) Run(ctx context.Context, _ remote.Login, command string) (remote.Result, error) {
	b.mu.Lock()
	b.ran = append(b.ran, command)
	b.mu.Unlock()
	b.started <- struct{}{}
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return remote.Result{}, ctx.Err()
		}
	}
	return b.result, b.err
}

func (b *blockingRunner) commands() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ran...)
}

func overdue(id int64, schedule string) model.NodeAction {
	// Last run long enough ago that any schedule in these tests is due.
	return model.NodeAction{
		ID: id, NodeID: 7, Name: "Dump", Command: "pg_dump",
		Schedule: schedule, LastRunAt: time.Now().Add(-24 * time.Hour),
	}
}

func TestRoundRunsWhatIsDueAndRecordsIt(t *testing.T) {
	store := &fakeScheduleStore{actions: []model.NodeAction{overdue(1, "@every 6h")}}
	runner := newBlockingRunner()
	runner.release = nil // finish immediately
	runner.result = remote.Result{Output: "done", DurationMS: 12, Fingerprint: "SHA256:abc"}
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	s.Round(context.Background())
	<-runner.started
	waitFor(t, func() bool { return len(store.recorded()) == 1 })

	run := store.recorded()[0]
	if run.Trigger != model.TriggerSchedule {
		t.Fatalf("trigger = %q, want the schedule", run.Trigger)
	}
	if run.Outcome != "" && run.Outcome != model.OutcomeOK {
		t.Fatalf("outcome = %q", run.Outcome)
	}
	if run.Result() != model.OutcomeOK {
		t.Fatalf("result = %q, want ok", run.Result())
	}
	if store.pinned != "SHA256:abc" {
		t.Fatal("a scheduled run pins the host key like any other connection")
	}
}

func TestRoundLeavesWhatIsNotDueAlone(t *testing.T) {
	action := overdue(1, "@every 6h")
	action.LastRunAt = time.Now().Add(-time.Minute)
	store := &fakeScheduleStore{actions: []model.NodeAction{action}}
	runner := newBlockingRunner()
	runner.release = nil
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	wait := s.Round(context.Background())
	if len(runner.commands()) != 0 {
		t.Fatal("an action that ran a minute ago is not due")
	}
	// And it sleeps a minute at most however far away the next fire is: cron
	// has minute resolution, so a longer sleep could only make it late.
	if wait != scheduleIdle {
		t.Fatalf("wait = %s, want the idle minute", wait)
	}
}

func TestASecondPassSkipsARunThatIsStillGoing(t *testing.T) {
	store := &fakeScheduleStore{actions: []model.NodeAction{overdue(1, "@every 6h")}}
	runner := newBlockingRunner()
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	s.Round(context.Background())
	<-runner.started // the first run is now inside the runner, holding

	// The action is still due — nothing has recorded a run yet — so a second
	// pass would start it again if the overlap guard were not there.
	s.Round(context.Background())
	waitFor(t, func() bool {
		for _, run := range store.recorded() {
			if run.Outcome == model.OutcomeSkipped {
				return true
			}
		}
		return false
	})
	if got := len(runner.commands()); got != 1 {
		t.Fatalf("the command ran %d times; two dumps must never race", got)
	}

	close(runner.release)
	waitFor(t, func() bool { return len(store.recorded()) == 2 })
	// The skip is a row, not a silence: a job that has outgrown its interval
	// should be visible as a run of skips.
	var skipped, ok int
	for _, run := range store.recorded() {
		switch run.Outcome {
		case model.OutcomeSkipped:
			skipped++
		case model.OutcomeOK, "":
			ok++
		}
	}
	if skipped != 1 || ok != 1 {
		t.Fatalf("skipped = %d, ok = %d, want one of each", skipped, ok)
	}
}

func TestAMissingLoginIsARecordedFailure(t *testing.T) {
	store := &fakeScheduleStore{actions: []model.NodeAction{overdue(1, "@every 6h")}, noLogin: true}
	runner := newBlockingRunner()
	runner.release = nil
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	s.Round(context.Background())
	waitFor(t, func() bool { return len(store.recorded()) == 1 })
	run := store.recorded()[0]
	if run.Result() != model.OutcomeFailed || run.Error == "" {
		t.Fatalf("run = %+v, want a recorded failure", run)
	}
	if len(runner.commands()) != 0 {
		t.Fatal("nothing should have been dialled")
	}
}

func TestAnUnreadableScheduleIsSkippedRatherThanRun(t *testing.T) {
	store := &fakeScheduleStore{actions: []model.NodeAction{overdue(1, "every 6 hours")}}
	runner := newBlockingRunner()
	runner.release = nil
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	s.Round(context.Background())
	if len(runner.commands()) != 0 || len(store.recorded()) != 0 {
		t.Fatal("an expression guard cannot read must not become a run")
	}
}

func TestRunningReportsWhatIsInFlight(t *testing.T) {
	store := &fakeScheduleStore{actions: []model.NodeAction{overdue(1, "@every 6h")}}
	runner := newBlockingRunner()
	s := &Scheduler{Store: store, Runner: runner, Log: quietLogger()}

	s.Round(context.Background())
	<-runner.started
	if _, busy := s.Running()[1]; !busy {
		t.Fatal("a run that is going should say so")
	}
	close(runner.release)
	waitFor(t, func() bool { _, busy := s.Running()[1]; return !busy })
}

// quietLogger keeps the expected failures in these tests out of the test
// output, where they read as something having gone wrong.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the scheduler")
}
