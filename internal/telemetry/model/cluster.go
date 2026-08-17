package model

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A Node is one machine guard watches from the outside.
//
// This is deliberately not the same idea as an Instance. An instance is derived
// from telemetry: it exists because something posted to guard, and it disappears
// when that stops — which is exactly when you most want to know about it. A node
// is declared, so guard can say "VPS-1 has been down for six minutes" about a
// machine that is not talking to anyone.
//
// A machine is two addresses that answer two different questions. The address
// is where the *service* answers — a public domain, or http://localhost:8000
// while there is not one yet — and the health path hangs off it, because a
// health endpoint belongs to a service rather than to a box. The SSH address is
// the *machine*, and it is only ever used to get in and run something.
//
// Both are dialled by guard from the server, never by the browser: the address
// is often on a network the laptop reading this dashboard is not on.
type Node struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// URL is the address that gets probed. It is derived from the parts below
	// when they are given — the store computes it on save — and typed directly
	// only by callers that predate them.
	URL string `json:"url"`
	// Domain is where the service answers: https://api.example.com, or
	// http://localhost:8000 until there is a public name for it. This is what
	// the health path is appended to.
	Domain string `json:"domain,omitempty"`
	// InternalURL is kept for machines added before the address and the health
	// path were one field. It is probed only when there is no domain at all;
	// nothing writes it any more.
	InternalURL string `json:"internal_url,omitempty"`
	// HealthPath hangs off the address, so the same "/api/health" follows the
	// service when it moves from localhost to a domain.
	HealthPath string `json:"health_path,omitempty"`
	// SSHAddress is user@host, optionally with a port: root@10.10.10.10:2222.
	// The machine itself, which is not the same thing as the address above: a
	// service behind a load balancer answers on a domain that belongs to no
	// single box, and the box it actually runs on is this.
	//
	// Empty means this machine is watched but not reachable, which is a normal
	// thing to want — the health check needs no login.
	SSHAddress string `json:"ssh_address,omitempty"`
	// Password is write-only. It is a pointer so that three different requests
	// can be told apart: absent means "leave it as it was", empty means "forget
	// it", and a value means "this is the new one". A plain string could only
	// express two of those, and the missing one is the one the edit form sends
	// on every save that is not about the password.
	Password *string `json:"password,omitempty"`
	// HasPassword is the read side. The secret itself never leaves the server —
	// the dashboard is told that one exists, and draws dots.
	HasPassword bool `json:"has_password,omitempty"`
	// SSHFingerprint is the host key guard saw the first time it connected, and
	// insists on every time after. Shown so it can be compared with what the
	// machine's owner says it should be.
	SSHFingerprint string `json:"ssh_fingerprint,omitempty"`
	// Locked finishes a machine's dangerous half: the login is frozen and the
	// list of commands is closed — nothing added, nothing edited, nothing
	// removed, from this page or from the API.
	//
	// It is one way, and the only way out is deleting the machine. That is the
	// entire point. A lock that can be lifted by whoever is inconvenienced by it
	// protects nobody, and the threat it answers is not a typo — it is a command
	// appearing in a list that somebody already decided to trust. Deleting the
	// machine cannot be that quiet: it takes the history, the login and the
	// commands with it, and what is left is a new row with nothing pinned.
	//
	// Locked does not mean frozen altogether. The name, the address and the
	// cadence stay editable, because none of them can run anything, and the
	// commands that exist can still be run.
	Locked bool `json:"locked"`
	// Actions are the commands someone chose to keep for this machine. Carried
	// with the node because the settings page draws them together, and a second
	// request per node to list two buttons is a request per node too many.
	Actions []NodeAction `json:"actions,omitempty"`
	// Env is what guard knows about this machine's environment: how many
	// variables are kept for it, when they were last saved and when they were
	// last put on the box. Not the values — the list every machine carries is
	// read three times a second by the dashboard, and a fleet's worth of
	// passwords does not belong in it. The editor asks for one machine's.
	Env NodeEnvState `json:"env"`
	// Group is the box this machine sits in — "VPC-1", "staging", "the rack in
	// the office". Free text, because guard cannot know whether the boundary
	// that matters to somebody is a VPC, a region, a customer or a floor, and a
	// dropdown of the ones it could infer would be wrong for the first person
	// whose boundary is none of them.
	//
	// It is not a tag. A tag is one of many labels for finding a machine again;
	// a group is where the machine *is*, so it is single-valued and the cluster
	// page is laid out by it. Empty is normal and lands in "Ungrouped".
	Group string `json:"group,omitempty"`
	// Tags are what somebody scanning twenty cards is actually looking for:
	// "the postgres boxes", "the redis ones". Guard attaches no meaning to
	// them — it does not know what postgres is — so they are a label and a
	// colour, chosen by the person who has to find the machine again.
	Tags []NodeTag `json:"tags,omitempty"`
	// Enabled stops the polling without losing the node. A machine taken down
	// for maintenance should not have to be deleted and retyped.
	Enabled bool `json:"enabled"`
	// IntervalSeconds is how often to check this machine. Per node, because a
	// load balancer worth watching every three seconds and a nightly batch box
	// worth watching every five minutes are both in the same cluster, and one
	// global cadence has to be wrong for one of them.
	IntervalSeconds int       `json:"interval_seconds"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// StatsIntervalSeconds is how often to ask the machine itself how it is
	// doing, over SSH. Its own cadence, and much slower than the health check:
	// a health check is one HTTP request on a kept-alive connection, and this
	// is a fresh SSH handshake. Zero turns it off.
	StatsIntervalSeconds int `json:"stats_interval_seconds"`
	// Stats is the last sample, carried with the node so the card draws in one
	// request — the same bargain the latest check makes.
	Stats *HostStats `json:"stats,omitempty"`
	// CPUHistory is the recent samples' CPU percentage, oldest first, for the
	// sparkline beside the uptime strip.
	CPUHistory []float64 `json:"cpu_history,omitempty"`
	// Provider, ProviderAccountID and ProviderInstanceID are the link to the
	// cloud account this machine's instance lives in, when there is one.
	//
	// A node stands on its own without them: guard watches addresses, and
	// plenty of the machines worth watching are not in anybody's API. The link
	// adds the half a health check cannot see — whether the box is powered on
	// at all, what it costs, what it can be rolled back to — and it is stored
	// as an id rather than an address because an instance keeps its id across
	// a reinstall and loses its IP to any number of ordinary events.
	//
	// Nothing else about the instance is stored. The plan, the region, the
	// power state and the snapshots are read live, per open, for the same
	// reason the registries page reads live: a copy of somebody else's state
	// is only ever right by coincidence.
	Provider           string `json:"provider,omitempty"`
	ProviderAccountID  int64  `json:"provider_account_id,omitempty"`
	ProviderInstanceID string `json:"provider_instance_id,omitempty"`
	// HasIcon says the node's favicon was found and stored. The bytes are not
	// carried here: at fifteen kilobytes each they would be most of a cluster
	// response the dashboard refetches every three seconds, for a picture that
	// changes about once a year. They come from their own endpoint, cached.
	HasIcon bool `json:"has_icon,omitempty"`

	// The rest is the latest check, carried alongside so the dashboard reads
	// the whole cluster in one request.
	Status     string    `json:"status"` // up | down | unknown
	StatusCode int       `json:"status_code,omitempty"`
	LatencyMS  float64   `json:"latency_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at,omitempty"`
	// Uptime is the share of successful checks over the last day, and Checks is
	// how many there were. A 100% that is one check out of one is worth telling
	// apart from a 100% that is two thousand.
	Uptime float64 `json:"uptime"`
	Checks int     `json:"checks"`
	// History is the recent checks, oldest first, for the sparkline: 1 up,
	// 0 down.
	History []float64 `json:"history,omitempty"`
}

// TagColours is the palette a tag may take, and the whole of it: ten hues
// that stay apart on the dark surface, named rather than spelled in hex.
//
// Named, because the name is what survives a theme change — a tag stored as
// "#3b82f6" is a tag that will be the wrong blue the day the surface moves,
// and the client maps these to the tokens it draws with. Ten, because a
// palette people choose from by eye stops being a palette somewhere around
// a dozen, and because two greens nobody can tell apart help nobody.
var TagColours = []string{
	"slate", "red", "orange", "amber", "green", "teal", "blue", "indigo", "violet", "pink",
}

// MaxTagsPerNode keeps a card readable. Past a handful the chips are the
// card, and the machine they describe is the thing pushed off it.
const MaxTagsPerNode = 8

// A NodeTag is one label on one machine — "postgres", in blue.
type NodeTag struct {
	Label  string `json:"label"`
	Colour string `json:"colour"`
}

func (t NodeTag) Validate() error {
	if strings.TrimSpace(t.Label) == "" {
		return errors.New("a tag needs a label")
	}
	if len(t.Label) > 24 {
		return errors.New("a tag label must be 24 characters or fewer")
	}
	if !ValidTagColour(t.Colour) {
		return fmt.Errorf("%q is not one of the tag colours", t.Colour)
	}
	return nil
}

// ValidTagColour reports whether a colour is one of the ten. An empty one is
// allowed and means the first: a tag typed without choosing a colour is a
// tag somebody wanted, and refusing it over a default is a poor trade.
func ValidTagColour(colour string) bool {
	if colour == "" {
		return true
	}
	for _, candidate := range TagColours {
		if candidate == colour {
			return true
		}
	}
	return false
}

// NodeAction is a command someone keeps for one machine: a name to press and a
// line to run over SSH.
//
// The pair is the whole feature. "Reboot" and "apt-get update && apt-get
// upgrade -y" are not commands guard knows anything about — it does not parse
// them, does not know which are dangerous, and deliberately offers no library
// of blessed ones. What it offers is that the line you already type at 3am is
// stored next to the machine you type it at.
type NodeAction struct {
	ID      int64  `json:"id"`
	NodeID  int64  `json:"node_id,omitempty"`
	Name    string `json:"name"`
	Command string `json:"command"`
	// Schedule is when guard runs this by itself: a five-field cron expression
	// in UTC, or "@every 6h". Empty — which is the normal case — means the
	// action is only ever a button somebody presses.
	//
	// It is a column rather than a job record because that is the whole design:
	// the thing that runs on a timer is the same stored command, on the same
	// machine, through the same SSH login, with the same audit line. A schedule
	// adds who pressed it, not what it does.
	Schedule string `json:"schedule,omitempty"`
	// StaleAfterSeconds is the other half, and the half that catches the
	// failure that matters: "there has been no successful run in seven hours".
	// Zero means nobody is watching this action, which is the default.
	//
	// It is deliberately not derived from the schedule. A dump every six hours
	// that has not worked for six hours and one minute is not yet news; one
	// that has not worked in a day is, and only the person who knows what the
	// job is for can say where the line is.
	StaleAfterSeconds int `json:"stale_after_seconds,omitempty"`
	// WebhookID is where this job's staleness alert goes, or zero for whatever
	// the instance was configured with. Per action, because "the backups have
	// stopped" belongs in the channel the person who owns the database watches
	// and "the cache flush has stopped" does not.
	WebhookID int64 `json:"webhook_id,omitempty"`
	// The last run, so a button can say what happened last time it was pressed
	// rather than looking identical whether it has ever worked.
	LastRunAt time.Time `json:"last_run_at,omitempty"`
	LastExit  int       `json:"last_exit"`
	LastError string    `json:"last_error,omitempty"`
	// LastOKAt is the last run that exited zero, kept apart from LastRunAt
	// because they answer different questions and the staleness watch reads
	// this one. An action failing every six hours on the dot has a very recent
	// last run and no successful one since Tuesday.
	LastOKAt time.Time `json:"last_ok_at,omitempty"`
	// AlertedAt is when the staleness watch last said something about this
	// action, so a stale job is reported and then repeated occasionally rather
	// than every time the watchdog wakes up. Cleared by the next success.
	AlertedAt time.Time `json:"alerted_at,omitempty"`
	// ScheduleFrom is when the expression above was last written, and it is
	// what an action that has never run counts from. Stored rather than
	// derived: an action saved with "@every 6h" at nine o'clock is first due at
	// three, and it has to stay due at three across every pass of the loop and
	// every restart in between.
	ScheduleFrom time.Time `json:"schedule_from,omitempty"`
	// CreatedAt anchors the staleness watch for an action that has never
	// succeeded: without it, a job that has never worked once looks exactly
	// like one that has nothing to report.
	CreatedAt time.Time `json:"created_at,omitempty"`
	// NextRunAt is computed on read, not stored — the schedule and the last run
	// are the truth, and a stored next-run is a second copy of it that is wrong
	// every time somebody edits the expression.
	NextRunAt time.Time `json:"next_run_at,omitempty"`
}

// Scheduled reports whether this action runs by itself.
func (a NodeAction) Scheduled() bool { return strings.TrimSpace(a.Schedule) != "" }

// StaleAfter is the staleness budget as a duration, or zero when nothing is
// watching.
func (a NodeAction) StaleAfter() time.Duration {
	if a.StaleAfterSeconds <= 0 {
		return 0
	}
	return time.Duration(a.StaleAfterSeconds) * time.Second
}

// NextRun is when this action is next due, given when it last ran.
//
// The anchor is the last run rather than the last success on purpose: a job
// that fails is not a job that should retry every pass, and a schedule is a
// cadence rather than a promise. The staleness watch is what notices the
// failures — that separation is why it exists.
func (a NodeAction) NextRun(now time.Time) time.Time {
	schedule, err := ParseSchedule(a.Schedule)
	if err != nil || !schedule.Set() {
		return time.Time{}
	}
	anchor := a.LastRunAt
	if anchor.IsZero() {
		// Never run: measured from when the schedule was written, so the first
		// fire is one period after somebody typed it — and, crucially, is a
		// fixed point. Anchoring a never-run action on "now" would move its due
		// time forward on every pass, which is a job that is always about to
		// run and never does.
		anchor = a.ScheduleFrom
	}
	if anchor.IsZero() {
		anchor = a.CreatedAt
	}
	if anchor.IsZero() {
		anchor = now
	}
	return schedule.Next(anchor)
}

// Stale reports whether this action has gone too long without a successful run,
// and since when. The "since" is what an alert is worth reading: "no successful
// dump since 02:00" says more than "stale".
func (a NodeAction) Stale(now time.Time) (bool, time.Time) {
	budget := a.StaleAfter()
	if budget == 0 {
		return false, time.Time{}
	}
	since := a.LastOKAt
	if since.IsZero() {
		// Never succeeded. Measured from when the action was created, so a job
		// that has never worked reports after one budget rather than never.
		since = a.CreatedAt
	}
	if since.IsZero() {
		return false, time.Time{}
	}
	return now.Sub(since) > budget, since
}

// Run is what came back from running one command on one machine.
//
// Output is combined stdout and stderr, in the order the machine produced it,
// because a command that failed usually explains itself on one of the two and
// which one is not worth making the reader think about.
type Run struct {
	ActionID   int64     `json:"action_id,omitempty"`
	Command    string    `json:"command"`
	Output     string    `json:"output"`
	ExitCode   int       `json:"exit_code"`
	DurationMS float64   `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
	RanAt      time.Time `json:"ran_at"`
	// Trigger says who asked: a person pressing the button, or the schedule.
	// Stored, because "it has been running fine" and "somebody has been running
	// it by hand every morning" are different states of the same job.
	Trigger string `json:"trigger,omitempty"`
	// Outcome is the row's verdict, so a history can be scanned without
	// re-deriving it from an exit code and an error string — and so a skipped
	// run has somewhere to be recorded, which is the one outcome that has no
	// exit code at all.
	Outcome string `json:"outcome,omitempty"`

	// The rest is only set when a run is read back out of the history.
	ID         int64  `json:"id,omitempty"`
	NodeID     int64  `json:"node_id,omitempty"`
	ActionName string `json:"action_name,omitempty"`
}

