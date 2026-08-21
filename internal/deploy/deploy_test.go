package deploy

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// fakeStore is the contract with the interesting parts made settable: which
// machines are locked, what the vault resolves to, and what was written.
type fakeStore struct {
	mu          sync.Mutex
	nodes       map[int64]model.Node
	locked      map[int64]bool
	template    model.DeployTemplate
	group       model.DeployGroup
	vars        []model.NodeEnvVar
	varsErr     error
	runs        map[int64]*model.DeployRun
	nextRun     int64
	healthy     []string
	state       map[string]string
	swept       int
	destErr     error
	destination notify.Destination
}

func newStore() *fakeStore {
	return &fakeStore{
		nodes:   map[int64]model.Node{},
		locked:  map[int64]bool{},
		runs:    map[int64]*model.DeployRun{},
		state:   map[string]string{},
		nextRun: 1,
	}
}

func (s *fakeStore) Node(id int64) (model.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodes[id]
	if !ok {
		return model.Node{}, errors.New("no such machine")
	}
	return node, nil
}

func (s *fakeStore) DeployGroup(int64) (model.DeployGroup, error) { return s.group, nil }

func (s *fakeStore) DeployTemplate(int64, int) (model.DeployTemplate, error) { return s.template, nil }

func (s *fakeStore) DeployTarget(nodeID int64) (remote.Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked[nodeID] {
		return remote.Login{}, errors.New("this machine is locked: nothing can be deployed to it")
	}
	return remote.Login{User: "guard", Address: "10.0.0.1:22", Password: "x"}, nil
}

func (s *fakeStore) ResolveDeployVars(model.DeployTemplate) ([]model.NodeEnvVar, error) {
	return s.vars, s.varsErr
}

func (s *fakeStore) DeployStateFor(nodeID int64, service string) (model.DeployState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return model.DeployState{NodeID: nodeID, ServiceName: service, CurrentTag: s.state[service]}, nil
}

func (s *fakeStore) CreateDeployRun(run model.DeployRun) (model.DeployRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.ID = s.nextRun
	s.nextRun++
	run.Status = model.RunRunning
	for i := range run.Instances {
		run.Instances[i].RunID = run.ID
		run.Instances[i].Status = model.InstancePending
	}
	stored := run
	s.runs[run.ID] = &stored
	return run, nil
}

func (s *fakeStore) DeployRun(id int64) (model.DeployRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return model.DeployRun{}, errors.New("no such run")
	}
	return *run, nil
}

func (s *fakeStore) SetDeployInstance(instance model.DeployInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[instance.RunID]
	for i := range run.Instances {
		if run.Instances[i].NodeID == instance.NodeID {
			run.Instances[i] = instance
		}
	}
	return nil
}

func (s *fakeStore) SetDeployRunStatus(runID int64, status string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[runID].Status = status
	return nil
}

func (s *fakeStore) RecordDeployHealthy(nodeID int64, service, tag string, _ int64, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy = append(s.healthy, service+"/"+tag)
	s.state[service] = tag
	return nil
}

func (s *fakeStore) DestinationFor(int64) (notify.Destination, error) {
	return s.destination, s.destErr
}

func (s *fakeStore) PinFingerprint(int64, string) error { return nil }

func (s *fakeStore) SweepDeployRuns() (int, error) { return s.swept, nil }

// fakeSSH records what each machine was asked to run, and can be told to fail.
type fakeSSH struct {
	mu       sync.Mutex
	commands []string
	fail     map[string]bool
	exit     int
}

func (e *fakeSSH) Run(_ context.Context, login remote.Login, command string) (remote.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands = append(e.commands, command)
	if e.fail[login.Address] {
		return remote.Result{Output: "no such image", ExitCode: 1}, nil
	}
	return remote.Result{Output: "recreated", ExitCode: e.exit}, nil
}

func (e *fakeSSH) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.commands)
}

// fakeProbe answers a scripted sequence, then repeats its last answer.
type fakeProbe struct {
	mu      sync.Mutex
	answers []bool
	seen    int
}

func (p *fakeProbe) Check(context.Context, string) model.Check {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := false
	if len(p.answers) > 0 {
		if p.seen < len(p.answers) {
			ok = p.answers[p.seen]
		} else {
			ok = p.answers[len(p.answers)-1]
		}
	}
	p.seen++
	return model.Check{OK: ok, StatusCode: 200, CheckedAt: time.Now()}
}

