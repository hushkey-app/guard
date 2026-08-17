// Package config is every environment variable guard is configured with, as a
// list guard itself knows about — and the form on the settings page that edits
// them.
//
// Guard has always been configured the way a container is: `GUARD_*` in the
// environment, a flag of the same name to override it. That is right for the
// process and wrong for the person, because it means every change is an SSH
// session, a file somebody has to remember the name of, and a restart typed by
// hand. The value that ends up in the file is usually copied from a document
// that lists the names — so guard may as well be that document.
//
// The design is one sentence: **stored values are applied to the process
// environment at startup, and nothing else changes.** Every reader above this
// package still calls os.Getenv or takes a flag, sign-in still builds itself
// from `auth.FromEnv`, and a deployment that sets everything in the unit file
// behaves exactly as it did. There is no second configuration system running
// beside the environment — there is one, and this fills it in.
//
// Which means precedence is decided here, once:
//
//	an explicit flag  >  a stored value  >  the environment  >  the default
//
// A flag typed on the command line wins because it is the escape hatch — the
// thing somebody reaches for when the dashboard has stored something that will
// not start. A stored value outranks the environment because otherwise the
// button would silently lose to a line in a unit file, which is worse than not
// having the button.
//
// Two variables can never be stored: `GUARD_DB_PATH` and `GUARD_SECRET_KEY`.
// Anything needed to open and decrypt the database cannot live inside it, and a
// form that pretended otherwise would be one that bricks an instance when
// somebody edits it. They are shown, read-only, because "where is this
// configured" is a question the page should still answer.
//
// And `GUARD_CONFIG_IGNORE=1` starts guard from the environment alone. It is
// the way back from a stored value that stops guard from starting — the case
// this package has to have an answer for, given that the way you would
// otherwise fix it is the dashboard that is not running.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kind is what an input should be, and what a value has to parse as.
type Kind string

const (
	KindText      Kind = "text"
	KindNumber    Kind = "number"
	KindDuration  Kind = "duration"
	KindList      Kind = "list"
	KindURL       Kind = "url"
	KindMultiline Kind = "multiline"
)

// Groups, in the order the page draws them.
const (
	GroupAccess  = "Access and ingest"
	GroupCluster = "Cluster and loops"
	GroupAlerts  = "Alerts"
	GroupSignIn  = "Signing in"
	GroupUpdates = "Updates"
	GroupVault   = "The vault"
	GroupPaths   = "Paths and keys"
)

// Entry is one variable: what it is called, what it means, and what guard does
// when nothing sets it.
type Entry struct {
	Name    string
	Group   string
	Label   string
	Help    string
	Kind    Kind
	Default string
	// Bootstrap marks a value that cannot be stored, because guard needs it
	// before it can read the database that would hold it.
	Bootstrap bool
	// Hidden marks a value that is never sent to a browser at all. Exactly one
	// qualifies: the key that seals every secret in the database, which is not
	// a credential somebody pastes into a collector — it is the thing that
	// makes the backup readable, and it cannot be rotated without re-sealing
	// everything.
	Hidden bool
	// Vault marks a value the second binary reads. It is applied there too, so
	// the form is not quietly lying about half its rows.
	Vault bool
	// Generate marks a value guard can mint: 32 random bytes as hex, the same
	// thing `openssl rand -hex 32` produces.
	//
	// Two rows qualify and no others, which is a rule about what a credential *is*
	// rather than a gap. These two are opaque bearer tokens whose only property is
	// being unguessable, so a random one is strictly better than a typed one. An
	// OAuth client secret is issued by Google; an alert token is issued by whoever
	// receives the alert; `GUARD_SECRET_KEY` could be minted and must never be
	// minted from a button, because changing it orphans every sealed row in the
	// database. A generate button on any of those would be a button that produces
	// a value the far end has never heard of.
	Generate bool
}

