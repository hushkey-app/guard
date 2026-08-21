package model

// Deploys: a versioned image put on the machines guard already watches.
//
// The vocabulary is four nouns, and each of them exists because the alternative
// was a second copy of something guard already has:
//
//   - a **group** is a set of machines, by id, out of the cluster. Not a second
//     inventory — adding a machine to guard is still the only way one comes to
//     exist, and a group that drifted from the cluster would be a deploy pointed
//     at a box nobody is watching.
//   - a **template** is the compose file, the service it names, and the
//     variables it expects. It is versioned rather than edited in place, so the
//     run recorded three months ago still says what it actually deployed.
//   - a **run** is one press: a group, a template version, a tag, a mode, and a
//     row per machine it touched.
//   - a **service state** is what is running where. Written only when a machine
//     comes back healthy, which is what makes it safe to roll back to.
//
// The compose file lives in guard rather than on the box, so it travels in the
// backup with everything else — a replacement machine is provisioned by
// deploying to it, not by remembering what was in /srv last year. Guard still
// writes only what guard rendered: a deploy request carries a template id and a
// tag, never file content, so this cannot become a way to drop a chosen file in
// a chosen place. That is the same rule internal/envfile keeps, for the same
// reason.

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DeployGroup is a named set of machines out of the cluster.
//
// A machine may be in several groups — one for the application it runs, one for
// the proxy every box shares — and editing a group changes nothing that already
// happened: a run stores the machines it touched, not a pointer to a list that
// can be edited afterwards.
type DeployGroup struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	NodeIDs []int64 `json:"node_ids"`
	// WebhookID is where this group's deploys speak up: a stopped run says so
	// once immediately and again if nobody has come back. Named per group
	// because a group is an application, and the people who own one are the
	// people to tell. Zero means nobody is told, which is logged and is the
	// same bargain every other rule here makes — with the difference that a
	// sequential run then waits in silence, so the page says so where it is
	// chosen.
	WebhookID int64 `json:"webhook_id,omitempty"`
	// Nodes is the read side: enough of each machine to draw the group without
	// a second request per member.
	Nodes     []DeployMember `json:"nodes,omitempty"`
	CreatedAt time.Time      `json:"created_at,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
}

// DeployMember is one machine as a group draws it: who it is, and whether guard
// can actually deploy to it. A group is allowed to hold a machine with no login
// — that is a normal state on the day somebody adds a box before its
// credentials — and the deploy is what refuses, in words, rather than the group.
type DeployMember struct {
	NodeID      int64  `json:"node_id"`
	Name        string `json:"name"`
	SSHAddress  string `json:"ssh_address,omitempty"`
	HasPassword bool   `json:"has_password"`
	Locked      bool   `json:"locked"`
	// CurrentTag and LastGoodTag are this machine's state for the service the
	// template names, filled in when a group is read in the context of one.
	CurrentTag  string `json:"current_tag,omitempty"`
	LastGoodTag string `json:"last_known_good_tag,omitempty"`
}

// DeployTemplate is a compose file and what it needs, at one version.
//
// ID identifies the template across its revisions and Version identifies the
// revision; a run pins both. Editing a template writes a new row and leaves the
// old one where it is, because the question a deploy record has to answer months
// later is "what did we actually deploy", and a template edited in place answers
// it with today's file.
type DeployTemplate struct {
	ID      int64  `json:"id"`
	Version int    `json:"version"`
	Name    string `json:"name"`
	// ServiceName is the service inside the compose file — what gets pulled and
	// recreated, and the key a machine's running tag is recorded under.
	ServiceName string `json:"service_name"`
	// Image is the repository the tag hangs off, chosen from Registries rather
	// than typed: a deploy pointed at a mistyped registry path fails in a way
	// that looks like the registry being down.
	Image string `json:"image"`
	// Path is the directory on the machine that holds the compose file and its
	// .env. Everything guard writes for this template goes inside it, and
	// nothing outside it is ever touched.
	Path string `json:"path"`
	// ComposeYAML is the file itself. Guard is the source of truth: it is
	// written on every deploy, so a box rebuilt from nothing needs only the
	// login.
	ComposeYAML string `json:"compose_yaml"`
	// HealthPath is what proves the deploy landed, and it belongs to the
	// application rather than to the machine — the box's own health path
	// answers for whatever was there before. Empty falls back to the node's.
	HealthPath string `json:"health_path,omitempty"`
	// HealthPort is set where the service answers on a port the machine's
	// address does not name. Zero keeps the address's own port.
	HealthPort int `json:"health_port,omitempty"`
	// SecretEnvID is the vault environment the secret variables are read from.
	// One environment per template, so a staging template cannot resolve a
	// production value — the same rule the vault keys keep.
	SecretEnvID int64 `json:"secret_env_id,omitempty"`
	// SecretEnvLabel is the read side: "hushkey / production", so the page can
	// say where the values come from without resolving any.
	SecretEnvLabel string `json:"secret_env_label,omitempty"`
	// Vars is what this template expects beyond TAG.
	Vars      []TemplateVar `json:"vars"`
	CreatedAt time.Time     `json:"created_at,omitempty"`
	// Versions is the read side of the history: every revision, newest first,
	// so the page can show what changed and a run can be pinned to one.
	Versions []DeployTemplateVersion `json:"versions,omitempty"`
}

// DeployTemplateVersion is one revision as a list draws it.
type DeployTemplateVersion struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	// InUse marks a version some run still refers to. A version nobody
	// deployed can be tidied away; one that a record points at cannot.
	InUse bool `json:"in_use,omitempty"`
}

// TemplateVar is one variable the compose file expects.
//
// Two sources, and the difference is where the value is at rest:
//
//   - **static** is stored with the template and written into the .env. A port,
//     a log level, a public URL. Nothing here is secret; the template travels in
//     the backup and is readable on the page.
//   - **vault** is a key in the template's secret environment, resolved at
//     deploy time and written into the .env on the target. Guard never stores a
//     copy, and the .env it writes is 0600 — but it is a plaintext file on the
//     box, and the page says so, because the alternative is discovering it
//     during an incident.
//
// A variable the application fetches from guard-vault itself is simply not
// declared here. That is the third option and it needs no support: the vault key
// is already on the machine, and the deploy has no business seeing the value.
type TemplateVar struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	// Value is the static text. Empty for a vault variable, always.
	Value string `json:"value,omitempty"`
}

// Where a variable's value comes from.
const (
	VarStatic = "static"
	VarVault  = "vault"
)

// The modes a run can be started in.
const (
	// ModeSequential is one machine at a time, each proved healthy before the
	// next is touched. The default, and the only one that protects anything.
	ModeSequential = "sequential"
	// ModeParallel is every machine at once with no gate between them. Health
	// is still checked and still recorded — it just stops nothing, which is the
	// trade being made, and the button says so.
	ModeParallel = "parallel"
)

// A run's status: what the whole press is doing.
const (
	RunRunning = "running"
	// RunAwaiting is a sequential run stopped at a failure with nobody having
	// told it what to do yet. It is a status rather than a pause because
	// somebody has to be told about it — see AwaitingAlert.
	RunAwaiting = "awaiting"
	RunHealthy  = "healthy"
	RunFailed   = "failed"
	// RunAbandoned is an awaiting run nobody came back to. The lock is
	// released and the machines that were never touched still hold what they
	// held; a lock nobody can clear is the worse failure.
	RunAbandoned = "abandoned"
	// RunInterrupted is what a restart leaves behind. Guard cannot know
	// whether `compose up` landed, so it says so rather than guessing.
	RunInterrupted = "interrupted"
	// RunCancelled is somebody pressing stop while it was still going.
	//
	// It is its own word rather than "failed" because nothing failed: a person
	// decided. What it does *not* mean is that anything was undone — a machine
	// already deployed to keeps what it was given, and the one in flight may
	// have a container running that guard never proved. Saying "cancelled" and
	// leaving those rows as they are is the only honest answer; a cancel that
	// implied a rollback would be the most dangerous button on the page.
	RunCancelled = "cancelled"
)

// An instance's status inside a run.
const (
	InstancePending     = "pending"
	InstanceDeploying   = "deploying"
	InstanceHealthCheck = "health_check"
	InstanceHealthy     = "healthy"
	InstanceFailed      = "failed"
	// InstanceSkipped is a machine the operator chose to step over, or one a
	// sequential run never reached.
	InstanceSkipped     = "skipped"
	InstanceInterrupted = "interrupted"
)

// DeployRun is one press, and what became of it.
type DeployRun struct {
	ID      int64 `json:"id"`
	GroupID int64 `json:"group_id"`
	// GroupName, TemplateName, ServiceName and Image are copied onto the run
	// rather than joined. A group renamed or a template deleted must not make
	// the history unreadable — the record is of something that happened, and
	// what it says has to stay true.
	GroupName       string `json:"group_name"`
	TemplateID      int64  `json:"template_id"`
	TemplateVersion int    `json:"template_version"`
	TemplateName    string `json:"template_name"`
	ServiceName     string `json:"service_name"`
	Image           string `json:"image"`
	Tag             string `json:"tag"`
	Mode            string `json:"mode"`
	Status          string `json:"status"`
	// Rollback marks a run that was started from a machine's last known good
	// tag. Nothing behaves differently; it is here so the history reads as what
	// happened rather than as a deploy that happens to go backwards.
	Rollback   bool      `json:"rollback,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// AwaitingSince is when the run stopped at a failure, and what the
	// abandon deadline is measured from.
	AwaitingSince time.Time        `json:"awaiting_since,omitempty"`
	Instances     []DeployInstance `json:"instances"`
}

