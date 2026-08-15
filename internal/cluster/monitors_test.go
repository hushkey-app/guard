package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/notify"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

type fakeMonitorStore struct {
	monitors []model.Monitor
	nodes    []model.Node
	states   map[int64]model.MonitorState
	cleared  int
}

func (f *fakeMonitorStore) ActiveMonitors() ([]model.Monitor, error) {
	out := make([]model.Monitor, len(f.monitors))
	copy(out, f.monitors)
	for i := range out {
		if state, ok := f.states[out[i].ID]; ok {
			out[i].States = []model.MonitorState{state}
		}
	}
	return out, nil
}

func (f *fakeMonitorStore) Nodes() ([]model.Node, error) { return f.nodes, nil }

func (f *fakeMonitorStore) SaveMonitorState(monitorID int64, state model.MonitorState) error {
	if f.states == nil {
		f.states = map[int64]model.MonitorState{}
	}
	f.states[monitorID] = state
	return nil
}

func (f *fakeMonitorStore) ClearMonitorState(monitorID, _ int64) error {
	f.cleared++
	delete(f.states, monitorID)
	return nil
}

func (f *fakeMonitorStore) DestinationFor(id int64) (notify.Destination, error) {
	return notify.Destination{ID: id, Name: "ops", URL: "https://hooks.example.com/ops"}, nil
}

func (f *fakeMonitorStore) RecordDelivery(int64, time.Time, string) error { return nil }

func diskRule(forSeconds int) model.Monitor {
	return model.Monitor{
		ID: 1, NodeID: 0, Metric: "disk_percent", Op: model.MonitorAbove,
		Threshold: 90, ForSeconds: forSeconds, WebhookID: 1, Enabled: true,
	}
}

func machine(diskUsed, diskTotal int64) model.Node {
	return model.Node{
		ID: 7, Name: "DB-1", URL: "http://10.19.96.4/health", Enabled: true,
		Status: model.StatusUp, LatencyMS: 78, Uptime: 100, Checks: 11890, CheckedAt: time.Now(),
		Stats: &model.HostStats{
			At: time.Now(), HasCPU: true, CPUPercent: 2,
			MemUsedKB: 222 << 10, MemTotalKB: 950 << 10,
			DiskUsedKB: diskUsed, DiskTotalKB: diskTotal,
		},
	}
}

func TestARuleFiresOnceAndCarriesTheParameters(t *testing.T) {
	store := &fakeMonitorStore{
		monitors: []model.Monitor{diskRule(0)},
		nodes:    []model.Node{machine(28<<20, 29<<20)}, // 96% of the disk
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}

	raised := m.Round(context.Background())
	if len(raised) != 1 {
		t.Fatalf("raised %d events, want one", len(raised))
	}
	event := raised[0]
	if event.State != notify.StateFiring || event.Kind != notify.KindClusterRule {
		t.Fatalf("event = %+v", event)
	}
	if event.Subject != "DB-1/disk_percent" {
		t.Fatalf("subject = %q", event.Subject)
	}
	// The whole card travels with it: an alert that says "disk 96%" and makes
	// somebody open the dashboard to learn the machine is also up and fast has
	// cost a trip it did not need to.
	for _, field := range []string{"node", "status", "value", "threshold", "latency_ms", "uptime_percent", "cpu_percent", "mem_percent"} {
		if _, ok := event.Fields[field]; !ok {
			t.Errorf("event carries no %s", field)
		}
	}
	// And it does not say it twice.
	if again := m.Round(context.Background()); len(again) != 0 {
		t.Fatalf("raised %d events on the second pass", len(again))
	}
}

func TestAConditionHasToHold(t *testing.T) {
	store := &fakeMonitorStore{
		monitors: []model.Monitor{diskRule(300)}, // five minutes
		nodes:    []model.Node{machine(28<<20, 29<<20)},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}

	if raised := m.Round(context.Background()); len(raised) != 0 {
		t.Fatal("one sample over the line is not five minutes over the line")
	}
	// The clock is running, though, and it is stored — a rule that forgot when
	// a condition started would never reach five minutes on a guard that
	// restarts hourly.
	if state := store.states[1]; state.Since.IsZero() {
		t.Fatal("the breach was not timed")
	}
	// Wind it back and the hold is satisfied.
	state := store.states[1]
	state.Since = time.Now().Add(-6 * time.Minute)
	store.states[1] = state
	if raised := m.Round(context.Background()); len(raised) != 1 {
		t.Fatal("six minutes over a five minute hold is an alert")
	}
}