// Entries is the catalogue. Adding a variable to guard means adding it here, and
// the page grows a field — there is no template to edit and no endpoint to
// write.
//
// Retention and the event cap are deliberately absent: they are first-class
// rows in the settings table, applied the moment they are saved rather than at
// the next start, and a second place to type them would be a second answer.
var Entries = []Entry{
	{
		Name: "GUARD_TOKEN", Group: GroupAccess, Label: "Operator token", Kind: KindText, Generate: true,
		Help: "Opens every write endpoint in the API as well as ingest. This one is yours — do not put it on a collector.",
	},
	{
		Name: "GUARD_OTEL_SECRET", Group: GroupAccess, Label: "Collector secret", Kind: KindText, Generate: true,
		Help: "Opens /v1/logs, /v1/traces and /v1/metrics and nothing else — the one to hand to a collector on somebody else's box.",
	},
	{
		Name: "GUARD_RUM_ORIGINS", Group: GroupAccess, Label: "Browser origins", Kind: KindList,
		Help: "Comma-separated origins allowed to post browser telemetry. Empty turns the browser intake off entirely.",
	},
	{
		Name: "GUARD_RUM_SERVICE", Group: GroupAccess, Label: "Browser service name", Kind: KindText, Default: "browser",
		Help: "The service identity guard assigns to browser spans. It is never taken from the payload.",
	},
	{
		Name: "GUARD_RUM_RELEASE", Group: GroupAccess, Label: "Browser release", Kind: KindText,
		Help: "Reported as the service instance for browser telemetry.",
	},
	{
		Name: "GUARD_RUM_PER_MINUTE", Group: GroupAccess, Label: "Browser rate limit", Kind: KindNumber, Default: "120",
		Help: "Requests a minute, per address, at the browser intake.",
	},
	{
		Name: "GUARD_CLUSTER_INTERVAL", Group: GroupCluster, Label: "Prober idle wait", Kind: KindDuration, Default: "30s",
		Help: "How long the prober waits when no machine is due; each machine carries its own interval.",
	},
	{
		Name: "GUARD_CLUSTER_TIMEOUT", Group: GroupCluster, Label: "Health check timeout", Kind: KindDuration, Default: "5s",
		Help: "How long one health check may take before it counts as down.",
	},
	{
		Name: "GUARD_SSH_TIMEOUT", Group: GroupCluster, Label: "Command timeout", Kind: KindDuration, Default: "2m",
		Help: "How long a command run from the cluster page may take.",
	},
	{
		Name: "GUARD_SCHEDULE_TIMEOUT", Group: GroupCluster, Label: "Scheduled run timeout", Kind: KindDuration, Default: "30m",
		Help: "How long a scheduled command may take. Longer than a pressed one, because the jobs people schedule are dumps.",
	},
	{
		Name: "GUARD_MONITOR_INTERVAL", Group: GroupCluster, Label: "Monitor interval", Kind: KindDuration, Default: "30s",
		Help: "How often the machine rules are evaluated.",
	},
	{
		Name: "GUARD_VIEW_ALERT_INTERVAL", Group: GroupCluster, Label: "View alert interval", Kind: KindDuration, Default: "1m",
		Help: "How often saved views carrying a rule are run.",
	},
	{
		Name: "GUARD_ALERT_INTERVAL", Group: GroupAlerts, Label: "Staleness check interval", Kind: KindDuration, Default: "5m",
		Help: "How often guard checks that scheduled commands are still succeeding.",
	},
	{
		Name: "GUARD_ALERT_REPEAT", Group: GroupAlerts, Label: "Repeat quiet period", Kind: KindDuration, Default: "6h",
		Help: "How long anything firing stays quiet between repeats.",
	},
	{
		Name: "GUARD_GOOGLE_CLIENT_ID", Group: GroupSignIn, Label: "Google client id", Kind: KindText,
		Help: "Set both halves to draw the Google button. Half a configuration is fatal at startup, on purpose.",
	},
	{
		Name: "GUARD_GOOGLE_CLIENT_SECRET", Group: GroupSignIn, Label: "Google client secret", Kind: KindText,
		Help: "The other half of the Google credentials.",
	},
	{
		Name: "GUARD_APPLE_CLIENT_ID", Group: GroupSignIn, Label: "Apple services id", Kind: KindText,
		Help: "The Services ID, not the app id.",
	},
	{
		Name: "GUARD_APPLE_TEAM_ID", Group: GroupSignIn, Label: "Apple team id", Kind: KindText,
	},
	{
		Name: "GUARD_APPLE_KEY_ID", Group: GroupSignIn, Label: "Apple key id", Kind: KindText,
	},
	{
		Name: "GUARD_APPLE_PRIVATE_KEY", Group: GroupSignIn, Label: "Apple private key", Kind: KindMultiline,
		Help: "The .p8 contents. Paste it whole, including the BEGIN and END lines.",
	},
	{
		Name: "GUARD_APPLE_PRIVATE_KEY_FILE", Group: GroupSignIn, Label: "Apple private key file", Kind: KindText,
		Help: "Read instead of the key above, when the key is a file on the box.",
	},
	{
		Name: "GUARD_ADMIN_EMAIL", Group: GroupSignIn, Label: "Always-admin addresses", Kind: KindList,
		Help: "Comma-separated. Checked beside the members table rather than seeded into it, so this is the way back in when the last stored admin removes themselves.",
	},
	{
		Name: "GUARD_AUTH_BASE_URL", Group: GroupSignIn, Label: "Public base URL", Kind: KindURL,
		Help: "Pin this behind a proxy: the redirect URI is compared as a string at both providers.",
	},
	{
		Name: "GUARD_AUTH_SESSION_TTL", Group: GroupSignIn, Label: "Session lifetime", Kind: KindDuration, Default: "168h",
		Help: "How long a sign-in lasts.",
	},
	{
		Name: "GUARD_UPDATE_REPO", Group: GroupUpdates, Label: "Release repository", Kind: KindText, Default: "hushkey-app/guard",
		Help: "Empty watches nothing at all, which is what an instance with no outbound internet should do.",
	},
	{
		Name: "GUARD_UPDATE_INTERVAL", Group: GroupUpdates, Label: "Release check interval", Kind: KindDuration, Default: "15m",
		Help: "How often guard asks GitHub. Server-side, so four open tabs still cost one request.",
	},
	{
		Name: "GUARD_UPDATE_STATE", Group: GroupUpdates, Label: "Version file", Kind: KindText, Default: "/etc/guard/version",
		Help: "The file deploy/guard-update reads to know which version this box should be on.",
	},
	{
		Name: "GUARD_VAULT_ADDR", Group: GroupVault, Label: "Vault listen address", Kind: KindText, Default: ":4319", Vault: true,
		Help: "127.0.0.1 serves this box alone; the private address serves the VPC. Never 0.0.0.0.",
	},
	{
		Name: "GUARD_VAULT_TOUCH", Group: GroupVault, Label: "Key use throttle", Kind: KindDuration, Default: "1m", Vault: true,
		Help: "How often one key's use is recorded.",
	},
	{
		Name: "GUARD_DB_PATH", Group: GroupPaths, Label: "SQLite database", Kind: KindText, Default: "guard.db",
		Bootstrap: true, Vault: true,
		Help: "Where this database is. It cannot be stored in the database it names.",
	},
	{
		Name: "GUARD_SECRET_KEY", Group: GroupPaths, Label: "Secret key", Kind: KindText,
		Bootstrap: true, Hidden: true, Vault: true,
		Help: "What the SSH passwords, the stored secrets and this configuration are sealed with. Unset, guard generates <db>.key beside the database. It is never shown and never stored here — it is what makes the rest readable.",
	},
}

