package cluster

// Reading a machine's own numbers: the command guard runs, and the parsing of
// what comes back.
//
// This is here rather than in internal/remote because internal/remote has no
// idea what it is running and should keep it that way. What a Linux box says
// about itself is knowledge about machines, which is what this package is for.

import (
	"strconv"
	"strings"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// StatsCommand is what a sample runs. It is a constant in guard's source and
// never a stored action: this runs on a timer, unattended, and the difference
// between "a fixed read-only command" and "whatever is in the command list"
// is the whole reason the command list needs a lock.
//
// One session, one round trip, six answers. Everything in it reads: /proc is
// a view of the kernel, df asks the filesystem for numbers it already has, and
// `docker ps` lists. Nothing here can change anything, which is why it is
// allowed to run against a locked machine.
//
// The markers make the output a format rather than a guess. Parsing `top` or
// `free` means parsing whatever locale and version that box has; /proc is the
// same on every Linux there has ever been.
const StatsCommand = "echo '#load'; cat /proc/loadavg 2>/dev/null; " +
	"echo '#uptime'; cat /proc/uptime 2>/dev/null; " +
	"echo '#mem'; cat /proc/meminfo 2>/dev/null | head -5; " +
	"echo '#cpu'; grep -m1 '^cpu ' /proc/stat 2>/dev/null; " +
	"echo '#cpus'; grep -c '^processor' /proc/cpuinfo 2>/dev/null; " +
	"echo '#disk'; df -Pk / 2>/dev/null; " +
	"echo '#docker'; docker ps -a --format '{{.Names}}\\t{{.Image}}\\t{{.Status}}' 2>&1 | head -40"

// ParseStats turns that output into one sample.
//
// Every section is optional. A machine that answers /proc but has no docker is
// an ordinary machine, and a df that failed should cost the disk figure and
// nothing else — a sample that threw away the memory reading because one
// section was missing would be a worse answer than a partial one.
func ParseStats(output string) model.HostStats {
	stats := model.HostStats{}
	section := ""
	docker := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "#") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		switch section {
		case "load":
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				stats.Load1, _ = strconv.ParseFloat(fields[0], 64)
				stats.Load5, _ = strconv.ParseFloat(fields[1], 64)
				stats.Load15, _ = strconv.ParseFloat(fields[2], 64)
			}
		case "uptime":
			if fields := strings.Fields(line); len(fields) >= 1 {
				stats.UptimeSeconds, _ = strconv.ParseFloat(fields[0], 64)
			}
		case "mem":
			readMemLine(&stats, line)
		case "cpu":
			readCPULine(&stats, line)
		case "cpus":
			if count, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
				stats.CPUCount = count
			}
		case "disk":
			readDiskLine(&stats, line)
		case "docker":
			docker = append(docker, line)
		}
	}
	readDocker(&stats, docker)
	return stats
}

// readMemLine reads the three lines that matter out of /proc/meminfo.
//
// Used is total minus *available*, not total minus free. Free is the number
// that makes people think a healthy Linux box is out of memory: the kernel
// spends everything spare on cache and hands it back the moment something
// wants it. Available is the kernel's own estimate of what a new process
// could get, which is the question being asked.
func readMemLine(stats *model.HostStats, line string) {
	name, value, found := strings.Cut(line, ":")
	if !found {
		return
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return
	}
	switch name {
	case "MemTotal":
		stats.MemTotalKB = kb
	case "MemAvailable":
		stats.MemUsedKB = stats.MemTotalKB - kb
	}
}

// readCPULine keeps the raw counters. /proc/stat counts jiffies since boot, so
// a percentage needs two readings — the collector takes the difference against
// the previous sample and this one carries the numbers forward.
func readCPULine(stats *model.HostStats, line string) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}
	var total, idle int64
	for i, field := range fields[1:] {
		value, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			continue
		}
		total += value
		// Fields 4 and 5 are idle and iowait. A box blocked on its disk is not
		// working, and calling that busy would point at the wrong thing.
		if i == 3 || i == 4 {
			idle += value
		}
	}
	stats.CPUTotal = total
	stats.CPUBusy = total - idle
}

// readDiskLine reads the root filesystem's line out of `df -Pk`, skipping the
// header. Root only: a box with twelve mounts turns a card into a table, and
// the one that fills up and stops everything is nearly always /.
func readDiskLine(stats *model.HostStats, line string) {
	fields := strings.Fields(line)
	if len(fields) < 6 || fields[0] == "Filesystem" {
		return
	}
	total, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return
	}
	used, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return
	}
	stats.DiskTotalKB = total
	stats.DiskUsedKB = used
}

// readDocker turns `docker ps` lines into containers, and anything that is not
// one into an explanation.
//
// The command redirects stderr into stdout on purpose: "no docker here" and
// "this login cannot talk to the socket" are the two most likely answers, and
// both are things the person reading the card needs to be told rather than
// left to infer from an empty list.
func readDocker(stats *model.HostStats, lines []string) {
	for _, line := range lines {
		name, rest, found := strings.Cut(line, "\t")
		image, status, statusFound := strings.Cut(rest, "\t")
		if !found || !statusFound {
			// Not a container row. The first such line is the reason there are
			// none, trimmed to something that fits on a card.
			if stats.DockerError == "" {
				stats.DockerError = trimTo(strings.TrimSpace(line), 120)
			}
			continue
		}
		stats.Containers = append(stats.Containers, model.Container{
			Name:   name,
			Image:  image,
			Status: status,
			// "Up 3 days (unhealthy)" is not up in any sense that matters to
			// somebody looking at this card.
			Up: strings.HasPrefix(status, "Up") && !strings.Contains(status, "unhealthy"),
		})
	}
	if len(stats.Containers) > 0 {
		stats.DockerError = ""
	}
}

func trimTo(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// cpuPercent is the busy share between two samples. It returns false when the
// pair cannot answer: no previous reading, a counter that went backwards
// because the machine rebooted, or two samples of the same instant.
func cpuPercent(previous, current model.HostStats) (float64, bool) {
	if previous.CPUTotal <= 0 || current.CPUTotal <= previous.CPUTotal {
		return 0, false
	}
	busy := current.CPUBusy - previous.CPUBusy
	total := current.CPUTotal - previous.CPUTotal
	if busy < 0 || total <= 0 {
		return 0, false
	}
	percent := float64(busy) / float64(total) * 100
	if percent > 100 {
		percent = 100
	}
	return percent, true
}
