package deploy

// Package deploy puts a versioned image on the machines guard already watches.
//
// It is a fifth loop-shaped thing beside the prober, the collector, the
// scheduler and the watchdog, and it borrows from each of them on purpose: the
// SSH runner the scheduler uses, the health check the prober performs, the
// delivery the watchdog reaches for. Nothing here is a new way to reach a
// machine — a deploy is files written and docker run over the same login, and
// the health gate is the same GET the cluster page has always made.
//
// Four rules carry the design, and each one is the answer to a question that
// would otherwise be discovered during an incident:
//
//   - **Healthy means the health check passed, and nothing else.** Three
//     consecutive successes, five seconds apart, two minutes to do it in. Not
//     an error rate: a container twenty seconds old has served nobody, and
//     guard's rule everywhere is that an empty window is silence rather than
//     zero — so a rate-based gate would read "no traffic yet" as "no errors" and
//     pass a corpse. Error rates are worth alerting on, on their own loop, after
//     the deploy. They are not a gate.
//
//   - **A stopped run says so, and does not wait forever.** A sequential run
//     that fails stops, tells the group's destination immediately, says it again
//     after fifteen minutes, and gives up after thirty — releasing its locks and
//     recording what it did and did not touch. An unattended deploy "waiting
//     until resolved" with nobody watching is a stuck deploy holding a lock
//     nobody can clear.
//
//   - **A lock is per machine, in memory, and fails loud.** The scheduler's
//     bargain exactly: a second deploy at a machine already being deployed to is
//     refused, never queued. In memory because a restart must not leave a lock
//     behind — what a restart leaves is rows saying `interrupted`, which
//     `SweepDeployRuns` writes at startup.
//
//   - **Nothing is resumed.** Guard cannot know whether `compose up` finished,
//     and `current_tag` only ever moves on a passed health gate, so an
//     interrupted run leaves the dashboard saying the last tag that actually
//     answered rather than the one that was on its way.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// Store is the contract, declared here so this package depends on an idea
// rather than on SQLite — the same shape every other loop's store has.
type Store interface {
	Node(id int64) (model.Node, error)
	DeployGroup(id int64) (model.DeployGroup, error)
	DeployTemplate(id int64, version int) (model.DeployTemplate, error)
	// DeployTarget is the one call that returns a secret, and the one that
	// applies the machine's lock. Nothing here reaches a login another way.
	DeployTarget(nodeID int64) (remote.Login, error)
	ResolveDeployVars(template model.DeployTemplate) ([]model.NodeEnvVar, error)
	DeployStateFor(nodeID int64, service string) (model.DeployState, error)
	CreateDeployRun(run model.DeployRun) (model.DeployRun, error)
	DeployRun(id int64) (model.DeployRun, error)
	SetDeployInstance(instance model.DeployInstance) error
	SetDeployRunStatus(runID int64, status string) error
	RecordDeployHealthy(nodeID int64, service, tag string, templateID int64, version int) error
	DestinationFor(id int64) (notify.Destination, error)
	PinFingerprint(nodeID int64, fingerprint string) error
	SweepDeployRuns() (int, error)
}

// Executor is all this needs of an SSH runner: one command, one answer.
type Executor interface {
	Run(ctx context.Context, login remote.Login, command string) (remote.Result, error)
}

// Streamer is the half of the runner that talks while it works. Optional: an
// Executor that does not implement it simply reports nothing until it is done,
// which is what every test store does and what guard did before.
type Streamer interface {
	Stream(ctx context.Context, login remote.Login, command string, onChunk func(string)) (remote.Result, error)
}

// progressEvery is how often a running command's output is written to the row
// the page is polling.
//
// Not per line: `docker compose pull` prints a progress bar, and a database
// write per frame of it is a lot of writes to say very little. A second is
// under the three the dashboard ticks at, so the pane is never behind by more
// than one tick — and the rest of the output lands with the final save either
// way.
const progressEvery = time.Second

// Prober is all this needs of the health check: one GET, one verdict. The
// cluster prober satisfies it, which is the point — a deploy is proved by the
// same check the dashboard has been drawing all along.
type Prober interface {
	Check(ctx context.Context, target string) model.Check
}

// Timeout is one machine's whole deploy over SSH: writing two files, pulling an
// image and recreating a container.
//
// Ten minutes, against the two a pressed command gets and the thirty a
// scheduled dump gets. A constant rather than a setting, like every other
// cadence in guard: the number is only ever wrong in one direction — a fat
// image on a slow link — and the place for it is twenty lines from where it is
// read.
const Timeout = 10 * time.Minute