func (p *fakeProbe) checks() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen
}

func harness(t *testing.T, machines int) (*Runner, *fakeStore, *fakeSSH, *fakeProbe) {
	t.Helper()
	store := newStore()
	store.template = model.DeployTemplate{
		ID: 1, Version: 1, Name: "pack", ServiceName: "app", Image: "r/app",
		Path: "/srv/pack", ComposeYAML: "services: {}", HealthPath: "/health",
	}
	for i := 1; i <= machines; i++ {
		id := int64(i)
		store.nodes[id] = model.Node{ID: id, Name: name(i), Domain: "http://10.0.0.1:8000"}
		store.group.Nodes = append(store.group.Nodes, model.DeployMember{NodeID: id, Name: name(i)})
	}
	ssh := &fakeSSH{fail: map[string]bool{}}
	probe := &fakeProbe{answers: []bool{true}}
	runner := &Runner{Store: store, SSH: ssh, Probe: probe, Sender: notify.Discard{}}
	runner.prepare()
	// The gate's shape, at test speed. Three passes is still three passes —
	// what shrinks is the wait between them, not the rule.
	runner.interval = time.Millisecond
	runner.deadline = 200 * time.Millisecond
	return runner, store, ssh, probe
}

func name(i int) string { return string(rune('A'+i-1)) + "-box" }

// settle waits for a run to reach a terminal state, or for it to be waiting.
func settle(t *testing.T, store *fakeStore, runID int64, want ...string) model.DeployRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.DeployRun(runID)
		if err == nil {
			for _, status := range want {
				if run.Status == status {
					return run
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, _ := store.DeployRun(runID)
	t.Fatalf("run never reached %v, it is %q", want, run.Status)
	return run
}

func TestASequentialDeployProvesEachMachineBeforeTheNext(t *testing.T) {
	runner, store, ssh, probe := harness(t, 3)

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2", Mode: model.ModeSequential})
	if err != nil {
		t.Fatal(err)
	}
	finished := settle(t, store, run.ID, model.RunHealthy, model.RunFailed)
	if finished.Status != model.RunHealthy {
		t.Fatalf("run is %q: %+v", finished.Status, finished.Instances)
	}
	if ssh.count() != 3 {
		t.Fatalf("three machines, %d deploys", ssh.count())
	}
	// Three consecutive passes each: one is a process that has not fallen over
	// yet, not a service that works.
	if probe.checks() != 3*model.HealthPasses {
		t.Fatalf("%d checks for three machines", probe.checks())
	}
	if len(store.healthy) != 3 {
		t.Fatalf("recorded %v", store.healthy)
	}
}

func TestASequentialDeployStopsAndWaitsRatherThanCarryingOn(t *testing.T) {
	runner, store, _, _ := harness(t, 3)
	// The second machine's docker fails. Everything after it must stay
	// untouched — the whole reason to deploy one at a time.
	failing := &countingSSH{fail: 2}
	runner.SSH = failing

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2", Mode: model.ModeSequential})
	if err != nil {
		t.Fatal(err)
	}
	waiting := settle(t, store, run.ID, model.RunAwaiting)
	if waiting.Instances[0].Status != model.InstanceHealthy {
		t.Fatalf("first machine is %q", waiting.Instances[0].Status)
	}
	if waiting.Instances[1].Status != model.InstanceFailed {
		t.Fatalf("second machine is %q", waiting.Instances[1].Status)
	}
	if waiting.Instances[2].Status != model.InstancePending {
		t.Fatalf("the third machine was touched: %q", waiting.Instances[2].Status)
	}
	if failing.count() != 2 {
		t.Fatalf("%d machines were deployed to after a failure", failing.count())
	}

	// And it goes nowhere until somebody says so.
	if err := runner.Resolve(run.ID, DecisionSkip); err != nil {
		t.Fatal(err)
	}
	finished := settle(t, store, run.ID, model.RunFailed, model.RunHealthy)
	if finished.Instances[2].Status != model.InstanceHealthy {
		t.Fatalf("skip did not carry on: %+v", finished.Instances[2])
	}
	// The failed row stays failed: it is what happened.
	if finished.Instances[1].Status != model.InstanceFailed {
		t.Fatalf("the failed machine is %q", finished.Instances[1].Status)
	}
	if finished.Status != model.RunFailed {
		t.Fatalf("a run with a failed machine is %q", finished.Status)
	}
}

func TestStoppingLeavesTheRestUntouched(t *testing.T) {
	runner, store, _, _ := harness(t, 3)
	runner.SSH = &countingSSH{fail: 1}

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunAwaiting)
	if err := runner.Resolve(run.ID, DecisionStop); err != nil {
		t.Fatal(err)
	}
	finished := settle(t, store, run.ID, model.RunFailed)
	for _, instance := range finished.Instances[1:] {
		if instance.Status != model.InstanceSkipped {
			t.Fatalf("an untouched machine is %q", instance.Status)
		}
		if !strings.Contains(instance.Error, "stopped") {
			t.Fatalf("an untouched machine says %q", instance.Error)
		}
	}
}

func TestRetryDeploysTheSameMachineAgain(t *testing.T) {
	runner, store, _, _ := harness(t, 1)
	ssh := &countingSSH{fail: 1}
	runner.SSH = ssh

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunAwaiting)
	if err := runner.Resolve(run.ID, DecisionRetry); err != nil {
		t.Fatal(err)
	}
	finished := settle(t, store, run.ID, model.RunHealthy, model.RunFailed)
	if finished.Status != model.RunHealthy {
		t.Fatalf("retry left the run %q: %+v", finished.Status, finished.Instances[0])
	}
	if ssh.count() != 2 {
		t.Fatalf("retry ran %d deploys", ssh.count())
	}
}