const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
)

const (
	OutcomeOK     = "ok"
	OutcomeFailed = "failed"
	// OutcomeSkipped is a scheduled run that did not happen because the
	// previous one was still going. Recorded rather than silently dropped: a
	// dump that now takes longer than its interval is a thing you want to see
	// as a row of skips, not as a backup that quietly halved its frequency.
	OutcomeSkipped = "skipped"
)

// Result is the verdict of a finished run.
func (r Run) Result() string {
	if r.Outcome != "" {
		return r.Outcome
	}
	if r.Error != "" || r.ExitCode != 0 {
		return OutcomeFailed
	}
	return OutcomeOK
}

// HostStats is one sample of what a machine is doing — asked of the machine
// itself, over SSH, because nothing else can answer it.
//
// The health check says whether the service replied, and a provider's API
// says whether the box is powered on. Neither knows that the disk is full or
// that something is eating the memory, and on a machine where the health
// endpoint belongs to a container, "the app is fine" and "the host is not"
// are routinely both true at once. That gap is what this closes.
//
// Everything here is read-only and comes from one command: /proc, df, and
// docker ps if there is a docker. A machine that answers none of it is not an
// error — a container host with no /proc is not a thing, but a df that fails
// on one mount should not throw away the memory figure that came with it.
type HostStats struct {
	At time.Time `json:"at"`
	// CPUPercent is busy time over the interval between this sample and the
	// one before it, which is why the first sample after a restart has none:
	// /proc/stat counts since boot, and a percentage from a single reading
	// would be the machine's whole life averaged, not what it is doing now.
	CPUPercent  float64 `json:"cpu_percent"`
	HasCPU      bool    `json:"has_cpu"`
	Load1       float64 `json:"load_1"`
	Load5       float64 `json:"load_5"`
	Load15      float64 `json:"load_15"`
	CPUCount    int     `json:"cpu_count,omitempty"`
	MemUsedKB   int64   `json:"mem_used_kb"`
	MemTotalKB  int64   `json:"mem_total_kb"`
	DiskUsedKB  int64   `json:"disk_used_kb"`
	DiskTotalKB int64   `json:"disk_total_kb"`
	// UptimeSeconds is the host's, not the service's. A box that has been up
	// for four minutes explains a great many things.
	UptimeSeconds float64 `json:"uptime_seconds"`
	// Containers is what docker says it is running, when there is a docker and
	// the login can talk to it. Empty means one of: no docker, no permission,
	// or nothing running — told apart by DockerError rather than guessed at.
	Containers  []Container `json:"containers,omitempty"`
	DockerError string      `json:"docker_error,omitempty"`
	// Error is set when the sample could not be taken at all: no login, no
	// answer, a refused password. A machine that cannot be asked says so
	// rather than showing zeroes, which read as "idle".
	Error string `json:"error,omitempty"`

	// The raw counters this sample was computed from. Kept so the next sample
	// can take a difference: the percentage is a rate, and a rate needs two
	// readings.
	CPUBusy  int64 `json:"-"`
	CPUTotal int64 `json:"-"`
}

