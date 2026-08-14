package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// fakeSampler answers one command with a fixed reply, so a test can be about
// what the collector does with the answer rather than about SSH.
type fakeSampler struct {
	output      string
	err         error
	fingerprint string
	command     string
}

func (f *fakeSampler) Run(_ context.Context, _ remote.Login, command string) (remote.Result, error) {
	f.command = command
	return remote.Result{Output: f.output, Fingerprint: f.fingerprint}, f.err
}

// fakeStatsStore is the store's half, in memory.
type fakeStatsStore struct {
	login       remote.Login
	loginErr    error
	last        model.HostStats
	hasLast     bool
	recorded    []model.HostStats
	pinned      string
	nodes       []model.Node
	recordCalls int
}

func (f *fakeStatsStore) NodesForStats() ([]model.Node, error) { return f.nodes, nil }
func (f *fakeStatsStore) Node(id int64) (model.Node, error)    { return model.Node{ID: id}, nil }
func (f *fakeStatsStore) SSHLoginFor(int64) (remote.Login, error) {
	return f.login, f.loginErr
}

func (f *fakeStatsStore) LastStats(int64) (model.HostStats, error) {
	if !f.hasLast {
		return model.HostStats{}, errors.New("no rows")
	}
	return f.last, nil
}

func (f *fakeStatsStore) RecordStats(_ int64, stats model.HostStats) error {
	f.recordCalls++
	f.recorded = append(f.recorded, stats)
	f.last, f.hasLast = stats, true
	return nil
}

func (f *fakeStatsStore) PinFingerprint(_ int64, fingerprint string) error {
	f.pinned = fingerprint
	return nil
}

func TestSampleNodeStoresWhatTheMachineSaid(t *testing.T) {
	store := &fakeStatsStore{login: remote.Login{User: "root", Address: "10.0.0.4:22"}}
	sampler := &fakeSampler{output: sample, fingerprint: "SHA256:abc"}
	collector := &Collector{Store: store, Runner: sampler}

	stats := collector.SampleNode(context.Background(), 1)
	if sampler.command != StatsCommand {
		t.Fatalf("ran %q — the sample must be guard's own fixed command", sampler.command)
	}
	if stats.MemTotalKB == 0 || len(stats.Containers) != 2 {
		t.Fatalf("parsed nothing useful: %#v", stats)
	}
	if stats.At.IsZero() || len(store.recorded) != 1 {
		t.Fatalf("recorded %d samples", len(store.recorded))
	}
	// The first sample has no CPU percentage: a rate needs two readings, and
	// a zero here would read as "idle" rather than "not measured yet".
	if stats.HasCPU {
		t.Error("a first sample invented a CPU percentage")
	}
	// The first connection pins the host key, exactly as a manual run does —
	// otherwise the pin would depend on which feature somebody used first.
	if store.pinned != "SHA256:abc" {
		t.Errorf("host key pinned as %q", store.pinned)
	}

	// The second sample has the first to compare against — and counters that
	// have actually moved. Two readings of the same instant are not a rate,
	// which is why the fake has to advance them for this to answer at all.
	sampler.output = strings.Replace(sample,
		"cpu  100 0 50 800 50 0 0 0 0 0",
		"cpu  200 0 100 1500 200 0 0 0 0 0", 1)
	second := collector.SampleNode(context.Background(), 1)
	if !second.HasCPU {
		t.Fatal("a second sample still had no CPU percentage")
	}
	// 150 busy jiffies of 1,000 elapsed.
	if second.CPUPercent != 15 {
		t.Fatalf("cpu = %v%%, want 15", second.CPUPercent)
	}
}

// A machine that cannot be asked stores that fact. A card that quietly kept
// showing the last good numbers would be lying by omission, and "guard has
// not been able to get in since 04:12" is the answer somebody needs.
func TestAFailedSampleIsStoredAsOne(t *testing.T) {
	store := &fakeStatsStore{loginErr: errors.New("this machine has no stored password")}
	collector := &Collector{Store: store, Runner: &fakeSampler{}}

	stats := collector.SampleNode(context.Background(), 1)
	if stats.Error == "" || len(store.recorded) != 1 || store.recorded[0].Error == "" {
		t.Fatalf("a refused sample was not recorded as one: %#v", store.recorded)
	}

	store = &fakeStatsStore{login: remote.Login{User: "root", Address: "10.0.0.4:22"}}
	collector = &Collector{Store: store, Runner: &fakeSampler{err: errors.New("timed out")}}
	if stats := collector.SampleNode(context.Background(), 1); stats.Error != "timed out" {
		t.Fatalf("a timeout was recorded as %q", stats.Error)
	}
}

// The loop samples what is due and leaves alone what is not — including every
// machine that turned sampling off, which is what a zero cadence means.
func TestRoundOnlySamplesWhatIsDue(t *testing.T) {
	store := &fakeStatsStore{
		login: remote.Login{User: "root", Address: "10.0.0.4:22"},
		nodes: []model.Node{
			{ID: 1, Enabled: true, StatsIntervalSeconds: 60},
			{ID: 2, Enabled: true, StatsIntervalSeconds: 0},
			{ID: 3, Enabled: false, StatsIntervalSeconds: 60},
		},
	}
	collector := &Collector{Store: store, Runner: &fakeSampler{output: sample}}
	wait := collector.Round(context.Background())
	if store.recordCalls != 1 {
		t.Fatalf("sampled %d machines, want only the due one", store.recordCalls)
	}
	if wait <= 0 {
		t.Fatalf("next pass in %s", wait)
	}
}