func TestAnswerIsRefusedWhenNothingIsWaiting(t *testing.T) {
	runner, _, _, _ := harness(t, 1)
	// "I pressed retry and nothing happened" is the worst thing this could do.
	if err := runner.Resolve(99, DecisionRetry); err == nil {
		t.Fatal("a run that is not waiting accepted an answer")
	}
	if err := runner.Resolve(99, "rollback"); err == nil {
		t.Fatal("rollback is a deploy of another tag, not an answer to a stopped run")
	}
}

func TestAParallelDeployGatesNothing(t *testing.T) {
	runner, store, _, _ := harness(t, 3)
	// The first machine fails. In parallel mode the other two still go.
	runner.SSH = &countingSSH{fail: 1}

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2", Mode: model.ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	finished := settle(t, store, run.ID, model.RunFailed, model.RunHealthy)
	healthy, failed, pending := finished.Tally()
	if pending != 0 {
		t.Fatalf("parallel left %d machines untouched", pending)
	}
	if healthy != 2 || failed != 1 {
		t.Fatalf("tally is %d healthy, %d failed", healthy, failed)
	}
	// Health is still checked and still recorded — it just stops nothing.
	for _, instance := range finished.Instances {
		if instance.Status == model.InstanceHealthy && instance.Health == "" {
			t.Fatal("a parallel deploy recorded no health result")
		}
	}
}

func TestAMachineBeingDeployedToRefusesASecondDeploy(t *testing.T) {
	runner, store, _, _ := harness(t, 1)
	held := make(chan struct{})
	runner.SSH = &blockingSSH{release: held}

	first, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	// Fails loud rather than queueing: two people deploying different tags to
	// one machine is not something to serialise.
	waitFor(t, func() bool { return len(runner.Deploying()) == 1 })
	_, err = runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v3"})
	if err == nil {
		t.Fatal("a second deploy at a busy machine was accepted")
	}
	if !strings.Contains(err.Error(), "already being deployed") {
		t.Fatalf("refused with %q", err)
	}
	close(held)
	settle(t, store, first.ID, model.RunHealthy, model.RunFailed)
	// And the lock is given back.
	if len(runner.Deploying()) != 0 {
		t.Fatal("the lock outlived the deploy")
	}
}

func TestALockedMachineIsRefusedBeforeAnythingIsTouched(t *testing.T) {
	runner, store, ssh, _ := harness(t, 3)
	store.locked[3] = true

	// The third machine is locked. Nothing may be deployed at all: a rolling
	// deploy that stops at the third machine has already replaced two.
	if _, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"}); err == nil {
		t.Fatal("a group holding a locked machine was deployed")
	}
	if ssh.count() != 0 {
		t.Fatalf("%d machines were touched before the refusal", ssh.count())
	}
}