// What an operator can say to a stopped run.
const (
	// DecisionRetry deploys the same tag to the same machine again. The
	// ordinary answer when the failure was the registry or the network.
	DecisionRetry = "retry"
	// DecisionSkip leaves the failed machine as it is and carries on with the
	// rest of the group. The machine keeps its failed row and its old tag.
	DecisionSkip = "skip"
	// DecisionStop ends the run. Everything not yet reached stays untouched.
	DecisionStop = "stop"
)

// Runner is the deploys, and the locks they hold.
type Runner struct {
	Store  Store
	SSH    Executor
	Probe  Prober
	Sender notify.Sender
	Log    *slog.Logger

	once sync.Once
	mu   sync.Mutex
	// ctx is the process's lifetime, taken from Run. A deploy outlives the
	// request that started it — that is what makes it a deploy rather than a
	// very long POST — so it must not be cancelled when the browser goes away.
	ctx context.Context
	// busy is the lock: a machine id to the run holding it. In memory, so a
	// restart releases every one of them.
	busy map[int64]int64
	// waiting is the runs stopped at a failure, and the way to answer them.
	waiting map[int64]chan string
	// preparing is the docker installs in flight, by machine — what the page
	// polls while one is talking.
	preparing map[int64]*Preparation
	// watchers are the browsers looking at a deploy right now. Nothing here is
	// authoritative — see stream.go — so losing them all costs nothing.
	watchers map[*watcher]struct{}
	// cancels is how a run is stopped: its own context, one per run, so
	// pressing stop reaches the SSH session and the health gate rather than
	// setting a flag nothing is reading. cancelled remembers that it was a
	// person rather than a failure, which is what the run gets recorded as.
	cancels   map[int64]context.CancelFunc
	cancelled map[int64]bool
	// The gate's numbers, taken from the model at startup. Fields rather than
	// constants read directly so the tests can prove the shape of the gate
	// without waiting the real thirty seconds a machine takes to pass one.
	// Nothing outside this package can set them: what "healthy" means is not a
	// knob, because a knob on it is a way to make a deploy pass by lowering the
	// bar in the same dialog that starts it.
	passes   int
	interval time.Duration
	deadline time.Duration
}

func (r *Runner) prepare() {
	r.once.Do(func() {
		if r.Log == nil {
			r.Log = slog.Default()
		}
		if r.SSH == nil {
			r.SSH = &remote.Runner{Timeout: Timeout}
		}
		r.busy = map[int64]int64{}
		r.waiting = map[int64]chan string{}
		r.preparing = map[int64]*Preparation{}
		r.watchers = map[*watcher]struct{}{}
		r.cancels = map[int64]context.CancelFunc{}
		r.cancelled = map[int64]bool{}
		r.ctx = context.Background()
		r.passes = model.HealthPasses
		r.interval = model.HealthInterval
		r.deadline = model.HealthDeadline
	})
}

// Run makes the process's lifetime available to deploys and makes honest what
// the last restart left behind.
//
// It does not loop. There is nothing to poll: a deploy is driven by the press
// that started it, and the one thing that has to happen on a timer — an
// operator who never came back — is a timer inside the run that is waiting.
func (r *Runner) Run(ctx context.Context) {
	r.prepare()
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
	if touched, err := r.Store.SweepDeployRuns(); err != nil {
		r.Log.Error("could not tidy up the deploys the last restart interrupted", slog.Any("err", err))
	} else if touched > 0 {
		r.Log.Warn("guard restarted while machines were being deployed to",
			slog.Int("machines", touched),
			slog.String("note", "marked interrupted; what each machine is running is the last tag that passed its health check"))
	}
	<-ctx.Done()
}

// Request is one press.
type Request struct {
	GroupID    int64
	TemplateID int64
	// TemplateVersion pins the revision. Zero takes the newest, and the run
	// records which that turned out to be.
	TemplateVersion int
	Tag             string
	Mode            string
	// NodeIDs narrows the run to some of the group's machines. Empty is the
	// whole group; one id is how a rollback of a single machine is expressed,
	// because a rollback is this same flow with a different tag.
	NodeIDs  []int64
	Rollback bool
}

