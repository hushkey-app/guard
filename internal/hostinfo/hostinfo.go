// Package hostinfo is what guard can say about the box it is running on.
//
// Guard already samples every machine it *watches* over SSH
// (`internal/cluster/stats.go`) and says nothing at all about the one it is on,
// which is the box somebody is looking at the dashboard from. "Is guard itself
// short of memory" and "how big has the database got" are the two questions
// that turn a slow dashboard into an explanation, and until now the only way to
// answer them was to SSH in.
//
// Two rules, both borrowed from the collector because they are the reasons that
// one is trustworthy:
//
//   - **Unmeasurable is empty, never zero.** A field guard could not read is
//     absent, and the page draws a dash. A zero would read as "no memory used",
//     which is a number somebody acts on.
//   - **Nothing here is stored.** It is read when asked, because it describes
//     right now, and a sampled history of the box guard runs on is what the
//     telemetry it already collects is for.
package hostinfo

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// Instance is one reading. Every optional field is `omitempty` so the page can
// tell "not measured" from "measured as zero" without a second boolean per
// value.
type Instance struct {
	// What this build is. The version is stamped at build time and is the same
	// string `guard -version` prints, which is what the updater asks for.
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	// Kernel and Distro are read from the box: `/proc/sys/kernel/osrelease` and
	// the PRETTY_NAME in /etc/os-release. Empty off Linux.
	Kernel   string `json:"kernel,omitempty"`
	Distro   string `json:"distro,omitempty"`
	Hostname string `json:"hostname,omitempty"`

	CPUCount int    `json:"cpu_count,omitempty"`
	CPUModel string `json:"cpu_model,omitempty"`
	// Load is the box's, over one, five and fifteen minutes. Reported rather
	// than a CPU percentage because a percentage needs two readings a moment
	// apart, and a page that has to sample twice to draw one number is a page
	// that lies on the first paint. Load against CPUCount is the comparison
	// worth making anyway.
	Load1  float64 `json:"load_1,omitempty"`
	Load5  float64 `json:"load_5,omitempty"`
	Load15 float64 `json:"load_15,omitempty"`
	HasCPU bool    `json:"has_cpu"`

	// Memory as the collector counts it: used is total minus *available*, not
	// total minus free, because Linux's free excludes the page cache and a box
	// with a healthy cache would otherwise read as almost full.
	MemTotalKB int64 `json:"mem_total_kb,omitempty"`
	MemUsedKB  int64 `json:"mem_used_kb,omitempty"`
	HasMemory  bool  `json:"has_memory"`

	// Disk is the filesystem holding the database, which is the one that fills
	// up and takes guard with it. Not "/" — a deployment that mounted a volume
	// for /var/lib/guard would otherwise be shown the root filesystem's free
	// space and be reassured by it.
	DiskTotalKB int64  `json:"disk_total_kb,omitempty"`
	DiskUsedKB  int64  `json:"disk_used_kb,omitempty"`
	DiskPath    string `json:"disk_path,omitempty"`
	HasDisk     bool   `json:"has_disk"`

	// DatabasePath and DatabaseBytes: the file itself, plus its -wal and -shm,
	// because a WAL that has not checkpointed is a real and confusing chunk of
	// somebody's disk.
	DatabasePath  string `json:"database_path,omitempty"`
	DatabaseBytes int64  `json:"database_bytes,omitempty"`

	// HostUptimeSeconds is the box's; ProcessUptimeSeconds is guard's. Both,
	// because "guard restarted" and "the box rebooted" are different mornings.
	HostUptimeSeconds    float64 `json:"host_uptime_seconds,omitempty"`
	ProcessUptimeSeconds float64 `json:"process_uptime_seconds"`

	// What the runtime is doing. Cheap to read and the first thing worth
	// seeing when guard itself is the thing behaving oddly.
	Goroutines  int   `json:"goroutines"`
	HeapBytes   int64 `json:"heap_bytes"`
	Supervised  bool  `json:"supervised"`
	InContainer bool  `json:"in_container"`
}

// started is when this process came up. Taken at package init rather than from
// /proc, so it is the same number on every platform and needs no permission.
var started = time.Now()

// Read takes one reading. dbPath is the database guard opened, which decides
// which filesystem is worth reporting.
func Read(version, dbPath string) Instance {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)

	instance := Instance{
		Version:              version,
		GoVersion:            runtime.Version(),
		OS:                   runtime.GOOS,
		Arch:                 runtime.GOARCH,
		CPUCount:             runtime.NumCPU(),
		DatabasePath:         dbPath,
		ProcessUptimeSeconds: time.Since(started).Seconds(),
		Goroutines:           runtime.NumGoroutine(),
		HeapBytes:            int64(memory.HeapAlloc),
		// The same signal the configuration page uses to decide whether to
		// offer a restart button: something will bring guard back.
		Supervised:  os.Getenv("INVOCATION_ID") != "",
		InContainer: inContainer(),
	}
	if name, err := os.Hostname(); err == nil {
		instance.Hostname = name
	}
	instance.DatabaseBytes = databaseSize(dbPath)
	readHost(&instance)
	return instance
}

// databaseSize counts the database and the two files SQLite keeps beside it.
// A missing file is zero rather than an error: an in-memory store has no path,
// and a -wal only exists between checkpoints.
func databaseSize(path string) int64 {
	if strings.TrimSpace(path) == "" || path == "memory" {
		return 0
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// inContainer is the cheap test that is right often enough to be worth showing
// and is never load-bearing: /.dockerenv is what the docker runtime leaves
// behind. It changes no behaviour — it is on the page because "this is a
// container" explains why there is no /etc/guard and no update button.
func inContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
