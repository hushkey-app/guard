package model

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A Webhook is a named place guard sends events to.
//
// Named rather than one URL in the environment, because "so I can direct where
// I want" is the whole feature: the backup alerts go to the channel the person
// who owns the database watches, and the page-me rules go somewhere that wakes
// somebody up. One row, reused by every watcher — the schedule staleness watch,
// the machine rules, a saved view — so a destination is typed once and pointed
// at from as many rules as want it.
type Webhook struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	// Header is where the token goes. Empty means Authorization, where a bare
	// token becomes a Bearer credential; an app wanting X-Api-Key names it and
	// gets the token verbatim.
	Header string `json:"header,omitempty"`
	// Token is write-only and a pointer, the same three-way the SSH password
	// is: absent leaves it alone, "" forgets it, a value replaces it. A form
	// that renames a destination sends no token and does not lose one.
	Token *string `json:"token,omitempty"`
	// HasToken is the read side. The secret never comes back — the dashboard
	// draws dots, exactly as it does for a machine's password.
	HasToken  bool      `json:"has_token"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	// LastSentAt and LastError are the only reason to keep state here: a
	// destination that has been quietly 404ing since Tuesday looks identical
	// to one nothing has happened on.
	LastSentAt time.Time `json:"last_sent_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

func (w Webhook) Validate() error {
	if strings.TrimSpace(w.Name) == "" {
		return errors.New("a destination needs a name")
	}
	if len(w.Name) > 60 {
		return errors.New("a destination name must be 60 characters or fewer")
	}
	parsed, err := url.Parse(strings.TrimSpace(w.URL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("a destination is an absolute http or https URL")
	}
	if strings.ContainsAny(w.Header, " \t\r\n") {
		return errors.New("a header name has no spaces in it")
	}
	return nil
}

// A Monitor is one rule about one measurement: "CPU above 90% for 5 minutes on
// DB-1, tell #ops".
//
// It is deliberately the same shape for every metric rather than a rule type
// per metric. Everything the cluster page already shows is a number or a state
// guard is polling anyway — the health check, the uptime share, the machine's
// own CPU, memory and disk — so a rule is which number, which way, how far,
// for how long, and where to say so. Adding the next metric is a line in
// MonitorMetrics and a case in Node.Measure, not a new table.
type Monitor struct {
	ID int64 `json:"id"`
	// NodeID is the machine this watches, or zero for **every** machine —
	// which is how "tell me about any disk over 90%" is one rule rather than
	// one per box, including the boxes added next month.
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	Metric   string `json:"metric"`
	// Op is above or below. Some metrics only make sense one way (uptime is a
	// below rule, disk is an above rule), but the pair is stored rather than
	// implied, because "latency above 500ms" and "latency below 5ms" are both
	// things somebody has wanted to know.
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	// ForSeconds is how long the condition has to hold. It is the difference
	// between a monitor and a noise generator: one 200ms blip at 3am is not a
	// page, and five minutes of them is.
	ForSeconds int   `json:"for_seconds"`
	WebhookID  int64 `json:"webhook_id"`
	Enabled    bool  `json:"enabled"`

	// The state, kept per rule and per machine because a rule over every
	// machine fires about one of them at a time.
	States []MonitorState `json:"states,omitempty"`
}

// MonitorState is where one rule stands against one machine.
type MonitorState struct {
	NodeID   int64     `json:"node_id"`
	NodeName string    `json:"node_name,omitempty"`
	Firing   bool      `json:"firing"`
	Since    time.Time `json:"since,omitempty"`
	Value    float64   `json:"value"`
	Alerted  time.Time `json:"alerted,omitempty"`
}

const (
	MonitorAbove = "above"
	MonitorBelow = "below"
)

// MetricUnit says how a threshold is written and read back, so the form can
// put "%" or "ms" next to the box and the alert can say "94%" rather than "94".
type MetricUnit struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
	// Category is what this measurement belongs to, and it is on the metric
	// rather than on the rule so it cannot be typed wrong or left blank. A list
	// of a dozen rules reading "cpu_percent above 90" tells you nothing about
	// which are watching the service and which are watching the box; grouped
	// under Health and Machine, it does.
	Category string `json:"category"`
	// Op is the direction this metric is almost always watched in — what the
	// form offers first. It stays changeable.
	Op string `json:"op"`
	// State marks the metrics that are not numbers at all. "down" is a
	// condition, and a threshold box next to it would be a box with nothing to
	// put in it.
	State bool `json:"state,omitempty"`
}

// MonitorMetrics is everything a rule can watch, and the whole of it. Each one
// is something guard is already polling for the page — no new collection, no
// new agent, no new table.
var MonitorMetrics = []MetricUnit{
	// Service: what the health check can see from outside, with no login.
	{Key: "down", Label: "Health check failing", Unit: "", Op: MonitorAbove, State: true, Category: CategoryService},
	{Key: "latency_ms", Label: "Response time", Unit: "ms", Op: MonitorAbove, Category: CategoryService},
	{Key: "uptime_percent", Label: "24h uptime", Unit: "%", Op: MonitorBelow, Category: CategoryService},
	// Machine: what the box says about itself over SSH. A rule here is silent
	// on a machine guard has no login for, which is why the split is worth
	// drawing in the picker rather than only in the docs.
	{Key: "cpu_percent", Label: "CPU", Unit: "%", Op: MonitorAbove, Category: CategoryMachine},
	{Key: "mem_percent", Label: "Memory", Unit: "%", Op: MonitorAbove, Category: CategoryMachine},
	{Key: "disk_percent", Label: "Disk /", Unit: "%", Op: MonitorAbove, Category: CategoryMachine},
	{Key: "host_uptime_hours", Label: "Host uptime", Unit: "h", Op: MonitorBelow, Category: CategoryMachine},
	{Key: "containers_down", Label: "Stopped containers", Unit: "", Op: MonitorAbove, Category: CategoryMachine},
}