// Start proves everything it can before touching a machine, then deploys in the
// background.
//
// Everything that can fail without consequence fails here: an unreadable
// template, a tag that is not a tag, a vault key that is not there, a machine
// that is locked or has no login, a machine already being deployed to. A run
// that gets past this line has a login for every machine in it and every value
// it is going to write — so a failure afterwards is the deploy failing, not the
// request being wrong.
func (r *Runner) Start(request Request) (model.DeployRun, error) {
	r.prepare()
	group, err := r.Store.DeployGroup(request.GroupID)
	if err != nil {
		return model.DeployRun{}, errors.New("that group does not exist")
	}
	template, err := r.Store.DeployTemplate(request.TemplateID, request.TemplateVersion)
	if err != nil {
		return model.DeployRun{}, errors.New("that template version does not exist")
	}
	if err := model.ValidateTag(request.Tag); err != nil {
		return model.DeployRun{}, err
	}
	mode := request.Mode
	if mode != model.ModeParallel {
		mode = model.ModeSequential
	}
	// The values, resolved once for the whole run. Once, because a vault key
	// changing halfway through a rolling deploy would give the second half of
	// the fleet a different environment to the first — and here, because a key
	// that is missing should stop the run before any machine has been touched.
	vars, err := r.Store.ResolveDeployVars(template)
	if err != nil {
		return model.DeployRun{}, err
	}
	nodes, err := r.plan(group, request.NodeIDs)
	if err != nil {
		return model.DeployRun{}, err
	}
	// Every machine has to be reachable and unlocked before any of them is
	// touched. A rolling deploy that stops at the third machine because it is
	// locked has already replaced two.
	instances := make([]model.DeployInstance, 0, len(nodes))
	for _, member := range nodes {
		if _, err := r.Store.DeployTarget(member.NodeID); err != nil {
			return model.DeployRun{}, fmt.Errorf("%s: %w", member.Name, err)
		}
		state, err := r.Store.DeployStateFor(member.NodeID, template.ServiceName)
		if err != nil {
			return model.DeployRun{}, err
		}
		instances = append(instances, model.DeployInstance{
			NodeID:      member.NodeID,
			NodeName:    member.Name,
			Status:      model.InstancePending,
			PreviousTag: state.CurrentTag,
		})
	}
	run := model.DeployRun{
		GroupID:         group.ID,
		GroupName:       group.Name,
		TemplateID:      template.ID,
		TemplateVersion: template.Version,
		TemplateName:    template.Name,
		ServiceName:     template.ServiceName,
		Image:           template.Image,
		Tag:             request.Tag,
		Mode:            mode,
		Rollback:        request.Rollback,
		Instances:       instances,
	}
	// The locks, taken together. Failing loud rather than queueing is the whole
	// of "no deploy queue": two people deploying different tags to one machine
	// is not something to serialise, it is something to stop.
	if err := r.hold(run.Instances); err != nil {
		return model.DeployRun{}, err
	}
	stored, err := r.Store.CreateDeployRun(run)
	if err != nil {
		r.releaseNodes(run.Instances)
		return model.DeployRun{}, err
	}
	// The lock is taken before the run exists, because the order that matters
	// is "nobody else can start on these machines" first. Now that the run has
	// an id, the lock can name it — so a refused second deploy says which run
	// is holding the machine rather than which run is holding it, run zero.
	r.stamp(stored.Instances, stored.ID)
	// Its own context, so this one run can be stopped without touching the
	// others. Derived from the process's lifetime rather than the request's: a
	// deploy outlives the POST that started it.
	r.mu.Lock()
	runCtx, cancel := context.WithCancel(r.ctx)
	r.cancels[stored.ID] = cancel
	r.mu.Unlock()

	go r.execute(runCtx, stored, template, vars, group.WebhookID)
	return stored, nil
}

// plan is which of the group's machines this run touches, in the group's own
// order — which is the order somebody chose when they built it, and therefore
// the order a rolling deploy should follow.
func (r *Runner) plan(group model.DeployGroup, only []int64) ([]model.DeployMember, error) {
	if len(group.Nodes) == 0 {
		return nil, errors.New("that group has no machines in it")
	}
	if len(only) == 0 {
		return group.Nodes, nil
	}
	wanted := map[int64]bool{}
	for _, id := range only {
		wanted[id] = true
	}
	chosen := []model.DeployMember{}
	for _, member := range group.Nodes {
		if wanted[member.NodeID] {
			chosen = append(chosen, member)
		}
	}
	if len(chosen) == 0 {
		return nil, errors.New("none of those machines are in that group")
	}
	return chosen, nil
}

