package hostinfo

import (
	"os"
	"path/filepath"
	"testing"
)

// Real files from a Vultr Ubuntu box, trimmed. The parsing is the part that can
// be wrong on somebody's machine and right on mine, so it is tested against
// what these files actually look like rather than against what is convenient.
const meminfoSample = `MemTotal:        2013456 kB
MemFree:          151264 kB
MemAvailable:    1402108 kB
Buffers:           76540 kB
Cached:          1102336 kB
SwapTotal:             0 kB
`

func TestMemoryCountsTheCacheAsAvailable(t *testing.T) {
	total, used, ok := memory(meminfoSample)
	if !ok {
		t.Fatal("a normal /proc/meminfo did not parse")
	}
	if total != 2013456 {
		t.Fatalf("total %d", total)
	}
	// Total minus available, not total minus free. Free here is 151 MB, which
	// would read as 92% used on a box that is actually 30% used — the number
	// somebody reboots a healthy machine over.
	if used != 2013456-1402108 {
		t.Fatalf("used %d, want %d", used, 2013456-1402108)
	}
}

// A kernel too old for MemAvailable would make used equal total. Saying nothing
// is better than saying "100% of memory used" on a box that is fine.
func TestMemoryWithoutMemAvailableIsUnmeasured(t *testing.T) {
	if _, _, ok := memory("MemTotal:  2013456 kB\nMemFree:  151264 kB\n"); ok {
		t.Fatal("memory was reported without MemAvailable")
	}
	if _, _, ok := memory("garbage\n"); ok {
		t.Fatal("garbage parsed")
	}
}

func TestCPUModelAcrossTheSpellingsThatOccur(t *testing.T) {
	for _, shape := range []struct{ what, raw, want string }{
		{"intel", "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) CPU E5-2690\nprocessor\t: 1\nmodel name\t: Intel(R) Xeon(R) CPU E5-2690\n", "Intel(R) Xeon(R) CPU E5-2690"},
		{"arm Model", "processor\t: 0\nModel\t\t: Neoverse-N1\n", "Neoverse-N1"},
		{"arm Hardware", "processor\t: 0\nHardware\t: BCM2835\n", "BCM2835"},
		{"nothing usable", "processor\t: 0\nflags\t: fpu vme\n", ""},
	} {
		if got := cpuModel(shape.raw); got != shape.want {
			t.Fatalf("%s: %q, want %q", shape.what, got, shape.want)
		}
	}
}

func TestLoadIsAllThreeOrNone(t *testing.T) {
	one, five, fifteen, ok := loadAverages("0.52 0.58 0.59 1/523 12345")
	if !ok || one != 0.52 || five != 0.58 || fifteen != 0.59 {
		t.Fatalf("%v %v %v ok=%v", one, five, fifteen, ok)
	}
	for _, bad := range []string{"", "0.52", "0.52 0.58", "x y z"} {
		if _, _, _, ok := loadAverages(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

// The second field is idle time summed over every core, which on a 10-core box
// is roughly ten times the uptime. Taking the wrong one is a box that claims to
// have been up for two months.
func TestUptimeTakesTheFirstField(t *testing.T) {
	if got := uptimeSeconds("532801.42 5301234.11"); got != 532801.42 {
		t.Fatalf("%v", got)
	}
	if got := uptimeSeconds(""); got != 0 {
		t.Fatalf("%v", got)
	}
}

func TestPrettyNameIsUnquoted(t *testing.T) {
	sample := `NAME="Ubuntu"
VERSION="24.04.1 LTS (Noble Numbat)"
PRETTY_NAME="Ubuntu 24.04.1 LTS"
ID=ubuntu
`
	if got := prettyName(sample); got != "Ubuntu 24.04.1 LTS" {
		t.Fatalf("%q", got)
	}
	if got := prettyName("ID=ubuntu\n"); got != "" {
		t.Fatalf("%q", got)
	}
}

// The database is three files once SQLite is running, and a WAL that has not
// checkpointed is a real and confusing chunk of somebody's disk.
func TestTheDatabaseSizeCountsTheWalAndShm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guard.db")
	for name, size := range map[string]int{"guard.db": 100, "guard.db-wal": 250, "guard.db-shm": 32} {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := databaseSize(path); got != 382 {
		t.Fatalf("%d bytes, want 382", got)
	}
	// A memory store has no file, and neither does a path that is not there.
	if got := databaseSize("memory"); got != 0 {
		t.Fatalf("%d", got)
	}
	if got := databaseSize(filepath.Join(dir, "absent.db")); got != 0 {
		t.Fatalf("%d", got)
	}
}

// The portable half has to answer on every platform, because it is what a
// development machine shows.
func TestReadAlwaysAnswersTheThingsItCanKnow(t *testing.T) {
	instance := Read("v1.2.3", "")
	if instance.Version != "v1.2.3" || instance.GoVersion == "" || instance.OS == "" || instance.Arch == "" {
		t.Fatalf("%+v", instance)
	}
	if instance.CPUCount < 1 || instance.Goroutines < 1 {
		t.Fatalf("%+v", instance)
	}
	// Unmeasurable is false rather than a zero somebody reads as a measurement.
	if instance.HasMemory && instance.MemTotalKB == 0 {
		t.Fatal("memory claimed to be measured and is zero")
	}
}