// The categories a rule can belong to. Named here rather than spelled in three
// places, because they are also what the alerts page groups by and what an
// event carries — a receiver routing "everything about the machines" should not
// have to keep its own list of which metrics those are.
const (
	// CategoryService is answered by the health check, from outside, with no
	// login: is it up, how fast, how much of the day.
	CategoryService = "Service"
	// CategoryMachine is answered by the box itself over SSH — and therefore
	// only on the machines guard has a way into.
	CategoryMachine = "Machine"
	// CategoryJob is a stored command that stopped succeeding. Its rule lives
	// on the command rather than here, but it is the same idea and belongs
	// under the same heading.
	CategoryJob = "Jobs"
	// CategoryView is a saved panel with a line drawn across it.
	CategoryView = "Views"
)

// MonitorCategories is the order the page draws them in: outside-in, which is
// the order somebody diagnosing reads them in too.
var MonitorCategories = []string{CategoryService, CategoryMachine, CategoryJob, CategoryView}

// Category is the heading a rule belongs under.
func (m Monitor) Category() string {
	if metric, ok := Metric(m.Metric); ok {
		return metric.Category
	}
	return CategoryService
}

// Metric looks up one of them.
func Metric(key string) (MetricUnit, bool) {
	for _, metric := range MonitorMetrics {
		if metric.Key == key {
			return metric, true
		}
	}
	return MetricUnit{}, false
}

func (m Monitor) Validate() error {
	metric, ok := Metric(m.Metric)
	if !ok {
		return fmt.Errorf("%q is not something guard measures", m.Metric)
	}
	if !metric.State && m.Op != MonitorAbove && m.Op != MonitorBelow {
		return errors.New("a rule is above or below a number")
	}
	if metric.Unit == "%" && (m.Threshold < 0 || m.Threshold > 100) {
		return errors.New("a percentage is between 0 and 100")
	}
	if m.ForSeconds < 0 || m.ForSeconds > 24*3600 {
		return errors.New("hold the condition for something between nothing and a day")
	}
	if m.WebhookID <= 0 {
		return errors.New("a rule needs somewhere to send its events")
	}
	return nil
}

// For is the hold as a duration.
func (m Monitor) For() time.Duration { return time.Duration(m.ForSeconds) * time.Second }

// Describe is the rule as a sentence, used in the event and on the page so the
// two cannot say different things.
func (m Monitor) Describe() string {
	metric, ok := Metric(m.Metric)
	if !ok {
		return m.Metric
	}
	if metric.State {
		return metric.Label
	}
	return fmt.Sprintf("%s %s %s%s", metric.Label, m.Op, trimFloat(m.Threshold), metric.Unit)
}

func trimFloat(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	return text
}

// Measure reads one metric off a machine as the monitors see it.
//
// The second return is whether the machine can answer at all: a box with no
// SSH login has no CPU figure, and a rule about one must be *silent* there
// rather than reading zero and paging somebody about a machine that is fine.
func (n Node) Measure(metric string) (float64, bool) {
	switch metric {
	case "down":
		if !n.Enabled || n.Status == StatusUnknown {
			// Paused or never checked is not down. A machine somebody took out
			// of service on purpose is the last thing that should page them.
			return 0, false
		}
		if n.Status == StatusDown {
			return 1, true
		}
		return 0, true
	case "latency_ms":
		if n.CheckedAt.IsZero() || n.Status != StatusUp {
			return 0, false
		}
		return n.LatencyMS, true
	case "uptime_percent":
		if n.Checks == 0 {
			return 0, false
		}
		return n.Uptime, true
	}
	// Everything below comes from the machine's own sample, which only exists
	// where guard has a login and a recent answer.
	if n.Stats == nil || n.Stats.Error != "" {
		return 0, false
	}
	switch metric {
	case "cpu_percent":
		if !n.Stats.HasCPU {
			return 0, false
		}
		return n.Stats.CPUPercent, true
	case "mem_percent":
		if n.Stats.MemTotalKB <= 0 {
			return 0, false
		}
		return n.Stats.MemPercent(), true
	case "disk_percent":
		if n.Stats.DiskTotalKB <= 0 {
			return 0, false
		}
		return n.Stats.DiskPercent(), true
	case "host_uptime_hours":
		if n.Stats.UptimeSeconds <= 0 {
			return 0, false
		}
		return n.Stats.UptimeSeconds / 3600, true
	case "containers_down":
		if len(n.Stats.Containers) == 0 {
			return 0, false
		}
		var down float64
		for _, container := range n.Stats.Containers {
			if !container.Up {
				down++
			}
		}
		return down, true
	}
	return 0, false
}

// Breached reports whether a measurement is on the wrong side of this rule.
func (m Monitor) Breached(value float64) bool {
	if metric, ok := Metric(m.Metric); ok && metric.State {
		return value >= 1
	}
	if m.Op == MonitorBelow {
		return value < m.Threshold
	}
	return value > m.Threshold
}