// hold takes every machine's lock or none of them.
func (r *Runner) hold(instances []model.DeployInstance) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, instance := range instances {
		if runID, busy := r.busy[instance.NodeID]; busy {
			return fmt.Errorf("%s is already being deployed to by run %d", instance.NodeName, runID)
		}
	}
	for _, instance := range instances {
		r.busy[instance.NodeID] = instance.RunID
	}
	return nil
}

// stamp names the run holding each lock, once it has an id.
func (r *Runner) stamp(instances []model.DeployInstance, runID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, instance := range instances {
		r.busy[instance.NodeID] = runID
	}
}

func (r *Runner) releaseNodes(instances []model.DeployInstance) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, instance := range instances {
		delete(r.busy, instance.NodeID)
	}
}

// Deploying reports the machines a deploy currently holds, so a page can say
// "being deployed to" about a machine rather than leaving somebody to work it
// out from a run they have not opened.
func (r *Runner) Deploying() map[int64]int64 {
	r.prepare()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[int64]int64, len(r.busy))
	for node, run := range r.busy {
		out[node] = run
	}
	return out
}

// Resolve answers a run that stopped at a failure.
//
// It is a press, not a poll: the run is sitting in `await` and this is the only
// thing that moves it, other than the deadline. A run that is not waiting says
// so rather than pretending, because "I clicked retry and nothing happened" is
// the worst thing this feature could do.
func (r *Runner) Resolve(runID int64, decision string) error {
	r.prepare()
	switch decision {
	case DecisionRetry, DecisionSkip, DecisionStop:
	default:
		return errors.New("that is not something a stopped deploy can be told")
	}
	r.mu.Lock()
	answers, waiting := r.waiting[runID]
	r.mu.Unlock()
	if !waiting {
		return errors.New("that deploy is not waiting for anything")
	}
	select {
	case answers <- decision:
		return nil
	default:
		return errors.New("that deploy has already been answered")
	}
}

// Cancel stops a run that is still going.
//
// What it can do and what it cannot are worth being exact about, because the
// button says "stop" and somebody will press it during an incident:
//
//   - it stops guard **advancing** — no machine after this one is touched;
//   - it stops guard **waiting** — the health gate and the SSH session are cut,
//     rather than run down their two- and ten-minute clocks;
//   - it does **not undo anything**. A machine already deployed to keeps what it
//     was given, and the one in flight may have a container running that guard
//     never proved. Going back is a deploy of the last known good tag, which is
//     the ordinary press.
//
// A run stopped at a failure is cancelled the same way, which is what makes
// this one button rather than two.
func (r *Runner) Cancel(runID int64) error {
	r.prepare()
	r.mu.Lock()
	cancel, running := r.cancels[runID]
	answers, waiting := r.waiting[runID]
	if running {
		r.cancelled[runID] = true
	}
	r.mu.Unlock()
	if !running {
		return errors.New("that deploy is not running")
	}
	// A run sitting in await is not inside a context-aware wait for the reason
	// it is waiting — it is waiting for a person — so it gets told as well.
	if waiting {
		select {
		case answers <- DecisionStop:
		default:
		}
	}
	cancel()
	r.Log.Warn("deploy cancelled", slog.Int64("run", runID))
	return nil
}

// stopping reports that somebody pressed stop, so a failure caused by the
// cancellation is recorded as a cancellation rather than as a machine that
// broke.
func (r *Runner) stopping(runID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled[runID]
}