// Lookup finds an entry by variable name.
func Lookup(name string) (Entry, bool) {
	for _, entry := range Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return Entry{}, false
}

// Store is the half of the database this package uses. An interface, so the
// vault — which has no write methods anywhere and is not about to grow one —
// can pass its own read-only store in.
type Store interface {
	Config() (map[string]string, error)
}

// Writer is a store that can also be changed: guard, and nothing else.
type Writer interface {
	Store
	SetConfig(values map[string]string) error
}

// Set is the configuration this process is running.
type Set struct {
	store Writer
	// booted is the effective value of every entry at startup, so the page can
	// say "saved, but this process is still running the old one" rather than
	// implying a change that has not happened.
	booted map[string]string
	// pristine is the environment as the box provides it, captured before the
	// stored values were applied over it. Everything that asks "what would this
	// fall back to" has to ask this rather than os.Getenv, which by then holds
	// what the database said — otherwise clearing a field looks like it keeps
	// its old value, and the checks below would pass a configuration that stops
	// the next start.
	pristine map[string]string
	// applied are the names that came from the database this start, for the one
	// log line that says so. Names only — several of these are credentials.
	applied []string
	// restart asks the supervisor for a new process. Nil where nothing would
	// bring guard back, and then the page says to restart it by hand.
	restart func()
}