// DeployInstance is one machine inside a run.
type DeployInstance struct {
	RunID  int64 `json:"run_id"`
	NodeID int64 `json:"node_id"`
	// NodeName is copied for the same reason the run copies the group's name.
	NodeName   string    `json:"node_name"`
	Position   int       `json:"position"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// PreviousTag is what this machine was running before, so a failed instance
	// can offer the one useful button without a second lookup.
	PreviousTag string `json:"previous_tag,omitempty"`
	// Health is the gate's own account: how many checks passed, what the last
	// one said. Recorded in parallel mode too, where it gates nothing.
	Health string `json:"health,omitempty"`
	// Error is why it failed, in the words somebody can act on.
	Error string `json:"error,omitempty"`
	// Output is what the machine said while it was being deployed to — the
	// pull and the up, truncated like every other run's output.
	Output string `json:"output,omitempty"`
}

// Done reports that this machine is not going to change again in this run.
func (i DeployInstance) Done() bool {
	switch i.Status {
	case InstanceHealthy, InstanceFailed, InstanceSkipped, InstanceInterrupted:
		return true
	}
	return false
}

// Tally is the line at the top of a run: "3/5 healthy, 1 failed, 1 pending".
func (r DeployRun) Tally() (healthy, failed, pending int) {
	for _, instance := range r.Instances {
		switch instance.Status {
		case InstanceHealthy:
			healthy++
		case InstanceFailed, InstanceInterrupted:
			failed++
		case InstanceSkipped:
		default:
			pending++
		}
	}
	return healthy, failed, pending
}

// DeployState is what one machine is running for one service.
//
// CurrentTag is what passed a health gate most recently. LastGoodTag is what
// passed the one **before** that, which is the only thing worth calling a
// rollback target.
//
// The first version of this moved both together on every success, which made
// them permanently equal and rollback a no-op in every case — if a deploy
// fails, the machine is still running the last good thing and there is nothing
// to go back to; if it succeeds, "last good" was the thing that just succeeded.
// Stepping the old current into last good on each success is what makes the
// button mean "the one before this".
type DeployState struct {
	NodeID      int64  `json:"node_id"`
	ServiceName string `json:"service_name"`
	CurrentTag  string `json:"current_tag"`
	// CurrentVersion is the template version that tag was deployed with. Both
	// halves matter: what changes between two deploys is often the compose file
	// rather than the image, and a rollback that restored the tag but not the
	// file would put back a version nobody ever ran.
	CurrentVersion  int       `json:"current_version,omitempty"`
	LastGoodTag     string    `json:"last_known_good_tag,omitempty"`
	LastGoodVersion int       `json:"last_known_good_version,omitempty"`
	TemplateID      int64     `json:"template_id,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

// CanRollBack reports that there is a previous good deploy to go back to, and
// that it is not the one already running.
func (s DeployState) CanRollBack() bool {
	// A target has to be fully known. A row written before the version was
	// recorded has none, and offering "v0" would name a template version that
	// never existed — one more good deploy fills it in honestly.
	if s.LastGoodTag == "" || s.LastGoodVersion == 0 {
		return false
	}
	return s.LastGoodTag != s.CurrentTag || s.LastGoodVersion != s.CurrentVersion
}

// The health gate, written here rather than passed in. These are the numbers
// that decide whether a deploy is believed, and a knob on them would be a way
// to make a deploy pass by lowering the bar in the same dialog that starts it.
const (
	// HealthPasses is how many consecutive successes prove it. Three, because
	// one is a process that has not fallen over yet.
	HealthPasses = 3
	// HealthInterval is the wait between checks.
	HealthInterval = 5 * time.Second
	// HealthDeadline is the ceiling. A check that never resolves is a failure,
	// never a run left in health_check forever.
	HealthDeadline = 2 * time.Minute
)

// The deadlines around an operator who has not answered yet.
const (
	// AwaitingAlert is how long a stopped run waits before saying so a second
	// time. It says so once immediately; this is the reminder.
	AwaitingAlert = 15 * time.Minute
	// AwaitingDeadline is when a stopped run gives up, releases its locks and
	// records what it did and did not touch.
	AwaitingDeadline = 30 * time.Minute
)

// servicePattern is what may name a compose service and a state row.
var servicePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// tagPattern is what a registry will accept as a tag. Checked because the tag
// is written into a file on the target, and a value with a newline in it is two
// variables.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// ValidateTag checks one tag.
func ValidateTag(tag string) error {
	if strings.TrimSpace(tag) == "" {
		return errors.New("a deploy needs a tag")
	}
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("%q is not a tag a registry would accept", tag)
	}
	return nil
}