// execute is the run itself, on its own goroutine.
func (r *Runner) execute(ctx context.Context, run model.DeployRun, template model.DeployTemplate,
	vars []model.NodeEnvVar, webhookID int64) {
	defer r.releaseNodes(run.Instances)
	defer func() {
		r.mu.Lock()
		if cancel, ok := r.cancels[run.ID]; ok {
			cancel()
			delete(r.cancels, run.ID)
		}
		delete(r.cancelled, run.ID)
		r.mu.Unlock()
	}()
	// A deploy runs on its own goroutine, and an unrecovered panic on any
	// goroutine takes the whole process with it. That is the wrong trade here
	// by a long way: guard is what the rest of the estate reports *into*, so a
	// bad deploy must not also stop the telemetry, the health checks and the
	// dashboard somebody would use to find out what happened. The run is marked
	// failed and the locks are given back by the defer above.
	defer func() {
		if problem := recover(); problem != nil {
			r.Log.Error("a deploy crashed", slog.Int64("run", run.ID), slog.Any("panic", problem),
				slog.String("stack", string(debug.Stack())))
			if err := r.Store.SetDeployRunStatus(run.ID, model.RunFailed); err != nil {
				r.Log.Error("could not record a crashed deploy", slog.Int64("run", run.ID), slog.Any("err", err))
			}
		}
	}()

	log := r.Log.With(slog.Int64("run", run.ID), slog.String("group", run.GroupName),
		slog.String("tag", run.Tag), slog.String("mode", run.Mode))
	log.Info("deploy started", slog.Int("machines", len(run.Instances)))

	if run.Mode == model.ModeParallel {
		r.parallel(ctx, run, template, vars)
	} else {
		r.sequential(ctx, run, template, vars, webhookID)
	}
	r.finish(run, log)
	// The run is over. One frame saying so, after the status has been written,
	// so a watcher that goes and reads the row gets the finished one.
	r.publish(Frame{Kind: KindRun, RunID: run.ID, Done: true})
}

// parallel is every machine at once, gated by nothing.
//
// Health is still checked and still recorded — the trade being made is that
// nobody waits for it, not that nobody knows. A machine that fails here is a
// failed row and a red line on the page, and the other four carried on.
func (r *Runner) parallel(ctx context.Context, run model.DeployRun, template model.DeployTemplate, vars []model.NodeEnvVar) {
	var wait sync.WaitGroup
	for i := range run.Instances {
		instance := run.Instances[i]
		wait.Add(1)
		go func() {
			defer wait.Done()
			r.deployOne(ctx, run, template, vars, &instance)
		}()
	}
	wait.Wait()
}

// sequential is one machine at a time, and a stop at the first failure.
func (r *Runner) sequential(ctx context.Context, run model.DeployRun, template model.DeployTemplate,
	vars []model.NodeEnvVar, webhookID int64) {
	for i := range run.Instances {
		instance := run.Instances[i]
		for {
			if r.deployOne(ctx, run, template, vars, &instance) {
				break
			}
			// Cancelled rather than broken. Nobody is asked what to do next,
			// because somebody has just said: a dialog offering retry to the
			// person who pressed stop is a dialog arguing with them.
			if r.stopping(run.ID) {
				r.abandon(run, i+1, DecisionStop)
				return
			}
			// Stopped. Say so before anything else: the point of stopping is
			// that a person decides what happens next, and a person who has not
			// been told is a run that waits half an hour and gives up.
			if err := r.Store.SetDeployRunStatus(run.ID, model.RunAwaiting); err != nil {
				r.Log.Error("could not record that a deploy is waiting", slog.Any("err", err))
			}
			r.tell(ctx, webhookID, run, instance, notify.StateFiring,
				instance.NodeName+" failed its health check",
				fmt.Sprintf("%s stopped at %s deploying %s:%s. Nothing after it has been touched. Retry, skip it, or stop the run.",
					run.GroupName, instance.NodeName, run.Image, run.Tag))
			decision := r.await(ctx, run.ID, webhookID, run, instance)
			if err := r.Store.SetDeployRunStatus(run.ID, model.RunRunning); err != nil {
				r.Log.Error("could not record that a deploy resumed", slog.Any("err", err))
			}
			switch decision {
			case DecisionRetry:
				continue
			case DecisionSkip:
				// The failed row stays failed — it is what happened — and the
				// rest of the group carries on.
			default:
				// Stopped, or nobody came back. Everything not yet reached is
				// left exactly as it is, and said to be.
				r.abandon(run, i+1, decision)
				return
			}
			break
		}
	}
}

// await is the wait for a person, with both ends defined.
func (r *Runner) await(ctx context.Context, runID, webhookID int64, run model.DeployRun,
	instance model.DeployInstance) string {
	answers := make(chan string, 1)
	r.mu.Lock()
	r.waiting[runID] = answers
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiting, runID)
		r.mu.Unlock()
	}()

	remind := time.NewTimer(model.AwaitingAlert)
	defer remind.Stop()
	deadline := time.NewTimer(model.AwaitingDeadline)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			// Guard is going down. The rows are made honest by the sweep on the
			// way back up, not from here — a process being torn down is the
			// worst moment to be writing about what it meant to do.
			return DecisionStop
		case decision := <-answers:
			return decision
		case <-remind.C:
			r.tell(ctx, webhookID, run, instance, notify.StateFiring,
				"still waiting on "+run.GroupName,
				fmt.Sprintf("%s has been stopped at %s for %s. It gives up at %s.",
					run.GroupName, instance.NodeName, model.AwaitingAlert, model.AwaitingDeadline))
		case <-deadline.C:
			r.tell(ctx, webhookID, run, instance, notify.StateFiring,
				run.GroupName+" gave up waiting",
				fmt.Sprintf("Nobody answered in %s, so the deploy released its machines. %s is still on %s; the machines after it were never touched.",
					model.AwaitingDeadline, instance.NodeName, orNone(instance.PreviousTag)))
			return decisionAbandoned
		}
	}
}