// Supervised reports whether something will start guard again if it exits.
// systemd sets INVOCATION_ID for every service it runs, so the answer is the
// supervisor's own word rather than a setting somebody has to keep true.
func Supervised() bool { return os.Getenv("INVOCATION_ID") != "" }

// Ignored reports whether stored configuration is being skipped this start.
func Ignored() bool {
	value := strings.TrimSpace(os.Getenv("GUARD_CONFIG_IGNORE"))
	return value != "" && value != "0" && !strings.EqualFold(value, "false")
}

// Apply puts the stored values into the process environment.
//
// Called before anything reads its configuration, which is why it takes a store
// and not a Set: at this point in a start, the store is the only thing that
// exists. Everything downstream then reads the environment as it always has.
//
// A stored value overwrites what the environment already says. That is the
// precedence the page promises, and the flags keep their own escape hatch: an
// explicitly typed one wins, because main re-derives only the flags nobody
// passed.
func Apply(store Store) ([]string, error) {
	if Ignored() {
		slog.Warn("GUARD_CONFIG_IGNORE is set — stored configuration is being skipped this start")
		return nil, nil
	}
	values, err := store.Config()
	if err != nil {
		return nil, err
	}
	var applied []string
	for _, entry := range Entries {
		value, ok := values[entry.Name]
		if !ok || value == "" {
			continue
		}
		if entry.Bootstrap {
			// Refused at save time as well; this is the second half of that,
			// for a row written by an older build or by hand with sqlite3.
			slog.Warn("ignoring stored configuration that cannot be applied at this point",
				slog.String("name", entry.Name))
			continue
		}
		if err := os.Setenv(entry.Name, value); err != nil {
			return applied, err
		}
		applied = append(applied, entry.Name)
	}
	sort.Strings(applied)
	if len(applied) > 0 {
		// Names, never values: this line goes to the same log guard ingests.
		slog.Info("configuration applied from the database", slog.Any("names", applied))
	}
	return applied, nil
}

// Load applies the stored configuration and remembers what this process ended
// up running, so the page can tell a saved value from a live one.
func Load(store Writer) (*Set, error) {
	set := &Set{store: store, booted: map[string]string{}, pristine: map[string]string{}}
	// In development, the .env in the working directory is the environment: read
	// before the pristine snapshot, so a value in it reads as "from the
	// environment" on the page and falls back to it when a stored one is cleared.
	if path, read := readDotEnv(); len(read) > 0 {
		slog.Info("read the development .env", slog.String("path", path), slog.Any("names", read))
	}
	for _, entry := range Entries {
		set.pristine[entry.Name] = os.Getenv(entry.Name)
	}
	applied, err := Apply(store)
	if err != nil {
		return nil, err
	}
	set.applied = applied
	for _, entry := range Entries {
		set.booted[entry.Name] = firstOf(os.Getenv(entry.Name), entry.Default)
	}
	return set, nil
}

// Restartable wires in how this process can be restarted. Same function the
// credentials card uses: guard exits and its supervisor brings it back.
func (s *Set) Restartable(restart func()) { s.restart = restart }

// Restart asks for the new environment to be read.
func (s *Set) Restart() error {
	if s.restart == nil {
		return errors.New("nothing here would start guard again — restart the service by hand")
	}
	slog.Warn("restarting to pick up stored configuration")
	s.restart()
	return nil
}