// Container is one docker container as `docker ps` describes it.
type Container struct {
	Name   string `json:"name"`
	Image  string `json:"image,omitempty"`
	Status string `json:"status"`
	// Up is the status parsed down to the only question a card has room for.
	// Health is part of it: "Up 3 days (unhealthy)" is not up in any sense the
	// person reading this cares about.
	Up bool `json:"up"`
}

// MemPercent and DiskPercent are what the bars draw. Zero totals answer zero
// rather than dividing by them.
func (s HostStats) MemPercent() float64 {
	if s.MemTotalKB <= 0 {
		return 0
	}
	return float64(s.MemUsedKB) / float64(s.MemTotalKB) * 100
}

func (s HostStats) DiskPercent() float64 {
	if s.DiskTotalKB <= 0 {
		return 0
	}
	return float64(s.DiskUsedKB) / float64(s.DiskTotalKB) * 100
}

const (
	// DefaultStatsIntervalSeconds is a minute, where the health check is three
	// seconds. Each sample is a fresh SSH handshake — a connection, a key
	// exchange, an authentication — and doing that every three seconds to
	// twenty machines would make guard the busiest thing on the network it is
	// supposed to be watching.
	DefaultStatsIntervalSeconds = 60
	// MinStatsIntervalSeconds is ten for the same reason: below it the samples
	// start overlapping their own handshakes.
	MinStatsIntervalSeconds = 10
	MaxStatsIntervalSeconds = 3600
)