// composeImage finds an `image:` line. Deliberately a regexp over the text
// rather than a YAML parse: the one question being asked is "which image does
// this file tag", and a parser would buy a dependency, an opinion about anchors
// and merge keys, and a second way for a file docker accepts to be refused
// here. What guard writes is the text either way.
var composeImage = regexp.MustCompile(`(?m)^[ \t]*image:[ \t]*["']?([^\s"']+)["']?[ \t]*$`)

// tagRef is how a compose file says "the tag guard is deploying".
var tagRef = regexp.MustCompile(`\$\{TAG(:-[^}]*)?\}|\$TAG\b`)

// ImageInCompose is the image a deploy is actually about: the one the compose
// file tags with ${TAG}.
//
// This is derived rather than typed, and that is worth more than the saved
// keystrokes. A separate image field can disagree with the compose file — it
// names one repository while the file pulls another — and nothing would notice,
// because guard never reads the field at deploy time. Deriving it means the tag
// list on the deploy dialog is always the tag list of the image that is going
// to be pulled.
//
// A file that never mentions ${TAG} is refused, because a deploy of it would
// pull whatever it already said and change nothing — which looks like a
// successful deploy of the wrong version.
func ImageInCompose(compose string) (string, error) {
	found := map[string]bool{}
	order := []string{}
	for _, match := range composeImage.FindAllStringSubmatch(compose, -1) {
		reference := match[1]
		if !tagRef.MatchString(reference) {
			continue
		}
		// Everything up to the tag is the repository. Split on the last colon,
		// because a registry with a port has one too.
		base := reference
		if at := strings.LastIndex(reference, ":"); at > 0 {
			base = reference[:at]
		}
		if !found[base] {
			found[base] = true
			order = append(order, base)
		}
	}
	switch len(order) {
	case 0:
		return "", errors.New("the compose file never uses ${TAG}, so deploying it would not change what is running")
	case 1:
		return order[0], nil
	default:
		return "", errors.New("this compose file tags more than one image (" + strings.Join(order, ", ") +
			") — a template deploys one, so split it into two templates")
	}
}