// decisionAbandoned is not something anybody can say — it is what the deadline
// says on their behalf, and it is separate from DecisionStop so the run records
// which of the two happened.
const decisionAbandoned = "abandoned"

// abandon marks everything a stopped run never reached.
//
// Skipped rather than failed: nothing was done to those machines, and a row
// saying "failed" about a box that was never touched is the kind of history
// that gets somebody to reboot a healthy machine at 3am.
func (r *Runner) abandon(run model.DeployRun, from int, decision string) {
	for i := from; i < len(run.Instances); i++ {
		instance := run.Instances[i]
		instance.Status = model.InstanceSkipped
		instance.FinishedAt = time.Now().UTC()
		if decision == decisionAbandoned {
			instance.Error = "the deploy gave up waiting before it reached this machine"
		} else {
			instance.Error = "the deploy was stopped before it reached this machine"
		}
		r.save(&instance)
	}
	if decision == decisionAbandoned {
		if err := r.Store.SetDeployRunStatus(run.ID, model.RunAbandoned); err != nil {
			r.Log.Error("could not record an abandoned deploy", slog.Any("err", err))
		}
	}
}

// finish reads the rows back and lets them decide, rather than tracking a
// verdict alongside them. One source of truth for "how did it go", and it is
// the same one the page reads.
func (r *Runner) finish(run model.DeployRun, log *slog.Logger) {
	current, err := r.Store.DeployRun(run.ID)
	if err != nil {
		r.Log.Error("could not read a finished deploy back", slog.Any("err", err))
		return
	}
	if current.Status == model.RunAbandoned {
		log.Warn("deploy abandoned")
		return
	}
	if r.stopping(run.ID) {
		if err := r.Store.SetDeployRunStatus(run.ID, model.RunCancelled); err != nil {
			r.Log.Error("could not record a cancelled deploy", slog.Any("err", err))
		}
		healthy, _, pending := current.Tally()
		log.Warn("deploy cancelled", slog.Int("healthy", healthy), slog.Int("untouched", pending))
		return
	}
	healthy, failed, pending := current.Tally()
	status := model.RunHealthy
	if failed > 0 || healthy == 0 {
		status = model.RunFailed
	}
	if err := r.Store.SetDeployRunStatus(run.ID, status); err != nil {
		r.Log.Error("could not record a finished deploy", slog.Any("err", err))
	}
	// "outcome", not "status": the console handler treats `status` as an HTTP
	// status code and reads it as an integer, so a string under that key is a
	// panic in whichever goroutine logged it. Every other key here is free.
	log.Info("deploy finished", slog.String("outcome", status),
		slog.Int("healthy", healthy), slog.Int("failed", failed), slog.Int("untouched", pending))
}

