package cluster

import (
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// A whole answer from a real-shaped machine: /proc, df, and a docker with two
// containers — one of them up but unhealthy, which is the case a card must not
// call green.
const sample = `#load
0.52 0.41 0.38 2/431 12345
#uptime
987654.32 1975308.64
#mem
MemTotal:        4030472 kB
MemFree:          210332 kB
MemAvailable:    2015236 kB
Buffers:          102400 kB
Cached:          1500000 kB
#cpu
cpu  100 0 50 800 50 0 0 0 0 0
#cpus
2
#disk
Filesystem     1024-blocks    Used Available Capacity Mounted on
/dev/vda1         51475068 20590027  28268041      43% /
#docker
api	registry.example/api:v2	Up 3 days
worker	registry.example/worker:v2	Up 2 hours (unhealthy)
`

func TestParseStatsReadsAWholeMachine(t *testing.T) {
	stats := ParseStats(sample)

	if stats.Load1 != 0.52 || stats.Load15 != 0.38 {
		t.Errorf("load = %v %v", stats.Load1, stats.Load15)
	}
	if stats.UptimeSeconds != 987654.32 {
		t.Errorf("uptime = %v", stats.UptimeSeconds)
	}
	if stats.CPUCount != 2 {
		t.Errorf("cpus = %d", stats.CPUCount)
	}
	// Used is total minus *available*, not total minus free: a healthy Linux
	// spends everything spare on cache and hands it back on demand, and the
	// free number is what makes people think a fine box is out of memory.
	if stats.MemTotalKB != 4030472 || stats.MemUsedKB != 4030472-2015236 {
		t.Errorf("memory = %d used of %d", stats.MemUsedKB, stats.MemTotalKB)
	}
	if stats.DiskTotalKB != 51475068 || stats.DiskUsedKB != 20590027 {
		t.Errorf("disk = %d used of %d", stats.DiskUsedKB, stats.DiskTotalKB)
	}
	// idle and iowait are both not-working: a box blocked on its disk is not
	// busy, and calling it busy points at the wrong thing.
	if stats.CPUTotal != 1000 || stats.CPUBusy != 150 {
		t.Errorf("cpu counters = %d busy of %d", stats.CPUBusy, stats.CPUTotal)
	}
	if len(stats.Containers) != 2 {
		t.Fatalf("containers = %#v", stats.Containers)
	}
	if !stats.Containers[0].Up || stats.Containers[0].Name != "api" {
		t.Errorf("first container = %#v", stats.Containers[0])
	}
	if stats.Containers[1].Up {
		t.Error("an unhealthy container was reported as up")
	}
	if stats.DockerError != "" {
		t.Errorf("docker error on a good answer: %q", stats.DockerError)
	}
}

// The docker line redirects stderr into stdout, because "no docker here" and
// "this login cannot reach the socket" are the two likely answers and both are
// things to tell the reader rather than leave them to infer from an empty list.
func TestParseStatsExplainsAMissingDocker(t *testing.T) {
	stats := ParseStats("#load\n0.1 0.1 0.1 1/1 1\n#docker\n" +
		"permission denied while trying to connect to the Docker daemon socket\n")
	if len(stats.Containers) != 0 {
		t.Fatalf("containers = %#v", stats.Containers)
	}
	if stats.DockerError == "" {
		t.Fatal("a refused docker socket was reported as no containers at all")
	}
}

// A machine that answers half the sections is a partial reading, not a failed
// one: a df that failed should cost the disk figure and nothing else.
func TestParseStatsSurvivesMissingSections(t *testing.T) {
	stats := ParseStats("#mem\nMemTotal: 1000 kB\nMemAvailable: 400 kB\n#disk\n")
	if stats.MemUsedKB != 600 {
		t.Errorf("memory = %d", stats.MemUsedKB)
	}
	if stats.DiskTotalKB != 0 || stats.DiskPercent() != 0 {
		t.Errorf("a missing df invented a disk: %d", stats.DiskTotalKB)
	}
}

// CPU is a rate, so it needs two readings — and has to refuse the pairs that
// cannot answer rather than inventing a number from them.
func TestCPUPercentNeedsTwoGoodReadings(t *testing.T) {
	first := model.HostStats{CPUBusy: 100, CPUTotal: 1000}
	second := model.HostStats{CPUBusy: 400, CPUTotal: 2000}
	percent, ok := cpuPercent(first, second)
	if !ok || percent != 30 {
		t.Fatalf("percent = %v (%v), want 30", percent, ok)
	}
	if _, ok := cpuPercent(model.HostStats{}, second); ok {
		t.Error("a first sample produced a percentage out of nothing")
	}
	// A rebooted machine counts from zero again. That is a gap, not 0% busy.
	if _, ok := cpuPercent(second, first); ok {
		t.Error("counters that went backwards produced a percentage")
	}
}

func TestPercentagesRefuseToDivideByZero(t *testing.T) {
	empty := model.HostStats{}
	if empty.MemPercent() != 0 || empty.DiskPercent() != 0 {
		t.Fatal("an empty sample produced percentages")
	}
	full := model.HostStats{MemUsedKB: 512, MemTotalKB: 1024}
	if full.MemPercent() != 50 {
		t.Fatalf("memory percent = %v", full.MemPercent())
	}
}