// Slug is a name made safe to put in a path and a state key.
func Slug(name string) string {
	var out strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
			dash = false
		default:
			if !dash && out.Len() > 0 {
				out.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(out.String(), "-")
}

// DeployRoot is where guard puts what it deploys. One directory it owns, with a
// directory per template inside it — so nothing guard writes is ever mixed in
// with whatever else somebody keeps on the box, and the answer to "what did
// guard put on this machine" is one `ls`.
const DeployRoot = "/guard"

// Normalise fills in what can be worked out, so a template is three answers
// rather than eight.
//
// The service name, the image and the directory are all derivable — from the
// name and from the compose file the person is already writing — and every one
// of them is a field that can be typed wrong in a way nothing checks. They stay
// *stored*, because a run months old has to say where it wrote and what it
// pulled, and because recomputing a path later would leave the old containers
// running in a directory guard had forgotten. Derived once, at the save.
//
// An explicit value is still honoured: the API predates this, and a template
// that has to live somewhere specific should not be a reason to go back to
// typing all three.
func (t *DeployTemplate) Normalise() error {
	t.Name = strings.TrimSpace(t.Name)
	t.ServiceName = strings.TrimSpace(t.ServiceName)
	t.Image = strings.TrimSpace(t.Image)
	t.Path = strings.TrimSpace(t.Path)
	slug := Slug(t.Name)
	if t.ServiceName == "" {
		t.ServiceName = slug
	}
	if t.Path == "" && slug != "" {
		t.Path = DeployRoot + "/" + slug
	}
	if t.Image == "" {
		image, err := ImageInCompose(t.ComposeYAML)
		if err != nil {
			return err
		}
		t.Image = image
	}
	return nil
}

// Validate checks a template before it is stored — including the path,
// which is the one field that decides where guard writes.
func (t DeployTemplate) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("a template needs a name")
	}
	if !servicePattern.MatchString(t.ServiceName) {
		return errors.New("the service name has to match the service in the compose file")
	}
	if strings.TrimSpace(t.Image) == "" {
		return errors.New("choose the image from Registries")
	}
	if err := ValidateDeployPath(t.Path); err != nil {
		return err
	}
	if strings.TrimSpace(t.ComposeYAML) == "" {
		return errors.New("the compose file is empty")
	}
	if err := ValidateHealthPath(t.HealthPath); err != nil {
		return err
	}
	if t.HealthPort < 0 || t.HealthPort > 65535 {
		return errors.New("that is not a port")
	}
	seen := map[string]bool{}
	for _, v := range t.Vars {
		key := strings.TrimSpace(v.Key)
		if err := ValidateSecretKey(key); err != nil {
			return err
		}
		if key == "TAG" {
			return errors.New("TAG is the deploy's own variable and is always written")
		}
		if seen[key] {
			return errors.New(key + " is declared twice")
		}
		seen[key] = true
		switch v.Source {
		case VarStatic:
		case VarVault:
			if t.SecretEnvID == 0 {
				return errors.New(key + " comes from the vault, so the template needs an environment to read it from")
			}
		default:
			return errors.New(key + " has to come from somewhere: a value here, or the vault")
		}
	}
	return nil
}

