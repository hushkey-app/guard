package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type fakeWatchStore struct {
	actions   []model.NodeAction
	alerted   map[int64]time.Time
	failMark  bool
	delivered []string
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

func (f *fakeWatchStore) DestinationFor(id int64) (notify.Destination, error) {
	return notify.Destination{ID: id, Name: "ops", URL: "https://hooks.example.com/ops"}, nil
}

func (f *fakeWatchStore) RecordDelivery(_ int64, _ time.Time, failure string) error {
	f.delivered = append(f.delivered, failure)
	return nil
}

// recordingSender stands in for the delivery module: the watch is tested on
// what it decides to say, not on whether a socket answered.
type recordingSender struct {
	sent []notify.Event
	err  error
}

func (r *recordingSender) Send(_ context.Context, _ notify.Destination, event notify.Event) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, event)
	return nil
}

// A command names the destination its alert goes to. There is no instance-wide
// fallback: the destinations are the named ones somebody added on
// /settings/alerts, and a command that names none is logged and nothing else.
const opsWebhook = 3

func staleAction() model.NodeAction {
	return model.NodeAction{
		ID: 1, NodeID: 7, Name: "Dump to R2", Schedule: "0 */6 * * *", WebhookID: opsWebhook,
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
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

	raised := w.Round(context.Background())
	if len(raised) != 1 || len(sender.sent) != 1 {
		t.Fatalf("raised %d, sent %d, want one of each", len(raised), len(sender.sent))
	}
	alert := sender.sent[0]
	if alert.Fields["node"] != "VPS-1" || alert.Fields["action"] != "Dump to R2" {
		t.Fatalf("alert = %+v, want the machine and the command named", alert.Fields)
	}
	if alert.Kind != notify.KindScheduleStale || alert.State != notify.StateFiring {
		t.Fatalf("alert = %+v, want a firing staleness event", alert)
	}
	if alert.Message == "" || alert.Fields["last_ok_at"] == nil {
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
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Repeat: 6 * time.Hour, Log: quietLogger()}

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
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

	if raised := w.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d alerts about a job that worked an hour ago", len(raised))
	}
	if len(store.alerted) != 0 {
		t.Fatal("nothing to record")
	}
}

func TestAFailedDeliveryIsRetriedNextPass(t *testing.T) {
	store := &fakeWatchStore{actions: []model.NodeAction{staleAction()}}
	sender := &recordingSender{err: errors.New("webhook down")}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

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
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

	if raised := w.Round(context.Background()); len(raised) != 1 {
		t.Fatal("an unscheduled job with a threshold is still watched")
	}
}

func TestNeverSucceededIsNullRatherThanTheYearOne(t *testing.T) {
	action := staleAction()
	action.LastOKAt = time.Time{}
	action.CreatedAt = time.Now().Add(-9 * time.Hour)
	store := &fakeWatchStore{actions: []model.NodeAction{action}}
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

	raised := w.Round(context.Background())
	if len(raised) != 1 {
		t.Fatalf("raised %d alerts", len(raised))
	}
	// The payload is read by somebody else's handler, and 0001-01-01 is not a
	// date anybody wants to special-case.
	body, err := json.Marshal(raised[0].Fields)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["last_ok_at"] != nil {
		t.Fatalf("last_ok_at = %v, want null", payload["last_ok_at"])
	}
}

// A command with no destination is logged and nothing else — and it counts as
// told, or every pass would re-log it. There used to be a GUARD_ALERT_WEBHOOK
// behind this; the named destinations on /settings/alerts are the whole answer
// now, and a second invisible one configured by an environment variable was a
// second answer to "where do alerts go".
func TestACommandWithNoDestinationIsLoggedAndNotRepeated(t *testing.T) {
	action := staleAction()
	action.WebhookID = 0
	store := &fakeWatchStore{actions: []model.NodeAction{action}}
	sender := &recordingSender{}
	w := &Watchdog{Store: store, Sender: sender, Log: quietLogger()}

	raised := w.Round(context.Background())
	if len(raised) != 1 {
		t.Fatalf("raised %d, want the event to have been raised", len(raised))
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent %d, want nothing sent anywhere", len(sender.sent))
	}
	if _, ok := store.alerted[action.ID]; !ok {
		t.Fatal("it must be marked as told, or the next pass logs it again")
	}
	if len(store.delivered) != 0 {
		t.Fatal("there was no delivery to record")
	}
}
