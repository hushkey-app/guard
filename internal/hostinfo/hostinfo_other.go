//go:build !linux

package hostinfo

// Everywhere that is not Linux — a laptop running `make dev`, in practice.
//
// It fills nothing rather than reaching for a per-platform equivalent of each
// /proc file. Guard is deployed on Linux; the honest answer on a development
// machine is the portable half (version, Go, CPU count, the database size, the
// process's own uptime) and a dash for the rest, which is exactly what an
// unmeasurable field is supposed to look like. A darwin implementation that
// shelled out to sysctl would be code nobody runs in production, checked by
// nobody, describing the one box where it does not matter.
func readHost(*Instance) {}
