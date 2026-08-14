package cluster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type fakeWatchStore struct {
	actions  []model.NodeAction
	alerted  map[int64]time.Time
	failMark bool
}

func (f *fakeWatchStore) WatchedActions() ([]model.NodeAction, error) {
	return append([]model.NodeAction(nil), f.actions...), nil
}

func (f *fakeWatchStore) Node(id int64) (model.Node, error) {
	return model.Node{ID: id, Name: "VPS-1"}, nil
}

func (f *fakeWatchStore) MarkAlerted(actionID int64, at time.Time) error {
	if f.failMark {
		return errors.New("no")
	}
	if f.alerted == nil {
		f.alerted = map[int64]time.Time{}
	}
	f.alerted[actionID] = at
	return nil
}

type recordingNotifier struct {
	sent []Alert
	err  error
}

func (r *recordingNotifier) Notify(_ context.Context, alert Alert) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, alert)
	return nil
}

func staleAction() model.NodeAction {
	return model.NodeAction{
		ID: 1, NodeID: 7, Name: "Dump to R2", Schedule: "0 */6 * * *",
		StaleAfterSeconds: int((7 * time.Hour).Seconds()),
		// Ran five minutes ago and failed; last worked nine hours ago. This is
		// the shape the watch exists for — the last run is fine, the last
		// success is not.
		LastRunAt: time.Now().Add(-5 * time.Minute),
		LastError: "exit 1",
		LastOKAt:  time.Now().Add(-9 * time.Hour),
	}
}

func TestWatchdogReportsAJobThatHasNotSucceeded(t *testing.T) {
	store := &fakeWatchStore{actions: []model.NodeAction{staleAction()}}
	notifier := &recordingNotifier{}
	w := &Watchdog{Store: store, Notifier: notifier, Log: quietLogger()}

	raised := w.Round(context.Background())
	if len(raised) != 1 || len(notifier.sent) != 1 {
		t.Fatalf("raised %d, sent %d, want one of each", len(raised), len(notifier.sent))
	}
	alert := notifier.sent[0]
	if alert.Node != "VPS-1" || alert.Action != "Dump to R2" {
		t.Fatalf("alert = %+v, want the machine and the command named", alert)
	}
	if alert.Message == "" || alert.LastOKAt.IsZero() {
		t.Fatal("an alert says when it last worked, not just that it is late")
	}
	if _, ok := store.alerted[1]; !ok {
		t.Fatal("a delivered alert is recorded, or it repeats every pass")
	}
}

func TestWatchdogStaysQuietInsideTheRepeatWindow(t *testing.T) {
	action := staleAction()
	action.AlertedAt = time.Now().Add(-time.Hour)
	store := &fakeWatchStore{actions: []model.NodeAction{action}}
	notifier := &recordingNotifier{}
	w := &Watchdog{Store: store, Notifier: notifier, Repeat: 6 * time.Hour, Log: quietLogger()}

	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatal("a job reported an hour ago should not be reported again yet")
	}
	// And speaks again once the window has passed: still broken is still news,
	// eventually.
	action.AlertedAt = time.Now().Add(-7 * time.Hour)
	store.actions = []model.NodeAction{action}
	if raised := w.Round(context.Background()); len(raised) != 1 {
		t.Fatal("a job still broken six hours later is worth saying again")
	}
}

func TestWatchdogLeavesAHealthyJobAlone(t *testing.T) {
	action := staleAction()
	action.LastOKAt = time.Now().Add(-time.Hour)
	store := &fakeWatchStore{actions: []model.NodeAction{action}}
	notifier := &recordingNotifier{}
	w := &Watchdog{Store: store, Notifier: notifier, Log: quietLogger()}

	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d alerts about a job that worked an hour ago", len(raised))
	}
	if len(store.alerted) != 0 {
		t.Fatal("nothing to record")
	}
}

func TestAFailedDeliveryIsRetriedNextPass(t *testing.T) {
	store := &fakeWatchStore{actions: []model.NodeAction{staleAction()}}
	notifier := &recordingNotifier{err: errors.New("webhook down")}
	w := &Watchdog{Store: store, Notifier: notifier, Log: quietLogger()}

	w.Round(context.Background())
	if len(store.alerted) != 0 {
		t.Fatal("an alert nobody received must not be marked as sent")
	}
}

func TestWatchdogWatchesJobsWithNoScheduleToo(t *testing.T) {
	// A job run by CI or by a person is exactly as capable of quietly
	// stopping, which is why the threshold stands on its own.
	action := staleAction()
	action.Schedule = ""
	store := &fakeWatchStore{actions: []model.NodeAction{action}}
	notifier := &recordingNotifier{}
	w := &Watchdog{Store: store, Notifier: notifier, Log: quietLogger()}

	if raised := w.Round(context.Background()); len(raised) != 1 {
		t.Fatal("an unscheduled job with a threshold is still watched")
	}
}