// StatsInterval is the sampling cadence, or zero when it is off. Off is a
// normal answer: a machine with no stored login has nothing to ask.
func (n Node) StatsInterval() time.Duration {
	if n.StatsIntervalSeconds <= 0 {
		return 0
	}
	seconds := n.StatsIntervalSeconds
	if seconds < MinStatsIntervalSeconds {
		seconds = MinStatsIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// Check is one probe of one node.
type Check struct {
	OK         bool      `json:"ok"`
	StatusCode int       `json:"status_code"`
	LatencyMS  float64   `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

const (
	StatusUp      = "up"
	StatusDown    = "down"
	StatusUnknown = "unknown"
)

const (
	// DefaultIntervalSeconds is what a node gets when nobody chose. Three
	// seconds is the same cadence the dashboard refreshes at, so a node's state
	// on screen is never much older than the screen itself.
	DefaultIntervalSeconds = 3
	// MinIntervalSeconds exists because the interval is a number a person types
	// into a form, and zero would mean an unbounded loop against someone's
	// production health endpoint.
	MinIntervalSeconds = 1
	MaxIntervalSeconds = 3600
)

// Linked reports whether this machine has an instance behind it. The
// dashboard asks before drawing a provider strip, and every provider
// endpoint asks before doing anything at all.
func (n Node) Linked() bool {
	return n.ProviderAccountID > 0 && strings.TrimSpace(n.ProviderInstanceID) != ""
}

// Interval is the checking cadence as a duration, with the default applied.
func (n Node) Interval() time.Duration {
	seconds := n.IntervalSeconds
	if seconds < MinIntervalSeconds {
		seconds = DefaultIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

// ClusterGroup is the instances guard believes run on one machine.
//
// The belief comes from the telemetry itself: a span carrying
// url.full=http://vps-1:8000/api/health was served by whatever answers at that
// host, and the node watching that host is the machine it ran on. Nothing is
// inferred from names — two services called "api" on different boxes are two
// different things, and guessing they are the same would be worse than leaving
// them apart.
type ClusterGroup struct {
	Node      *Node      `json:"node,omitempty"`
	Instances []Instance `json:"instances"`
	// Hosts is what the match was made on, so a grouping that looks wrong can
	// be argued with rather than only disbelieved.
	Hosts []string `json:"hosts,omitempty"`
}

// ClusterTopology is the whole picture: what runs where, and what could not be
// placed.
//
// Unassigned is not a failure state. Plenty of telemetry carries no host at
// all — a background worker with no HTTP surface has nothing to match on — and
// a service quietly filed under the wrong machine would be worse than one
// openly filed under none.
type ClusterTopology struct {
	Groups     []ClusterGroup `json:"groups"`
	Unassigned []Instance     `json:"unassigned"`
}

// ClusterSummary is the one-line answer: how many are up, and are any of them
// down right now.
type ClusterSummary struct {
	Nodes   int    `json:"nodes"`
	Up      int    `json:"up"`
	Down    int    `json:"down"`
	Unknown int    `json:"unknown"`
	Worst   string `json:"worst,omitempty"`
}

// Validate runs before the handler, and in the browser too — the settings form
// imports this package, so a URL guard could never poll is rejected before it
// costs a round trip.
func (n Node) Validate() error {
	if strings.TrimSpace(n.Name) == "" {
		return errors.New("name is required")
	}
	if len(n.Name) > 80 {
		return errors.New("name must be 80 characters or fewer")
	}
	// Zero means "not chosen", which the store fills in. A negative or absurd
	// number is a mistake worth naming rather than clamping silently.
	if n.IntervalSeconds != 0 && (n.IntervalSeconds < MinIntervalSeconds || n.IntervalSeconds > MaxIntervalSeconds) {
		return fmt.Errorf("check interval must be between %d and %d seconds", MinIntervalSeconds, MaxIntervalSeconds)
	}
	// Zero is off, which is a choice rather than a mistake. Anything else has
	// to be a cadence an SSH handshake can keep up with.
	if n.StatsIntervalSeconds != 0 &&
		(n.StatsIntervalSeconds < MinStatsIntervalSeconds || n.StatsIntervalSeconds > MaxStatsIntervalSeconds) {
		return fmt.Errorf("stats interval must be 0, or between %d and %d seconds",
			MinStatsIntervalSeconds, MaxStatsIntervalSeconds)
	}
	if domain := strings.TrimSpace(n.Domain); domain != "" {
		if err := ValidateNodeURL(domain); err != nil {
			return fmt.Errorf("domain: %w", err)
		}
	}
	if internal := strings.TrimSpace(n.InternalURL); internal != "" {
		if err := ValidateNodeURL(internal); err != nil {
			return fmt.Errorf("internal address: %w", err)
		}
	}
	if err := ValidateHealthPath(n.HealthPath); err != nil {
		return err
	}
	if err := ValidateSSHAddress(n.SSHAddress); err != nil {
		return err
	}
	for _, action := range n.Actions {
		if err := action.Validate(); err != nil {
			return err
		}
	}
	if len(n.Tags) > MaxTagsPerNode {
		return fmt.Errorf("a machine can carry at most %d tags", MaxTagsPerNode)
	}
	for _, tag := range n.Tags {
		if err := tag.Validate(); err != nil {
			return err
		}
	}
	// Something has to be probed. The two addresses are the modern way to say
	// it and URL is the old one, so a node is valid if it has either.
	if strings.TrimSpace(n.Domain) == "" && strings.TrimSpace(n.InternalURL) == "" {
		if strings.TrimSpace(n.URL) == "" {
			return errors.New("a domain or an internal address is required")
		}
		return ValidateNodeURL(n.URL)
	}
	return nil
}

// ProbeURL is the address the prober actually fetches: the address with the
// health path on the end.
//
// The health path belongs to the address, not to the SSH host. A machine
// answering http://localhost:8000 today and https://api.example.com tomorrow
// keeps the same /api/health either way, and the box's own IP is never where
// the check is aimed unless somebody typed it as the address.
func (n Node) ProbeURL() string {
	base := strings.TrimSpace(n.Domain)
	if base == "" {
		base = strings.TrimSpace(n.InternalURL)
	}
	if base == "" {
		return strings.TrimSpace(n.URL)
	}
	path := strings.TrimSpace(n.HealthPath)
	if path == "" || path == "/" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(base, "/") + path
}

// ValidateHealthPath keeps the path a path. It is concatenated onto an address
// somebody else typed, so a value carrying a scheme or a host would quietly
// repoint the probe at another machine.
func ValidateHealthPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") {
		return errors.New("health path must start with /, like /api/health")
	}
	if len(path) > 512 {
		return errors.New("health path is too long")
	}
	if strings.ContainsAny(path, " \t\r\n\"'<>\\") || strings.Contains(path, "//") {
		return errors.New("health path must be a plain path, like /api/health")
	}
	return nil
}

// ValidateSSHAddress accepts user@host and user@host:port, and nothing else.
//
// Narrow on purpose: this string is handed to a dialer, and the shapes people
// reach for instead — a whole ssh:// URL, an -o flag, a second hop — are each a
// way to make guard connect somewhere other than the machine on the row.
func ValidateSSHAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	if len(address) > 255 {
		return errors.New("ssh address is too long")
	}
	user, hostPort, found := strings.Cut(address, "@")
	if !found {
		return errors.New("ssh address needs a user, like root@10.10.10.10")
	}
	if user == "" || strings.ContainsAny(user, " \t/\\:@") {
		return errors.New("ssh user must be a plain name, like root")
	}
	host := hostPort
	if h, port, ok := strings.Cut(hostPort, ":"); ok {
		host = h
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return errors.New("ssh port must be a number between 1 and 65535")
		}
	}
	if host == "" || strings.ContainsAny(host, " \t/\\@") {
		return errors.New("ssh address needs a host, like root@10.10.10.10")
	}
	return nil
}

// SSHDial is the address to dial: the host with a port, defaulting to 22.
func (n Node) SSHDial() (user, hostPort string, ok bool) {
	address := strings.TrimSpace(n.SSHAddress)
	if address == "" {
		return "", "", false
	}
	user, host, found := strings.Cut(address, "@")
	if !found || user == "" || host == "" {
		return "", "", false
	}
	if !strings.Contains(host, ":") {
		host += ":22"
	}
	return user, host, true
}

// Validate is the same promise for an action: a name somebody can read on a
// button, and a command that is not empty.
func (a NodeAction) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return errors.New("every action needs a name")
	}
	if len(a.Name) > 60 {
		return errors.New("action name must be 60 characters or fewer")
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("action %q has no command", a.Name)
	}
	if len(a.Command) > 4000 {
		return fmt.Errorf("action %q is too long", a.Name)
	}
	if _, err := ParseSchedule(a.Schedule); err != nil {
		return fmt.Errorf("action %q: %w", a.Name, err)
	}
	if a.StaleAfterSeconds < 0 {
		return fmt.Errorf("action %q: an alert cannot be set in the past", a.Name)
	}
	if a.StaleAfterSeconds > 0 && a.StaleAfterSeconds < 60 {
		return fmt.Errorf("action %q: an alert threshold under a minute is a false alarm generator", a.Name)
	}
	return nil
}

// ValidateNodeURL is the whole safety story for the prober.
//
// Guard makes an outbound request to whatever this says, on a timer, from
// inside whatever network it runs in. That is the entire point of the feature
// and also its only real risk, so the rule is narrow and explicit: an absolute
// http or https URL with a host. No file://, no gopher://, no scheme-relative
// path that resolves against guard's own origin.
func ValidateNodeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("url is required")
	}
	if len(raw) > 2048 {
		return errors.New("url is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url is not valid: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("url must start with http:// or https://")
	}
	if parsed.Host == "" {
		return errors.New("url needs a host, like https://vps-1.example.com/api/health")
	}
	return nil
}
