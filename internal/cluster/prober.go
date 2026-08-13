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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

type Store interface {
	// NodesForProbe returns the enabled nodes with their cadence and the time
	// they were last checked — enough to decide what is due, without the
	// uptime and history a dashboard needs.
	NodesForProbe() ([]model.Node, error)
	Nodes() ([]model.Node, error)
	Node(id int64) (model.Node, error)
	RecordCheck(nodeID int64, check model.Check) error
}

// Icons is the optional half of the store: a prober whose store does not
// implement it simply never looks for a favicon. Optional because fetching a
// picture is not what a health prober is for, and a test store should not have
// to grow two methods to say so.
type Icons interface {
	IconStale(nodeID int64) (bool, error)
	SaveIcon(nodeID int64, icon []byte, contentType string) error
}

type Prober struct {
	Store    Store
	Interval time.Duration
	Timeout  time.Duration
	Log      *slog.Logger

	client *http.Client
	once   sync.Once
	wake   chan struct{}
}

const (
	// defaultInterval is only the idle sleep now that the cadence lives on each
	// node — how long to wait when there is nothing to watch at all.
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
		p.wake = make(chan struct{}, 1)
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

// Run checks nodes as they come due, until the context is cancelled.
//
// Not one ticker: the cadence is per node, so a loop at any single rate is
// either too slow for the fastest node or a busy-wait for the slowest. Each
// pass checks whatever is due and then sleeps exactly until the next one is,
// which also means an interval edited in the UI takes effect on the following
// pass rather than at the end of some longer cycle.
//
// The first pass happens immediately. A dashboard that says "unknown" for the
// first half-minute looks broken in exactly the way this feature exists to
// rule out.
func (p *Prober) Run(ctx context.Context) {
	p.prepare()
	p.Log.Info("cluster prober started", slog.Duration("timeout", p.Timeout))
	for {
		wait := p.Round(ctx)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-p.wake:
			// Something changed the node list. Sleeping out the remaining
			// interval would leave a machine somebody just added unwatched for
			// as long as the slowest node in the cluster.
			timer.Stop()
		}
	}
}

// Wake asks for a pass now. Called when a node is added, edited or removed, so
// the change takes effect while its author is still looking at the page.
//
// Non-blocking, and a single pending wake is as good as ten: the pass it
// triggers reads the whole node list anyway.
func (p *Prober) Wake() {
	p.prepare()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Round checks every node that is due and returns how long until the next one
// is. Exported for tests and for the scheduler above; the "check everything
// now" button calls RoundAll instead.
func (p *Prober) Round(ctx context.Context) time.Duration {
	p.prepare()
	nodes, err := p.Store.NodesForProbe()
	if err != nil {
		p.Log.Error("cluster prober could not read its nodes", slog.Any("err", err))
		return p.idle()
	}

	now := time.Now()
	var due []model.Node
	next := p.idle()
	for _, node := range nodes {
		if node.CheckedAt.IsZero() {
			due = append(due, node)
			// It counts towards the next wake-up too: a cluster of nodes that
			// have all just been added would otherwise be checked once and
			// then left alone for the idle sleep.
			next = min(next, node.Interval())
			continue
		}
		remaining := node.Interval() - now.Sub(node.CheckedAt)
		if remaining <= 0 {
			due = append(due, node)
			// A node just checked is next due one full interval from now.
			remaining = node.Interval()
		}
		next = min(next, remaining)
	}
	p.check(ctx, due)
	// Never a zero sleep: a node whose interval is shorter than its own check
	// takes would otherwise spin this loop with no gap at all.
	return max(next, 100*time.Millisecond)
}

// RoundAll checks every enabled node regardless of when it was last seen — the
// "is it back yet" button, which is a different question from "what is due".
func (p *Prober) RoundAll(ctx context.Context) {
	p.prepare()
	nodes, err := p.Store.NodesForProbe()
	if err != nil {
		p.Log.Error("cluster prober could not read its nodes", slog.Any("err", err))
		return
	}
	p.check(ctx, nodes)
}

// check probes a set of nodes concurrently.
//
// Concurrent because a pass must not take as long as the sum of its timeouts:
// ten nodes, one of them a black hole, and a serial pass would fall a minute
// behind its own schedule within a minute.
func (p *Prober) check(ctx context.Context, nodes []model.Node) {
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

// idle is how long to sleep with nothing to watch. Long enough not to poll an
// empty table in a loop, short enough that a node added in the UI starts being
// checked while its author is still looking at the page.
func (p *Prober) idle() time.Duration {
	if p.Interval > 0 {
		return p.Interval
	}
	return defaultInterval
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
	// Only from a machine that just answered, and at most once a day: a box
	// that is down has better things to be asked for, and most machines have no
	// favicon at all — which is an answer worth remembering rather than
	// rediscovering on every check.
	if check.OK {
		p.fetchIcon(ctx, node)
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

// maxIcon is the largest favicon worth storing. Real ones are a few kilobytes;
// anything past this is a machine serving something else at that path.
const maxIcon = 64 << 10

// fetchIcon asks a healthy node for /favicon.ico and stores whatever came back.
//
// Guard fetches it rather than the browser pointing an <img> at the node: this
// process can reach an internal box that the browser looking at the dashboard
// often cannot, and an <img> at http://vps-1/ from an https page is blocked as
// mixed content before it is attempted.
//
// Only /favicon.ico. Parsing the HTML for <link rel="icon"> would find a few
// more, at the cost of downloading and parsing a page on every node — and a
// health endpoint usually answers JSON, so there would be no HTML to read.
func (p *Prober) fetchIcon(ctx context.Context, node model.Node) {
	icons, ok := p.Store.(Icons)
	if !ok {
		return
	}
	stale, err := icons.IconStale(node.ID)
	if err != nil || !stale {
		return
	}

	target, err := url.Parse(node.URL)
	if err != nil {
		return
	}
	target.Path, target.RawQuery, target.Fragment = "/favicon.ico", "", ""

	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return
	}
	request.Header.Set("User-Agent", "guard-cluster-probe/1")
	request.Header.Set("Accept", "image/*")

	// Every path below records the attempt, including the failures. "We looked
	// and there is nothing" is the common answer, and remembering it is what
	// keeps this to one request a day instead of one per check.
	response, err := p.client.Do(request)
	if err != nil {
		_ = icons.SaveIcon(node.ID, nil, "")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxIcon+1))
	contentType := response.Header.Get("Content-Type")
	switch {
	case err != nil, response.StatusCode != http.StatusOK,
		len(body) == 0, len(body) > maxIcon,
		!strings.HasPrefix(contentType, "image/"):
		_ = icons.SaveIcon(node.ID, nil, "")
		return
	}
	if err := icons.SaveIcon(node.ID, body, contentType); err != nil {
		p.Log.Error("cluster icon not stored", slog.String("node", node.Name), slog.Any("err", err))
	}
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