func TestAMissingVaultKeyStopsTheRunBeforeItStarts(t *testing.T) {
	runner, _, ssh, _ := harness(t, 2)
	runner.Store.(*fakeStore).varsErr = errors.New("DATABASE_URL is not in pack / production")

	if _, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"}); err == nil {
		t.Fatal("a deploy with an unresolvable variable started")
	}
	if ssh.count() != 0 {
		t.Fatal("a machine was touched before the values were known")
	}
}

func TestHealthyMeansThreeInARow(t *testing.T) {
	runner, _, _, _ := harness(t, 1)
	// Two passes, a failure, then passes. The failure resets the count: two
	// passes and a failure is not two-thirds of a healthy service.
	probe := &fakeProbe{answers: []bool{true, true, false, true, true, true}}
	runner.Probe = probe
	ok, summary := runner.gate(context.Background(), "http://box/health")
	if !ok {
		t.Fatalf("should have passed: %s", summary)
	}
	if probe.checks() != 6 {
		t.Fatalf("passed after %d checks", probe.checks())
	}
}

func TestAHealthCheckThatNeverResolvesIsAFailure(t *testing.T) {
	runner, _, _, _ := harness(t, 1)
	runner.Probe = &fakeProbe{answers: []bool{false}}
	// The deadline is what stops a run sitting in health_check forever. It is
	// two minutes in the model; the test proves the shape, not the clock.
	ok, summary := runner.gate(context.Background(), "http://box/health")
	if ok {
		t.Fatal("a service that never answered passed")
	}
	if summary == "" {
		t.Fatal("a failed gate said nothing about why")
	}
}

func TestAMachineWithNoAddressCannotPassAGate(t *testing.T) {
	runner, _, _, _ := harness(t, 1)
	// "Cannot check" is not "passed". A gate that treated it as one would be a
	// gate in name only.
	ok, summary := runner.gate(context.Background(), "")
	if ok {
		t.Fatal("a machine with nowhere to check passed its health gate")
	}
	if !strings.Contains(summary, "no address") {
		t.Fatalf("said %q", summary)
	}
}

