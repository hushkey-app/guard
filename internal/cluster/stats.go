package cluster

// The stats collector: the machines' own numbers, asked over SSH on a slow
// cadence.
//
// A separate loop from the prober, not a section of it, because they are
// different questions asked of different things at different rates. The
// prober fetches a URL every few seconds and learns whether the *service*
// answered. This opens an SSH session every minute or so and learns what the
// *machine* is doing — and on a box where the health endpoint lives inside a
// container, those two answers routinely disagree, which is the entire point.
//
// It only ever runs one fixed, read-only command (StatsCommand). Nothing a
// user typed reaches this loop.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/remote"
	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// StatsStore is the contract, declared here so this package depends on an idea
// rather than on SQLite.
type StatsStore interface {
	NodesForStats() ([]model.Node, error)
	Node(id int64) (model.Node, error)
	// SSHLoginFor is the one call that returns a secret, and it is named for
	// it. A machine with no stored login answers an error, which is how this
	// loop learns to leave it alone.
	SSHLoginFor(nodeID int64) (remote.Login, error)
	// LastStats is the previous sample, needed because CPU is a rate: the
	// kernel counts jiffies since boot, so a percentage is a difference
	// between two readings and never one of them.
	LastStats(nodeID int64) (model.HostStats, error)
	RecordStats(nodeID int64, stats model.HostStats) error
	PinFingerprint(nodeID int64, fingerprint string) error
}

// Sampler is all the collector needs of a runner: one command, one answer.
// Narrow because it is also the seam a test comes in through — an SSH server
// is a fine thing to test internal/remote against and a poor thing to require
// of everything that reads a number out of one.
type Sampler interface {
	Run(ctx context.Context, login remote.Login, command string) (remote.Result, error)
}

// Collector samples machines on their own cadence.
type Collector struct {
	Store  StatsStore
	Runner Sampler
	Log    *slog.Logger

	once sync.Once
	wake chan struct{}
}

// statsIdle is how long to sleep when nothing is being sampled at all —
// nobody has a login stored, or everybody turned it off.
const statsIdle = 60 * time.Second

// statsTimeout bounds one sample. The command reads six files and lists
// containers; a machine that cannot do that in fifteen seconds is a machine
// with a problem the sample was going to report anyway.
const statsTimeout = 15 * time.Second

func (c *Collector) prepare() {
	c.once.Do(func() {
		if c.Log == nil {
			c.Log = slog.Default()
		}
		if c.Runner == nil {
			c.Runner = &remote.Runner{}
		}
		c.wake = make(chan struct{}, 1)
	})
}

// Run samples nodes as they come due, until the context is cancelled. The
// same shape as the prober's loop, and for the same reason: the cadence is per
// node, so a single ticker would be wrong for every node but one.
func (c *Collector) Run(ctx context.Context) {
	c.prepare()
	c.Log.Info("cluster stats collector started")
	for {
		wait := c.Round(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-c.wake:
			timer.Stop()
		}
	}
}

// Wake asks for a pass now — called when a machine is added or its login
// changes, so the first sample lands while its author is still on the page.
func (c *Collector) Wake() {
	c.prepare()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Round samples everything due and returns how long until the next one is.
func (c *Collector) Round(ctx context.Context) time.Duration {
	c.prepare()
	nodes, err := c.Store.NodesForStats()
	if err != nil {
		c.Log.Error("stats collector could not read its nodes", slog.Any("err", err))
		return statsIdle
	}
	now := time.Now()
	next := statsIdle
	var due []model.Node
	for _, node := range nodes {
		interval := node.StatsInterval()
		if interval == 0 || !node.Enabled {
			continue
		}
		last := time.Time{}
		if node.Stats != nil {
			last = node.Stats.At
		}
		if last.IsZero() {
			due = append(due, node)
			next = min(next, interval)
			continue
		}
		remaining := interval - now.Sub(last)
		if remaining <= 0 {
			due = append(due, node)
			remaining = interval
		}
		next = min(next, remaining)
	}
	c.sample(ctx, due)
	return max(next, time.Second)
}

// sample reads a set of machines concurrently, because a pass must not take as
// long as the sum of its timeouts — and an unreachable box is exactly the one
// that costs the full fifteen seconds.
func (c *Collector) sample(ctx context.Context, nodes []model.Node) {
	var wait sync.WaitGroup
	for _, node := range nodes {
		wait.Add(1)
		go func(node model.Node) {
			defer wait.Done()
			c.SampleNode(ctx, node.ID)
		}(node)
	}
	wait.Wait()
}

// SampleNode takes one sample and stores it. A failure is a stored sample too:
// "guard could not get in since 04:12" is information, and a card that quietly
// kept showing the last good numbers would be lying by omission.
func (c *Collector) SampleNode(ctx context.Context, nodeID int64) model.HostStats {
	c.prepare()
	stats := model.HostStats{At: time.Now().UTC()}

	login, err := c.Store.SSHLoginFor(nodeID)
	if err != nil {
		stats.Error = err.Error()
		c.record(nodeID, stats)
		return stats
	}

	ctx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()
	result, err := c.Runner.Run(ctx, login, StatsCommand)
	if result.Fingerprint != "" && login.Fingerprint == "" {
		// First connection: pin the key, exactly as a manual run does. A
		// sample is a connection like any other, and leaving it unpinned would
		// mean the pin depends on which feature somebody used first.
		if err := c.Store.PinFingerprint(nodeID, result.Fingerprint); err != nil {
			c.Log.Error("host key not pinned", slog.Int64("node", nodeID), slog.Any("err", err))
		}
	}
	if err != nil {
		stats.Error = err.Error()
		if errors.Is(err, remote.ErrHostKeyChanged) {
			// Worth a line of its own: the machine answering is not the machine
			// that answered last time, which is a rebuild or an interception
			// and never a thing to shrug at.
			c.Log.Warn("stats sample refused: host key changed", slog.Int64("node", nodeID))
		}
		c.record(nodeID, stats)
		return stats
	}

	parsed := ParseStats(result.Output)
	parsed.At = stats.At
	// CPU is a rate. The previous sample is the other half of it, and its
	// absence — a fresh machine, a restarted guard — is why HasCPU exists
	// rather than a zero that reads as "idle".
	if previous, err := c.Store.LastStats(nodeID); err == nil {
		if percent, ok := cpuPercent(previous, parsed); ok {
			parsed.CPUPercent, parsed.HasCPU = percent, true
		}
	}
	c.record(nodeID, parsed)
	return parsed
}

func (c *Collector) record(nodeID int64, stats model.HostStats) {
	if err := c.Store.RecordStats(nodeID, stats); err != nil {
		c.Log.Error("stats sample not recorded", slog.Int64("node", nodeID), slog.Any("err", err))
	}
}
