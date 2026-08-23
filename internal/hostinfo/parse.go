package hostinfo

// The parsing, kept away from the reading on purpose.
//
// Every one of these functions turns the *contents* of a /proc file into
// numbers, and none of them touches a filesystem. That is what makes them
// testable on a laptop: the readers in hostinfo_linux.go only compile on Linux,
// so anything that lived in there would be code that runs exclusively in
// production and is checked exclusively by hope.

import (
	"strconv"
	"strings"
)

// cpuModel takes the model name of the first processor. They are the same
// machine's cores, so the first is the answer; the ARM boards that say
// "Hardware" instead are worth the extra case because that is what a Vultr or
// Hetzner arm64 box reports.
func cpuModel(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name", "Model", "Hardware":
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// loadAverages reads the first three fields of /proc/loadavg. All three or
// none: a partial line is a file that is not what we think it is.
func loadAverages(line string) (one, five, fifteen float64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	one, err1 := strconv.ParseFloat(fields[0], 64)
	five, err2 := strconv.ParseFloat(fields[1], 64)
	fifteen, err3 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return one, five, fifteen, true
}

// memory counts used as total minus *available*, which is the collector's rule
// and the only one that is not misleading: MemFree excludes the page cache, so
// a healthy box with a warm cache reads as nearly full.
//
// A kernel too old to report MemAvailable would make used equal total, which is
// worse than saying nothing — so that case is unmeasured rather than alarming.
func memory(raw string) (total, used int64, ok bool) {
	var available int64
	seen := false
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		parsed, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total = parsed
		case "MemAvailable":
			available, seen = parsed, true
		}
	}
	if total <= 0 || !seen {
		return 0, 0, false
	}
	return total, total - available, true
}

// uptimeSeconds is the first field of /proc/uptime; the second is idle time
// summed over every core, which is not what anybody means by uptime.
func uptimeSeconds(line string) float64 {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return 0
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return seconds
}

// prettyName pulls PRETTY_NAME out of an os-release file: `Ubuntu 24.04.1 LTS`,
// which is the string somebody would actually say out loud.
func prettyName(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "PRETTY_NAME" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}