// deployOne is one machine: two files, a pull, a recreate and the gate. It
// reports whether the machine came back healthy.
func (r *Runner) deployOne(ctx context.Context, run model.DeployRun, template model.DeployTemplate,
	vars []model.NodeEnvVar, instance *model.DeployInstance) bool {
	instance.RunID = run.ID
	instance.Status = model.InstanceDeploying
	instance.StartedAt = time.Now().UTC()
	instance.FinishedAt = time.Time{}
	instance.Error = ""
	instance.Health = ""
	instance.Output = ""
	r.save(instance)

	// The login is read again rather than carried from the preflight: a machine
	// locked while the deploy was working through the group must be refused,
	// and a password rotated in the same minute must be the one used.
	login, err := r.Store.DeployTarget(instance.NodeID)
	if err != nil {
		return r.fail(instance, err.Error())
	}
	command, err := Command(template, run.Tag, vars)
	if err != nil {
		return r.fail(instance, err.Error())
	}
	// Streamed into the row the page is already polling. A ten-minute pull that
	// says nothing until it finishes is a progress bar with no progress in it —
	// and the row is the right place for it rather than a live connection,
	// because a deploy outlives the browser that started it.
	result, err := r.run(ctx, login, command, func(sofar string) {
		instance.Output = sofar
		r.save(instance)
	}, func(sofar string) {
		// Every chunk, not every second: this one goes to a channel, not to
		// SQLite, and the whole point of it is that it is immediate.
		r.publish(Frame{Kind: KindRun, RunID: run.ID, NodeID: instance.NodeID,
			Status: instance.Status, Output: sofar})
	})
	if result.Fingerprint != "" {
		if err := r.Store.PinFingerprint(instance.NodeID, result.Fingerprint); err != nil {
			r.Log.Error("could not pin a host key", slog.Int64("node", instance.NodeID), slog.Any("err", err))
		}
	}
	instance.Output = result.Output
	if err != nil {
		return r.fail(instance, err.Error())
	}
	if result.ExitCode != 0 {
		// The exit code alone is a number to go and look up. What the machine
		// actually said is the answer nine times out of ten — "no such image",
		// "port is already allocated", "no docker compose on this machine" —
		// so the row carries it and the pane below carries the rest.
		return r.fail(instance, fmt.Sprintf("docker exited %d: %s", result.ExitCode, lastLine(result.Output)))
	}

	instance.Status = model.InstanceHealthCheck
	r.save(instance)
	node, err := r.Store.Node(instance.NodeID)
	if err != nil {
		return r.fail(instance, err.Error())
	}
	passed, summary := r.gate(ctx, template.ProbeURL(node))
	instance.Health = summary
	if !passed {
		return r.fail(instance, summary)
	}

	// The one write that moves what this machine is running. After the gate,
	// never before it: a tag that has not answered is not a tag to go back to.
	if err := r.Store.RecordDeployHealthy(instance.NodeID, template.ServiceName, run.Tag, template.ID, template.Version); err != nil {
		return r.fail(instance, "deployed, but guard could not record it: "+err.Error())
	}
	instance.Status = model.InstanceHealthy
	instance.FinishedAt = time.Now().UTC()
	r.save(instance)
	return true
}

// gate is what "healthy" means, and the whole of it.
//
// Three consecutive passes, five seconds apart, inside two minutes. Consecutive
// because a process that has not fallen over yet passes once; a deadline
// because a check that never resolves has to become a failure rather than a run
// stuck in health_check forever.
func (r *Runner) gate(ctx context.Context, target string) (bool, string) {
	if target == "" {
		// A machine guard has no address for cannot be proved, and a deploy
		// that treated "cannot check" as "passed" would be a gate in name only.
		return false, "there is no address to check this service on: give the machine an address, or the template a health path and port"
	}
	started := time.Now()
	deadline := started.Add(r.deadline)
	passes := 0
	last := ""
	for {
		check := r.Probe.Check(ctx, target)
		if check.OK {
			passes++
			if passes >= r.passes {
				return true, fmt.Sprintf("%d checks passed in %s", passes, round(time.Since(started)))
			}
		} else {
			// Back to zero. Two passes and a failure is not two-thirds of a
			// healthy service.
			passes = 0
			last = checkReason(check)
		}
		if !time.Now().Add(r.interval).Before(deadline) {
			if last == "" {
				last = fmt.Sprintf("only %d of %d checks passed", passes, r.passes)
			}
			return false, fmt.Sprintf("still not healthy after %s: %s", round(time.Since(started)), last)
		}
		select {
		case <-ctx.Done():
			return false, "guard stopped while this was being checked"
		case <-time.After(r.interval):
		}
	}
}

func checkReason(check model.Check) string {
	if check.Error != "" {
		return check.Error
	}
	if check.StatusCode > 0 {
		return fmt.Sprintf("answered %d", check.StatusCode)
	}
	return "no answer"
}

func round(d time.Duration) time.Duration { return d.Round(time.Second) }

// lastLine is the machine's final word, which is where a shell puts the reason
// it gave up. Empty output says so rather than trailing off.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimRight(output, "\n \t\r"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return "it printed nothing"
}

func orNone(tag string) string {
	if tag == "" {
		return "whatever it was running before"
	}
	return tag
}