// ValidateDeployPath is the whole of what stops a template writing anywhere it
// likes. Absolute, no traversal, no shell metacharacters, and not one of the
// directories that would take the machine down with it.
//
// It is a model function rather than a check in the writer because it is the
// rule, and a rule that lives next to one caller grows a second caller without
// it.
func ValidateDeployPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("a template needs a directory on the machine")
	}
	if !strings.HasPrefix(path, "/") {
		return errors.New("the directory has to be absolute")
	}
	if strings.HasSuffix(path, "/") && path != "/" {
		return errors.New("drop the trailing slash")
	}
	if strings.ContainsAny(path, "'\"$`\\ \n\r\t*?[]{}|;&<>()") {
		return errors.New("that directory has characters a shell would read; use a plain path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == "." {
			return errors.New("no . or .. in the directory")
		}
	}
	switch path {
	case "/", "/etc", "/usr", "/bin", "/sbin", "/lib", "/var", "/boot", "/dev", "/proc", "/sys", "/root", "/home":
		return errors.New(path + " is the machine, not a place to put an application")
	}
	return nil
}

// EnvFor renders the .env a deploy writes: the tag first, then the template's
// variables with the vault ones already resolved.
//
// TAG leads because it is the line somebody looks for, and it is written by
// guard rather than declared, which is why the template may not declare it.
func EnvFor(tag string, vars []NodeEnvVar) string {
	all := append([]NodeEnvVar{{Key: "TAG", Value: tag}}, vars...)
	return RenderEnvVars(all)
}

// ProbeURL is where this template's health check is aimed on one machine.
//
// The application's path over the machine's address, because those are two
// different questions: the node's own health path answers for whatever the box
// was already running, and a deploy has to be proved by the thing it deployed.
// A port is set where the service answers on one the address does not name.
// Empty means there is nothing to check, which a gated deploy treats as a
// failure rather than as a pass.
func (t DeployTemplate) ProbeURL(n Node) string {
	base := strings.TrimSpace(n.Domain)
	if base == "" {
		base = strings.TrimSpace(n.InternalURL)
	}
	if base == "" {
		base = strings.TrimSpace(n.URL)
	}
	if base == "" {
		return ""
	}
	if t.HealthPort > 0 {
		parsed, err := url.Parse(base)
		if err != nil || parsed.Host == "" {
			return ""
		}
		parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(t.HealthPort))
		// A port override replaces the address's path too: the service is
		// somewhere else, so whatever the address pointed at is not it.
		parsed.Path = ""
		base = parsed.String()
	}
	path := strings.TrimSpace(t.HealthPath)
	if path == "" {
		path = strings.TrimSpace(n.HealthPath)
	}
	if path == "" || path == "/" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(base, "/") + path
}