func TestTheCommandWritesBothFilesAndNothingElse(t *testing.T) {
	template := model.DeployTemplate{
		ServiceName: "app", Path: "/srv/pack",
		ComposeYAML: "services:\n  app:\n    image: r/app:${TAG}\n",
	}
	command, err := Command(template, "v2", []model.NodeEnvVar{{Key: "LOG_LEVEL", Value: "info"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/srv/pack/docker-compose.yml", "/srv/pack/.env", "chmod 600", "chmod 644",
		"pull", "up -d", ".guard-bak"} {
		if !strings.Contains(command, want) {
			t.Fatalf("the command does not %q:\n%s", want, command)
		}
	}
	// The content travels as base64, so a compose file full of colons and
	// dollars has no quoting bug to have.
	if strings.Contains(command, "services:") {
		t.Fatal("file content reached the shell as text")
	}
}

func TestTheEnvLeadsWithTheTag(t *testing.T) {
	rendered := model.EnvFor("v2", []model.NodeEnvVar{{Key: "LOG_LEVEL", Value: "info"}})
	if !strings.HasPrefix(rendered, "TAG=v2\n") {
		t.Fatalf("rendered %q", rendered)
	}
}

func TestTheHealthTargetIsTheApplicationsNotTheMachines(t *testing.T) {
	node := model.Node{Domain: "http://10.0.0.1:8000", HealthPath: "/whatever-was-here-before"}
	template := model.DeployTemplate{HealthPath: "/health", HealthPort: 9000}
	if got := template.ProbeURL(node); got != "http://10.0.0.1:9000/health" {
		t.Fatalf("aimed at %q", got)
	}
	// With no path of its own it falls back to the machine's, which is the
	// ordinary case: one service on the box, already being watched.
	template = model.DeployTemplate{}
	if got := template.ProbeURL(node); got != "http://10.0.0.1:8000/whatever-was-here-before" {
		t.Fatalf("aimed at %q", got)
	}
}

// countingSSH fails the nth call and succeeds on every other.
type countingSSH struct {
	mu   sync.Mutex
	seen int
	fail int
}

func (e *countingSSH) Run(context.Context, remote.Login, string) (remote.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen++
	if e.seen == e.fail {
		return remote.Result{Output: "manifest unknown", ExitCode: 1}, nil
	}
	return remote.Result{Output: "recreated", ExitCode: 0}, nil
}

func (e *countingSSH) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seen
}

// blockingSSH holds the deploy open so the lock can be observed.
type blockingSSH struct{ release chan struct{} }

func (e *blockingSSH) Run(context.Context, remote.Login, string) (remote.Result, error) {
	<-e.release
	return remote.Result{Output: "recreated"}, nil
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// panickingStore fails the way a real one cannot be relied on not to: from
// inside the deploy goroutine, after the run has started.
type panickingStore struct {
	*fakeStore
	after int
	seen  int
}

func (s *panickingStore) SetDeployInstance(instance model.DeployInstance) error {
	s.seen++
	if s.seen == s.after {
		panic("something in here went wrong")
	}
	return s.fakeStore.SetDeployInstance(instance)
}

func TestACrashedDeployDoesNotTakeGuardWithIt(t *testing.T) {
	// The first version of this shipped with a log key the console handler
	// reads as an integer. A string under it panicked on the deploy's own
	// goroutine, and an unrecovered panic anywhere takes the process — so a
	// failed deploy stopped the telemetry, the health checks and the dashboard
	// somebody would have used to find out why.
	runner, store, _, _ := harness(t, 2)
	broken := &panickingStore{fakeStore: store, after: 2}
	runner.Store = broken

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	// The process is still here, the run says what became of it, and the locks
	// went back — which is the part that would otherwise wedge every later
	// deploy at those machines.
	settle(t, store, run.ID, model.RunFailed)
	waitFor(t, func() bool { return len(runner.Deploying()) == 0 })
}

func TestTheConsolesReservedKeysAreNotUsed(t *testing.T) {
	// The console handler reads `status`, `bytes` and `took` positionally and
	// calls Int64() on the first two. Anything logged under those keys from
	// here has to be an integer, and the simplest way to keep that true is not
	// to use them at all.
	source, err := os.ReadFile("deploy.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []string{`slog.String("status"`, `slog.String("bytes"`, `slog.String("method"`, `slog.String("path"`} {
		if strings.Contains(string(source), reserved) {
			t.Fatalf("%s is a key the console handler reads as something else", reserved)
		}
	}
}

func TestPreparingAMachineIsOneFixedCommand(t *testing.T) {
	command := PrepareCommand()
	// Idempotent first: a box that is already fine costs one round trip and
	// installs nothing.
	if !strings.HasPrefix(strings.Split(command, "\n")[1], "if docker compose version") {
		t.Fatalf("the already-installed branch is not first:\n%s", command)
	}
	for _, want := range []string{"docker-compose-plugin", "get.docker.com", "systemctl enable --now docker",
		"docker --version", "docker compose version"} {
		if !strings.Contains(command, want) {
			t.Fatalf("the command does not %q", want)
		}
	}
	// It takes no input, which is the whole reason it can be a button: there is
	// no argument anybody could shape into running something else.
	if strings.Contains(command, "%s") || strings.Contains(command, "%v") {
		t.Fatal("the prepare command interpolates something")
	}
}

func TestPreparingIsRefusedOnALockedMachine(t *testing.T) {
	runner, store, _, _ := harness(t, 1)
	store.locked[1] = true
	// It installs packages as root. That is exactly the class of thing a locked
	// machine exists to refuse.
	if _, err := runner.Prepare(1); err == nil {
		t.Fatal("a locked machine was prepared")
	}
}

func TestPreparingSaysWhetherItChangedAnything(t *testing.T) {
	runner, _, ssh, _ := harness(t, 1)
	ssh.exit = 0
	// It answers at once and installs behind the request, so the press does not
	// hold a connection open for a minute of apt.
	report, err := runner.Prepare(1)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Running {
		t.Fatal("prepare should report that it is still going")
	}
	waitFor(t, func() bool {
		now, ok := runner.Preparing(1)
		return ok && !now.Running
	})
	done, _ := runner.Preparing(1)
	// The fake says "recreated", not the already-installed marker, so this
	// counts as a change — the flag is read off the machine's own output rather
	// than guessed from an exit code.
	if !done.Changed {
		t.Fatalf("a machine that printed nothing familiar was reported as unchanged: %+v", done)
	}
}