// run is Stream where the runner can, Run where it cannot, with the callback
// throttled so a progress bar does not become a write per frame.
func (r *Runner) run(ctx context.Context, login remote.Login, command string,
	onProgress func(string), onFrame func(string)) (remote.Result, error) {
	streamer, ok := r.SSH.(Streamer)
	if !ok {
		return r.SSH.Run(ctx, login, command)
	}
	var mu sync.Mutex
	last := time.Time{}
	return streamer.Stream(ctx, login, command, func(sofar string) {
		// Two speeds on purpose. The frame goes out now, because somebody is
		// watching; the row is written once a second, because it is a database
		// and `compose pull` redraws a progress bar many times a second.
		if onFrame != nil {
			onFrame(sofar)
		}
		if onProgress == nil {
			return
		}
		mu.Lock()
		if time.Since(last) < progressEvery {
			mu.Unlock()
			return
		}
		last = time.Now()
		mu.Unlock()
		onProgress(sofar)
	})
}

// fail records why, and reports false so the caller reads as the sentence it is.
func (r *Runner) fail(instance *model.DeployInstance, reason string) bool {
	if r.stopping(instance.RunID) {
		// The container may well be up. Guard stopped before it could prove it,
		// and saying "failed" about a machine somebody chose to stop watching
		// is how a healthy box gets restarted at 3am.
		instance.Status = model.InstanceInterrupted
		instance.Error = "cancelled: the container may be running, guard stopped before it was proved"
		instance.FinishedAt = time.Now().UTC()
		r.save(instance)
		return false
	}
	instance.Status = model.InstanceFailed
	instance.Error = reason
	instance.FinishedAt = time.Now().UTC()
	r.save(instance)
	return false
}

func (r *Runner) save(instance *model.DeployInstance) {
	// Every write to the row is also a frame, so a watcher sees a machine move
	// from deploying to health_check the moment it happens rather than on the
	// next tick.
	//
	// Never Done: that word is about the *run*, and a watcher closes its
	// connection when it hears it. Setting it per machine ended the stream at
	// the first one to finish, so the second machine of a rolling deploy was
	// never watched — the whole thing this exists for.
	r.publish(Frame{Kind: KindRun, RunID: instance.RunID, NodeID: instance.NodeID,
		Status: instance.Status, Output: instance.Output})
	if err := r.Store.SetDeployInstance(*instance); err != nil {
		r.Log.Error("could not record a deploy's progress",
			slog.Int64("run", instance.RunID), slog.Int64("node", instance.NodeID), slog.Any("err", err))
	}
}

// tell delivers through the group's destination, and says so in the log when
// there is nobody to tell.
//
// A group with no destination is allowed — the same bargain a rule with no
// destination makes — but here it means a sequential run can stop and wait in
// silence, so the log says it plainly and the page says it where the
// destination is chosen.
func (r *Runner) tell(ctx context.Context, webhookID int64, run model.DeployRun,
	instance model.DeployInstance, state, title, message string) {
	if webhookID == 0 {
		r.Log.Warn("a deploy stopped and there is nobody to tell",
			slog.Int64("run", run.ID), slog.String("group", run.GroupName),
			slog.String("note", "give the group a destination on the deploys page"))
		return
	}
	if r.Sender == nil {
		return
	}
	destination, err := r.Store.DestinationFor(webhookID)
	if err != nil {
		r.Log.Error("a deploy could not read its destination", slog.Int64("run", run.ID), slog.Any("err", err))
		return
	}
	event := notify.Event{
		At:      time.Now().UTC(),
		Kind:    KindDeploy,
		Subject: fmt.Sprintf("%s/%s", run.GroupName, instance.NodeName),
		State:   state,
		Title:   title,
		Message: message,
		Fields: map[string]any{
			"run":     run.ID,
			"group":   run.GroupName,
			"machine": instance.NodeName,
			"image":   run.Image,
			"tag":     run.Tag,
			"was":     instance.PreviousTag,
			"error":   instance.Error,
		},
	}
	// Its own context: this is the alert about a deploy that is stuck, and it
	// must not inherit a cancellation from the thing it is reporting on.
	send, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := r.Sender.Send(send, destination, event); err != nil {
		r.Log.Error("a deploy could not tell anybody it stopped",
			slog.Int64("run", run.ID), slog.String("destination", destination.Name), slog.Any("err", err))
	}
}

// KindDeploy is what a receiver routes on. Declared here rather than in notify
// because the event belongs to this feature; notify owns the POST, not the
// vocabulary of everything that can raise one.
const KindDeploy = "deploy.stopped"