// Value is one row of the form.
type Value struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Label   string `json:"label"`
	Help    string `json:"help,omitempty"`
	Kind    Kind   `json:"kind"`
	Default string `json:"default,omitempty"`
	// Value is what the next start will use. Empty for a hidden entry, which
	// reports only that something is set.
	Value string `json:"value,omitempty"`
	// Source is where that came from: stored, environment, or default.
	Source string `json:"source"`
	// Pending says the value differs from what this process is running.
	Pending   bool `json:"pending"`
	Bootstrap bool `json:"bootstrap,omitempty"`
	Hidden    bool `json:"hidden,omitempty"`
	IsSet     bool `json:"is_set"`
	Vault     bool `json:"vault,omitempty"`
	// Generatable says the row gets a Generate button.
	Generatable bool `json:"generatable,omitempty"`
}

// Group is a titled section of the form.
type Group struct {
	Name   string  `json:"name"`
	Values []Value `json:"values"`
}

// State is the whole form, plus what pressing things will do.
type State struct {
	Groups []Group `json:"groups"`
	// Pending says at least one saved value is not in force yet.
	Pending bool `json:"pending"`
	// Restartable says something will start guard again if it exits.
	Restartable bool `json:"restartable"`
	// Ignored says this process was started with GUARD_CONFIG_IGNORE, so what
	// is stored is not what is running and saving changes nothing until that
	// is removed. Silence here would be a page that appears to do nothing.
	Ignored bool `json:"ignored"`
}

// State reads the database and reports the form.
func (s *Set) State() (State, error) {
	stored, err := s.store.Config()
	if err != nil {
		return State{}, err
	}
	state := State{Restartable: s.restart != nil, Ignored: Ignored()}
	byGroup := map[string]*Group{}
	for _, entry := range Entries {
		value := Value{
			Name: entry.Name, Group: entry.Group, Label: entry.Label, Help: entry.Help,
			Kind: entry.Kind, Default: entry.Default,
			Bootstrap: entry.Bootstrap, Hidden: entry.Hidden, Vault: entry.Vault,
			Generatable: entry.Generate,
		}
		next := entry.Default
		switch {
		case stored[entry.Name] != "" && !entry.Bootstrap:
			next, value.Source = stored[entry.Name], "stored"
		case s.pristine[entry.Name] != "":
			next, value.Source = s.pristine[entry.Name], "environment"
		default:
			value.Source = "default"
		}
		value.IsSet = next != ""
		// A bootstrap entry reports where it came from, and a hidden one does
		// not report the value at all.
		if !entry.Hidden {
			value.Value = next
		}
		if !entry.Bootstrap && next != s.booted[entry.Name] {
			value.Pending = true
			state.Pending = true
		}
		group := byGroup[entry.Group]
		if group == nil {
			group = &Group{Name: entry.Group}
			byGroup[entry.Group] = group
		}
		group.Values = append(group.Values, value)
	}
	// In catalogue order rather than map order, because the order the form is
	// read in is part of the form.
	for _, name := range groupOrder() {
		if group := byGroup[name]; group != nil {
			state.Groups = append(state.Groups, *group)
		}
	}
	return state, nil
}

// Save validates and stores, and refuses the whole set rather than part of it.
//
// A half-written pair is the failure mode that matters here: guard treats a
// client id with no secret as a fatal misconfiguration at startup, deliberately,
// so a save that left one behind would be a save that stops the next start.
func (s *Set) Save(values map[string]string) (State, error) {
	if len(values) == 0 {
		return s.State()
	}
	clean := map[string]string{}
	for name, value := range values {
		entry, ok := Lookup(name)
		if !ok {
			return State{}, fmt.Errorf("%q is not a setting guard knows about", name)
		}
		if entry.Bootstrap {
			return State{}, fmt.Errorf("%s cannot be stored — guard needs it before it can read the database, so it belongs in the unit file", name)
		}
		value = strings.TrimSpace(value)
		if entry.Kind == KindMultiline {
			// A PEM key's own line breaks are the value; only the edges go.
			value = strings.Trim(values[name], " \t\r\n")
		}
		if err := validate(entry, value); err != nil {
			return State{}, err
		}
		clean[name] = value
	}
	stored, err := s.store.Config()
	if err != nil {
		return State{}, err
	}
	for name, value := range clean {
		if value == "" {
			delete(stored, name)
			continue
		}
		stored[name] = value
	}
	if err := s.pairsComplete(stored); err != nil {
		return State{}, err
	}
	if err := s.store.SetConfig(clean); err != nil {
		return State{}, err
	}
	names := make([]string, 0, len(clean))
	for name := range clean {
		names = append(names, name)
	}
	sort.Strings(names)
	slog.Info("configuration saved", slog.Any("names", names))
	// And into the development .env, so the file in the checkout is what guard
	// holds. A failure to mirror is not a failure to save: the database is the
	// store, and this is a convenience for everything else that reads a .env.
	if err := writeDotEnv(stored); err != nil {
		slog.Warn("could not write the development .env", slog.Any("err", err))
	}
	return s.State()
}