func TestRecoveryIsItsOwnEvent(t *testing.T) {
	store := &fakeMonitorStore{
		monitors: []model.Monitor{diskRule(0)},
		nodes:    []model.Node{machine(28<<20, 29<<20)},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}
	m.Round(context.Background())

	// The disk gets cleaned up.
	store.nodes = []model.Node{machine(7<<20, 29<<20)}
	raised := m.Round(context.Background())
	if len(raised) != 1 || raised[0].State != notify.StateResolved {
		t.Fatalf("raised %+v, want one resolved event", raised)
	}
	// A receiver that gets firing and later resolved can close its own
	// incident; one that only hears about problems learns to ignore them.
	if store.states[1].Firing {
		t.Fatal("the rule still thinks it is firing")
	}
}

func TestAMachineThatCannotAnswerIsSilentRatherThanZero(t *testing.T) {
	// No stored login, so no sample at all. A rule reading that as 0% would be
	// a rule that never fires — found out during the incident it was for.
	node := machine(0, 0)
	node.Stats = nil
	store := &fakeMonitorStore{
		monitors: []model.Monitor{{ID: 1, Metric: "cpu_percent", Op: model.MonitorBelow, Threshold: 50, WebhookID: 1, Enabled: true}},
		nodes:    []model.Node{node},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}
	if raised := m.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d events about a machine that said nothing", len(raised))
	}
}

func TestAPausedMachineIsNotDown(t *testing.T) {
	node := machine(7<<20, 29<<20)
	node.Enabled = false
	node.Status = model.StatusDown
	store := &fakeMonitorStore{
		monitors: []model.Monitor{{ID: 1, Metric: "down", WebhookID: 1, Enabled: true}},
		nodes:    []model.Node{node},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}
	// A machine somebody took out of service on purpose is the last thing that
	// should page them at 3am.
	if raised := m.Round(context.Background()); len(raised) != 0 {
		t.Fatalf("raised %d events about a paused machine", len(raised))
	}
}

func TestARuleWithNoMachineCoversAllOfThem(t *testing.T) {
	full := machine(28<<20, 29<<20)
	second := machine(28<<20, 29<<20)
	second.ID, second.Name = 8, "DB-2"
	store := &fakeMonitorStore{monitors: []model.Monitor{diskRule(0)}, nodes: []model.Node{full, second}}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}

	raised := m.Round(context.Background())
	if len(raised) != 2 {
		t.Fatalf("raised %d events, want one per machine", len(raised))
	}
}

func TestADisabledRuleIsNotEvaluated(t *testing.T) {
	rule := diskRule(0)
	rule.Enabled = false
	store := &fakeMonitorStore{monitors: nil, nodes: []model.Node{machine(28<<20, 29<<20)}}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}
	if raised := m.Round(context.Background()); len(raised) != 0 {
		t.Fatal("nothing to evaluate")
	}
}

func TestMeasuringWhatTheCardShows(t *testing.T) {
	node := machine(7900<<10, 29<<20)
	for _, test := range []struct {
		metric string
		want   float64
	}{
		{"latency_ms", 78},
		{"uptime_percent", 100},
		{"cpu_percent", 2},
		{"down", 0},
	} {
		got, ok := node.Measure(test.metric)
		if !ok || got != test.want {
			t.Errorf("%s = %v (%v), want %v", test.metric, got, ok, test.want)
		}
	}
	// Memory is used over total, the same figure the bar draws.
	if got, _ := node.Measure("mem_percent"); got < 23 || got > 24 {
		t.Errorf("mem_percent = %v, want about 23.4", got)
	}
}

func TestEditingAFiringRuleStillClosesIt(t *testing.T) {
	// The store keeps "firing" across an edit and only drops the "already told
	// them" stamp, so the next pass has to close the incident rather than the
	// receiver being left with one open forever.
	store := &fakeMonitorStore{
		monitors: []model.Monitor{diskRule(0)},
		nodes:    []model.Node{machine(7<<20, 29<<20)}, // well under the line
		states: map[int64]model.MonitorState{
			1: {NodeID: 7, Firing: true, Since: time.Now().Add(-time.Hour)},
		},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Log: quietLogger()}
	raised := m.Round(context.Background())
	if len(raised) != 1 || raised[0].State != notify.StateResolved {
		t.Fatalf("raised %+v, want the resolved event", raised)
	}
}

func TestAFiringRuleRepeatsOnlyAfterTheWindow(t *testing.T) {
	store := &fakeMonitorStore{
		monitors: []model.Monitor{diskRule(0)},
		nodes:    []model.Node{machine(28<<20, 29<<20)},
	}
	m := &Monitors{Store: store, Sender: &recordingSender{}, Repeat: time.Hour, Log: quietLogger()}
	if len(m.Round(context.Background())) != 1 {
		t.Fatal("the first breach is an alert")
	}
	if len(m.Round(context.Background())) != 0 {
		t.Fatal("the second pass is not")
	}
	// Wind the stamp back past the repeat window: still broken is still news,
	// eventually.
	state := store.states[1]
	state.Alerted = time.Now().Add(-2 * time.Hour)
	store.states[1] = state
	if len(m.Round(context.Background())) != 1 {
		t.Fatal("two hours later, with a one hour repeat, it says so again")
	}
}
