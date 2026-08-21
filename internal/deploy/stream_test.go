package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// talkingSSH prints in pieces, like a real pull does.
type talkingSSH struct{ chunks []string }

func (e *talkingSSH) Run(context.Context, remote.Login, string) (remote.Result, error) {
	return remote.Result{Output: strings.Join(e.chunks, "")}, nil
}

func (e *talkingSSH) Stream(_ context.Context, _ remote.Login, _ string, onChunk func(string)) (remote.Result, error) {
	sofar := ""
	for _, chunk := range e.chunks {
		sofar += chunk
		onChunk(sofar)
		time.Sleep(2 * time.Millisecond)
	}
	return remote.Result{Output: sofar}, nil
}

func TestAWatcherSeesOutputAsItArrives(t *testing.T) {
	runner, _, _, _ := harness(t, 1)
	runner.SSH = &talkingSSH{chunks: []string{"pulling…\n", "extracting…\n", "done\n"}}

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	frames, stop := runner.Watch(KindRun, run.ID)
	defer stop()

	seen := []string{}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-frames:
			seen = append(seen, frame.Output)
			if frame.Done {
				// Something arrived before the end, which is the whole point:
				// a pane that only fills in when the deploy finishes is a
				// spinner with extra steps.
				partial := false
				for _, output := range seen {
					if strings.Contains(output, "pulling") && !strings.Contains(output, "done") {
						partial = true
					}
				}
				if !partial {
					t.Fatalf("nothing arrived mid-command: %q", seen)
				}
				return
			}
		case <-deadline:
			t.Fatalf("no final frame; saw %q", seen)
		}
	}
}

func TestASlowWatcherNeverStallsADeploy(t *testing.T) {
	runner, store, _, _ := harness(t, 1)
	chunks := make([]string, 200)
	for i := range chunks {
		chunks[i] = "layer\n"
	}
	runner.SSH = &talkingSSH{chunks: chunks}

	// Subscribed and then never read. A publisher that blocked on this would
	// stop the deploy dead, which is the worst bug this file could have.
	_, stop := runner.Watch(KindRun, 1)
	defer stop()

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunHealthy, model.RunFailed)
}

// hangingSSH never returns until its context is cancelled, which is what a
// machine in the middle of a slow pull looks like from here.
type hangingSSH struct{ started chan struct{} }

func (e *hangingSSH) Run(ctx context.Context, _ remote.Login, _ string) (remote.Result, error) {
	select {
	case e.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return remote.Result{Output: "…"}, ctx.Err()
}

func TestStoppingARunningDeployCutsIt(t *testing.T) {
	runner, store, _, _ := harness(t, 2)
	runner.SSH = &hangingSSH{started: make(chan struct{}, 1)}

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(runner.Deploying()) > 0 })
	if err := runner.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}

	// It is cancelled, not failed: nothing broke, somebody decided.
	finished := settle(t, store, run.ID, model.RunCancelled)
	// The machine in flight says the honest thing rather than "failed" — its
	// container may well be up, guard just stopped before it could prove it.
	if finished.Instances[0].Status != model.InstanceInterrupted {
		t.Fatalf("the machine in flight is %q", finished.Instances[0].Status)
	}
	if !strings.Contains(finished.Instances[0].Error, "cancelled") {
		t.Fatalf("it says %q", finished.Instances[0].Error)
	}
	// And the one it never reached was never touched.
	if finished.Instances[1].Status != model.InstanceSkipped {
		t.Fatalf("the untouched machine is %q", finished.Instances[1].Status)
	}
	// The locks came back, so the machines can be deployed to again.
	waitFor(t, func() bool { return len(runner.Deploying()) == 0 })
}

func TestStoppingAFinishedDeployIsRefused(t *testing.T) {
	runner, store, _, _ := harness(t, 1)
	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunHealthy, model.RunFailed)
	// A button that says "stop" over a deploy that ended four hours ago is a
	// button that gets pressed by accident.
	if err := runner.Cancel(run.ID); err == nil {
		t.Fatal("a finished deploy was cancelled")
	}
}

func TestStoppingARunWaitingOnSomebodyWorksToo(t *testing.T) {
	runner, store, _, _ := harness(t, 3)
	runner.SSH = &countingSSH{fail: 1}

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunAwaiting)
	// One button for both states: a run stopped at a failure and a run still
	// going are both "make this stop" to the person looking at it.
	if err := runner.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	settle(t, store, run.ID, model.RunCancelled)
}

func TestTheStreamLastsTheWholeRun(t *testing.T) {
	// The first version of this marked a frame "done" when a *machine*
	// finished, and the browser closes its connection when it hears that — so
	// the second machine of a rolling deploy was never watched, which is the
	// whole thing the stream exists for.
	runner, store, _, _ := harness(t, 3)
	frames, stop := runner.Watch(KindRun, 1)
	defer stop()

	run, err := runner.Start(Request{GroupID: 1, TemplateID: 1, Tag: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	dones := 0
	deadline := time.After(3 * time.Second)
	for {
		select {
		case frame := <-frames:
			if frame.NodeID != 0 {
				seen[frame.NodeID] = true
			}
			if frame.Done {
				dones++
				if len(seen) != 3 {
					t.Fatalf("the run ended having reported %d machines, not 3", len(seen))
				}
				if dones != 1 {
					t.Fatalf("%d frames claimed the run was over", dones)
				}
				// And the row is finished by the time the frame lands, so a
				// watcher that goes and reads it gets the final status.
				finished, err := store.DeployRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if finished.Status == model.RunRunning {
					t.Fatal("the done frame arrived before the run status was written")
				}
				return
			}
		case <-deadline:
			t.Fatalf("no frame said the run was over; saw %d machines", len(seen))
		}
	}
}