// Generate mints a value for one of the credentials and stores it.
//
// Minting rather than typing, because these two are opaque tokens whose only
// property is being unguessable — and because the alternative is somebody
// reaching for a shell to run `openssl rand -hex 32`, which is the step this whole
// page exists to remove. It goes through Save, so it is validated, logged and
// pending a restart exactly like a value that was typed.
func (s *Set) Generate(name string) (State, error) {
	entry, ok := Lookup(name)
	if !ok {
		return State{}, fmt.Errorf("%q is not a setting guard knows about", name)
	}
	if !entry.Generate {
		return State{}, fmt.Errorf("%s is not a value guard can invent — it has to come from whatever issues it", name)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return State{}, fmt.Errorf("could not generate a credential: %w", err)
	}
	return s.Save(map[string]string{name: hex.EncodeToString(buf)})
}

func validate(entry Entry, value string) error {
	if value == "" {
		return nil
	}
	switch entry.Kind {
	case KindNumber:
		number, err := strconv.Atoi(value)
		if err != nil || number <= 0 {
			return fmt.Errorf("%s wants a whole number above zero", entry.Name)
		}
	case KindDuration:
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("%s wants a duration like 30s, 5m or 6h", entry.Name)
		}
	case KindURL:
		if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return fmt.Errorf("%s wants a URL starting with http:// or https://", entry.Name)
		}
	}
	// The value becomes an environment variable, and a newline in one is a
	// second variable as far as anything reading a .env file is concerned.
	if entry.Kind != KindMultiline && strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%s cannot contain a line break", entry.Name)
	}
	return nil
}

// pairsComplete refuses a configuration guard would refuse to start on.
//
// It is the same rule as internal/auth's, checked at the moment somebody could
// still fix it: half a provider's credentials means somebody meant to close the
// door and mistyped, and the honest answer is to say so here rather than at the
// next restart, from a log file, with the dashboard down.
func (s *Set) pairsComplete(values map[string]string) error {
	get := func(name string) string { return strings.TrimSpace(s.resolve(values, name)) }
	if (get("GUARD_GOOGLE_CLIENT_ID") == "") != (get("GUARD_GOOGLE_CLIENT_SECRET") == "") {
		return errors.New("Google sign-in needs both the client id and the client secret, or neither")
	}
	apple := []string{"GUARD_APPLE_CLIENT_ID", "GUARD_APPLE_TEAM_ID", "GUARD_APPLE_KEY_ID"}
	set, missing := 0, []string{}
	for _, name := range apple {
		if get(name) != "" {
			set++
		} else {
			missing = append(missing, name)
		}
	}
	key := get("GUARD_APPLE_PRIVATE_KEY") != "" || get("GUARD_APPLE_PRIVATE_KEY_FILE") != ""
	if key {
		set++
	} else {
		missing = append(missing, "GUARD_APPLE_PRIVATE_KEY")
	}
	if set > 0 && set < 4 {
		return errors.New("Apple sign-in needs all of the services id, team id, key id and private key — missing " + strings.Join(missing, ", "))
	}
	return nil
}

// resolve answers what a start would use for one name, given a set of stored
// values that may not be written yet: the stored value, then the environment
// the box provides, then guard's default.
func (s *Set) resolve(values map[string]string, name string) string {
	entry, _ := Lookup(name)
	return firstOf(values[name], s.pristine[name], entry.Default)
}

func firstOf(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func groupOrder() []string {
	return []string{GroupAccess, GroupCluster, GroupAlerts, GroupSignIn, GroupUpdates, GroupVault, GroupPaths}
}
