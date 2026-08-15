package cluster

// The scheduler: the stored commands that run without anybody pressing them.
//
// A third loop beside the prober and the stats collector, and the same shape as
// both, because it answers a third question. The prober asks a service whether
// it is alive. The collector asks a machine how it is doing. This one runs the
// command somebody already stored against a machine — the same row, the same
// login, the same audit line — on a cadence instead of on a click.
//
// What it deliberately is not is a job queue. There is no work table, no
// retries, no dependencies and no workers. An action is due or it is not; if it
// is already running, the pass says so and moves on. Everything a job queue
// would buy — parallelism across machines, a record of what happened — is
// either already here or is a row in cluster_runs.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// ScheduleStore is the contract, declared here so the scheduler depends on an
// idea rather than on SQLite — and narrow enough that a test can be a struct
// with five methods.
type ScheduleStore interface {
	// ScheduledActions is every action carrying a schedule, on a machine that
	// is not paused.
	ScheduledActions() ([]model.NodeAction, error)
	// SSHLoginFor is the one call that returns a secret. An action on a machine
	// with no stored login answers an error, which is how a scheduled command
	// on an unreachable box becomes a recorded failure rather than a panic.
	SSHLoginFor(nodeID int64) (remote.Login, error)
	RecordRun(actionID int64, run model.Run) error
	PinFingerprint(nodeID int64, fingerprint string) error
}

// Executor is all the scheduler needs of a runner: one command, one answer.
// The same shape internal/remote already has, declared here because a package
// should ask for what it uses.
type Executor interface {
	Run(ctx context.Context, login remote.Login, command string) (remote.Result, error)
}

// Scheduler runs due actions until its context is cancelled.
type Scheduler struct {
	Store  ScheduleStore
	Runner Executor
	// Timeout bounds one scheduled run, and is much longer than the two minutes
	// a pressed button gets: the jobs people schedule are dumps and syncs, and
	// a backup killed at two minutes is a backup that has never worked.
	Timeout time.Duration
	Log     *slog.Logger

	once sync.Once
	wake chan struct{}
	// running is the overlap guard, and the reason this needs no queue: an
	// action already in flight is skipped rather than started twice. A dump
	// that has grown past its own interval must not race a second copy of
	// itself into the same bucket.
	mu      sync.Mutex
	running map[int64]time.Time
}

const (
	// scheduleIdle is how long to sleep when nothing is scheduled at all, and
	// the longest this loop ever sleeps: cron has minute resolution, so a pass
	// a minute is as often as it can matter.
	scheduleIdle = 60 * time.Second
	// DefaultScheduleTimeout is half an hour, against two minutes for a run
	// somebody is watching. It is a ceiling rather than an expectation — the
	// point is that a slow dump finishes, not that a wedged one hangs forever.
	DefaultScheduleTimeout = 30 * time.Minute
)

func (s *Scheduler) prepare() {
	s.once.Do(func() {
		if s.Log == nil {
			s.Log = slog.Default()
		}
		if s.Runner == nil {
			s.Runner = &remote.Runner{Timeout: DefaultScheduleTimeout}
		}
		if s.Timeout <= 0 {
			s.Timeout = DefaultScheduleTimeout
		}
		s.wake = make(chan struct{}, 1)
		s.running = map[int64]time.Time{}
	})
}

// Run starts due actions as they come due, until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.prepare()
	s.Log.Info("cluster scheduler started", slog.Duration("timeout", s.Timeout))
	for {
		wait := s.Round(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.wake:
			timer.Stop()
		}
	}
}

// Wake asks for a pass now — called when a machine's commands are saved, so a
// schedule somebody just typed is picked up while they are still looking at it.
func (s *Scheduler) Wake() {
	s.prepare()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Round starts everything due and returns how long until the next one is.
//
// The runs themselves are goroutines: a pass must not take as long as the job
// it started, or a six-hour dump would hold every other machine's schedule
// behind it. The pass is what decides, and what it decided is in cluster_runs.
func (s *Scheduler) Round(ctx context.Context) time.Duration {
	s.prepare()
	actions, err := s.Store.ScheduledActions()
	if err != nil {
		s.Log.Error("scheduler could not read its actions", slog.Any("err", err))
		return scheduleIdle
	}
	now := time.Now()
	next := scheduleIdle
	for _, action := range actions {
		schedule, err := model.ParseSchedule(action.Schedule)
		if err != nil {
			// Stored expressions are validated on save, so this is a row that
			// predates a rule or was written by hand. Skipped and said out
			// loud, because a schedule that silently never fires is the worst
			// of the available failures.
			s.Log.Warn("skipping an action with a schedule guard cannot read",
				slog.Int64("action", action.ID), slog.String("schedule", action.Schedule), slog.Any("err", err))
			continue
		}
		due := action.NextRun(now)
		if due.IsZero() {
			continue
		}
		if remaining := time.Until(due); remaining > 0 {
			next = min(next, remaining)
			continue
		}
		s.start(ctx, action)
		// And come back for this action's *next* fire rather than sleeping the
		// idle minute: an "@every 30s" job would otherwise run every sixty
		// seconds, which is a schedule quietly rewritten by its own loop.
		if following := schedule.Next(now); !following.IsZero() {
			next = min(next, time.Until(following))
		}
	}
	return max(next, time.Second)
}

// start runs one action, unless it is already running.
func (s *Scheduler) start(ctx context.Context, action model.NodeAction) {
	s.mu.Lock()
	since, busy := s.running[action.ID]
	if busy {
		s.mu.Unlock()
		s.skip(action, since)
		return
	}
	s.running[action.ID] = time.Now()
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, action.ID)
			s.mu.Unlock()
		}()
		s.execute(ctx, action)
	}()
}

