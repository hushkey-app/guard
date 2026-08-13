// Package cluster polls the machines guard watches from the outside.
//
// It is separate from internal/telemetry for two reasons. Making outbound HTTP
// requests is not a storage package's job; and a prober that needed a database
// to exist could not be tested against a single fake node, which is most of
// what there is to test.
//
// The contract with the store is two methods wide, declared here rather than
// there, so this package depends on an idea instead of on SQLite.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

type Store interface {
	Nodes() ([]model.Node, error)
	Node(id int64) (model.Node, error)
	RecordCheck(nodeID int64, check model.Check) error
}

type Prober struct {
	Store    Store
	Interval time.Duration
	Timeout  time.Duration
	Log      *slog.Logger

	client *http.Client
	once   sync.Once
}

const (
	defaultInterval = 30 * time.Second
	defaultTimeout  = 5 * time.Second
	// A health endpoint answers in a few bytes. Reading more would let one
	// misconfigured URL pointing at a video file exhaust the process, and
	// nothing here looks at the body anyway.
	maxBody = 4 << 10
	// Enough redirects for a scheme upgrade or a trailing-slash bounce, few
	// enough that a redirect loop is a failed check rather than a hung one.
	maxRedirects = 3
)

func (p *Prober) prepare() {
	p.once.Do(func() {
		if p.Interval <= 0 {
			p.Interval = defaultInterval
		}
		if p.Timeout <= 0 {
			p.Timeout = defaultTimeout
		}
		if p.Log == nil {
			p.Log = slog.Default()
		}
		p.client = &http.Client{
			Timeout: p.Timeout,
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: p.Timeout}).DialContext,
				TLSHandshakeTimeout: p.Timeout,
				// Health checks are small, frequent and to a handful of hosts:
				// keeping connections is the difference between measuring the
				// service and measuring a TLS handshake.
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
}

// Run polls every enabled node on a ticker until the context is cancelled.
//
// The first round happens immediately. A dashboard that says "unknown" for
// thirty seconds after start looks broken in exactly the way this feature
// exists to rule out.
func (p *Prober) Run(ctx context.Context) {
	p.prepare()
	p.Log.Info("cluster prober started", slog.Duration("interval", p.Interval), slog.Duration("timeout", p.Timeout))
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		p.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Round checks every enabled node once, concurrently.
//
// Concurrent because the round must not take as long as the sum of its
// timeouts: ten nodes, one of them a black hole, and a serial round would be
// fifty seconds behind a thirty-second ticker within a minute.
func (p *Prober) Round(ctx context.Context) {
	p.prepare()
	nodes, err := p.Store.Nodes()
	if err != nil {
		p.Log.Error("cluster prober could not read its nodes", slog.Any("err", err))
		return
	}
	var wait sync.WaitGroup
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		wait.Add(1)
		go func(node model.Node) {
			defer wait.Done()
			p.checkAndRecord(ctx, node)
		}(node)
	}
	wait.Wait()
}

// CheckNow probes one node immediately, for the button that asks "is it back
// yet" rather than waiting out the interval.
func (p *Prober) CheckNow(ctx context.Context, id int64) (model.Check, error) {
	p.prepare()
	node, err := p.Store.Node(id)
	if err != nil {
		return model.Check{}, err
	}
	return p.checkAndRecord(ctx, node), nil
}

func (p *Prober) checkAndRecord(ctx context.Context, node model.Node) model.Check {
	check := p.Check(ctx, node.URL)
	if err := p.Store.RecordCheck(node.ID, check); err != nil {
		p.Log.Error("cluster check not recorded", slog.String("node", node.Name), slog.Any("err", err))
	}
	if !check.OK {
		p.Log.Warn("cluster node is down",
			slog.String("node", node.Name), slog.String("url", node.URL),
			slog.Int("status", check.StatusCode), slog.String("err", check.Error))
	}
	return check
}

// Check performs one probe. It never returns an error: a probe that failed is
// the result, not an exception.
func (p *Prober) Check(ctx context.Context, target string) model.Check {
	p.prepare()
	check := model.Check{CheckedAt: time.Now().UTC()}
	if err := model.ValidateNodeURL(target); err != nil {
		check.Error = err.Error()
		return check
	}

	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		check.Error = err.Error()
		return check
	}
	request.Header.Set("User-Agent", "guard-cluster-probe/1")
	request.Header.Set("Accept", "*/*")

	start := time.Now()
	response, err := p.client.Do(request)
	check.LatencyMS = float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		check.Error = reason(err)
		return check
	}
	defer response.Body.Close()
	// Drained, not ignored: an unread body means the connection cannot be
	// reused, and the next check pays for a fresh handshake.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBody))

	check.StatusCode = response.StatusCode
	check.OK = response.StatusCode >= 200 && response.StatusCode < 400
	if !check.OK {
		check.Error = response.Status
	}
	return check
}

// reason turns Go's transport errors into something a person reading a
// dashboard at 3am can act on. The originals name the dialer, the address and
// the syscall, which is three facts too many when the answer is "nothing is
// listening".
func reason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	text := err.Error()
	switch {
	case strings.Contains(text, "connection refused"):
		return "connection refused — nothing is listening"
	case strings.Contains(text, "no such host"):
		return "host not found"
	case strings.Contains(text, "certificate"):
		return "TLS certificate rejected"
	case strings.Contains(text, "network is unreachable"):
		return "network unreachable"
	}
	// Unwrap the layers Go's client wraps around the cause; the last one is
	// usually the readable half.
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		return unwrapped.Error()
	}
	return text
}
