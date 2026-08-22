package hostinfo

// The Linux half: reading the files, and one statfs. The parsing lives in
// parse.go so it can be tested anywhere.
//
// Everything here is a file read, so it needs no privilege and cannot block —
// which is what makes it safe inside a request rather than on a timer. A file
// that will not read leaves its fields empty, because the package's promise is
// that a number on the page was measured.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func readHost(instance *Instance) {
	instance.Kernel = firstLine("/proc/sys/kernel/osrelease")
	instance.Distro = prettyName(read("/etc/os-release"))
	instance.CPUModel = cpuModel(read("/proc/cpuinfo"))
	if one, five, fifteen, ok := loadAverages(firstLine("/proc/loadavg")); ok {
		instance.Load1, instance.Load5, instance.Load15, instance.HasCPU = one, five, fifteen, true
	}
	if total, used, ok := memory(read("/proc/meminfo")); ok {
		instance.MemTotalKB, instance.MemUsedKB, instance.HasMemory = total, used, true
	}
	instance.HostUptimeSeconds = uptimeSeconds(firstLine("/proc/uptime"))
	readDisk(instance)
}

// readDisk asks about the filesystem holding the database rather than about
// "/", because a deployment with a volume mounted for the database would
// otherwise be shown the root filesystem's free space and be reassured by it.
//
// Used is total minus free — the whole filesystem's used, not the part
// available to this user — because a disk that is full for root is full.
func readDisk(instance *Instance) {
	path := strings.TrimSpace(instance.DatabasePath)
	if path == "" || path == "memory" {
		return
	}
	dir := filepath.Dir(path)
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return
	}
	block := int64(stat.Bsize)
	if block <= 0 {
		return
	}
	instance.DiskTotalKB = int64(stat.Blocks) * block / 1024
	instance.DiskUsedKB = (int64(stat.Blocks) - int64(stat.Bfree)) * block / 1024
	instance.DiskPath = dir
	instance.HasDisk = instance.DiskTotalKB > 0
}

func read(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func firstLine(path string) string {
	line, _, _ := strings.Cut(read(path), "\n")
	return strings.TrimSpace(line)
}