// skip records a scheduled run that did not happen because the last one had
// not finished. A row rather than a log line, so a job that has outgrown its
// interval shows up as a run of skips on the card rather than as a backup that
// quietly halved its frequency.
func (s *Scheduler) skip(action model.NodeAction, since time.Time) {
	s.Log.Warn("scheduled run skipped: the previous one is still going",
		slog.Int64("action", action.ID), slog.String("name", action.Name),
		slog.Duration("running_for", time.Since(since).Round(time.Second)))
	run := model.Run{
		ActionID: action.ID,
		NodeID:   action.NodeID,
		Command:  action.Command,
		RanAt:    time.Now().UTC(),
		Trigger:  model.TriggerSchedule,
		Outcome:  model.OutcomeSkipped,
		Error:    "the previous run was still going after " + time.Since(since).Round(time.Second).String(),
	}
	if err := s.Store.RecordRun(action.ID, run); err != nil {
		s.Log.Error("skipped run not recorded", slog.Int64("action", action.ID), slog.Any("err", err))
	}
}

// execute is the same sequence a pressed button follows: read the login, run
// the line, pin the host key the first time, record how it went.
func (s *Scheduler) execute(ctx context.Context, action model.NodeAction) model.Run {
	run := model.Run{
		ActionID: action.ID,
		NodeID:   action.NodeID,
		Command:  action.Command,
		RanAt:    time.Now().UTC(),
		Trigger:  model.TriggerSchedule,
	}
	login, err := s.Store.SSHLoginFor(action.NodeID)
	if err != nil {
		// A scheduled command on a machine with no stored login is a recorded
		// failure, not a silence: somebody removed the password from under a
		// job that has been working for a month.
		run.Error = err.Error()
		run.ExitCode = -1
		s.record(run)
		return run
	}

	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	result, runErr := s.Runner.Run(ctx, login, action.Command)
	run.Output = result.Output
	run.ExitCode = result.ExitCode
	run.DurationMS = result.DurationMS
	run.Truncated = result.Truncated
	if result.Fingerprint != "" && login.Fingerprint == "" {
		// Trust on first use, exactly as a pressed run does: the pin must not
		// depend on which feature connected first.
		if err := s.Store.PinFingerprint(action.NodeID, result.Fingerprint); err != nil {
			s.Log.Error("host key not pinned", slog.Int64("node", action.NodeID), slog.Any("err", err))
		}
	}
	if runErr != nil {
		run.Error = runErr.Error()
		run.ExitCode = -1
		if errors.Is(runErr, remote.ErrHostKeyChanged) {
			s.Log.Warn("scheduled run refused: host key changed", slog.Int64("node", action.NodeID))
		}
	}
	// The same line a pressed run writes, with the schedule as the presser.
	// This is the one part of guard that changes somebody else's machine, and
	// nobody is watching a browser tab when it happens.
	s.Log.Info("ran a scheduled command over ssh",
		slog.Int64("node", action.NodeID), slog.Int64("action", action.ID),
		slog.String("name", action.Name), slog.String("user", login.User),
		slog.String("address", login.Address), slog.String("command", action.Command),
		slog.Int("exit", run.ExitCode), slog.Float64("ms", run.DurationMS),
		slog.String("err", run.Error))
	s.record(run)
	return run
}

func (s *Scheduler) record(run model.Run) {
	if err := s.Store.RecordRun(run.ActionID, run); err != nil {
		s.Log.Error("scheduled run not recorded", slog.Int64("action", run.ActionID), slog.Any("err", err))
	}
}

// Running reports which actions are in flight, so a page can say "still
// running" instead of "last run: four hours ago" about a job that is going on
// right now.
func (s *Scheduler) Running() map[int64]time.Time {
	s.prepare()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]time.Time, len(s.running))
	for id, since := range s.running {
		out[id] = since
	}
	return out
}
