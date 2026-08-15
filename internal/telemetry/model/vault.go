package model

// The secrets an application needs to run, and the keys that hand them out.
//
// This is the one thing in guard whose stored value is meant to come back.
// An SSH password and a webhook token are credentials guard uses on somebody's
// behalf, so they go in sealed and are never read out; a secret is a value an
// application is going to be given, and a store that could only be written to
// would just mean the real copy lives in a file somewhere else. So the values
// are readable — by an admin on the page, and by a key over the vault — and
// everything else about them is the same as every other sealed thing here.

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// A Workspace is one application: pack, hushkey, auth — whatever runs in the
// VPC and needs configuration.
//
// Two levels rather than one, because a flat list of environments is fine for
// one application and unreadable for eight: "hushkey-production" beside
// "auth-production" beside "pack-production" is a naming convention doing a
// schema's job, and the first person to type "hushkey_production" breaks it.
// A workspace owns its environments, so one application can have a preview
// stage that the others do not.
type Workspace struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
	// Envs, Secrets and Keys are what it holds, counted for the picker: an
	// application nobody has configured yet should say so before it is opened.
	Envs      int       `json:"envs"`
	Secrets   int       `json:"secrets"`
	Keys      int       `json:"keys"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

func (w Workspace) Validate() error {
	return validName(w.Name, "workspace", 40)
}

// An Env is a stage of one workspace — local, develop, staging, production, or
// whatever else that application needs.
type Env struct {
	ID          int64  `json:"id"`
	WorkspaceID int64  `json:"workspace_id"`
	Workspace   string `json:"workspace,omitempty"`
	Name        string `json:"name"`
	Note        string `json:"note,omitempty"`
	// Secrets is how many are in it. Cheap to count and the first thing
	// anybody wants to know about a group they have not opened.
	Secrets   int       `json:"secrets"`
	Keys      int       `json:"keys"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	// Revision is the newest change in the group, in nanoseconds. It is what
	// the vault answers as an ETag, so an application can ask "has anything
	// moved" every minute for the price of a 304 rather than a decrypt.
	Revision int64 `json:"revision,omitempty"`
}

// DefaultEnvs are the four stages every new workspace starts with. Seeded
// rather than left empty because "local, develop, staging, production" is what
// almost everybody was going to type, and adding an application should not be
// four more presses before it can hold anything.
var DefaultEnvs = []string{"local", "develop", "staging", "production"}

// DefaultWorkspace is where an installation's environments live until somebody
// names an application. It is also where the ones from before workspaces
// existed are put on upgrade, rather than being guessed at.
const DefaultWorkspace = "default"

func (e Env) Validate() error {
	if e.WorkspaceID == 0 {
		return errors.New("an environment belongs to one workspace")
	}
	return validName(e.Name, "environment", 40)
}

// validName holds workspace and environment names to what can appear in a
// token, a URL and a log line without quoting — they are identifiers people
// type, not prose.
func validName(name, kind string, limit int) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a %s needs a name", kind)
	}
	if len(name) > limit {
		return fmt.Errorf("a %s name must be %d characters or fewer", kind, limit)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return fmt.Errorf("a %s name is letters, digits, dashes, underscores and dots — %q is not", kind, name)
		}
	}
	return nil
}

// A Secret is one key and one value in one environment.
//
// The value is a plain string in both directions, unlike every other secret in
// guard. It is sealed in the database exactly the same way; what is different
// is that reading it back is the point of storing it, so there is no HasValue
// flag and no pointer dance — an endpoint that should not return a value
// simply does not ask for one.
type Secret struct {
	ID    int64  `json:"id"`
	EnvID int64  `json:"env_id"`
	Key   string `json:"key"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
	// Unreadable says the value did not decrypt: it was sealed with a key this
	// instance no longer has. Distinguished from an empty value because the
	// answer is different — "set it again", not "it is empty".
	Unreadable bool      `json:"unreadable,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

func (s Secret) Validate() error {
	if err := ValidateSecretKey(s.Key); err != nil {
		return err
	}
	if len(s.Value) > 1<<20 {
		return errors.New("a secret value must be a megabyte or less")
	}
	return nil
}

// ValidateSecretKey holds the names to what a shell and a process environment
// can actually carry. A key with a space or an equals sign in it is a key that
// works in guard and breaks in the container it was written for, which is a
// worse failure than being told now.
func ValidateSecretKey(key string) error {
	if key == "" {
		return errors.New("a secret needs a key")
	}
	if len(key) > 128 {
		return errors.New("a secret key must be 128 characters or fewer")
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("a secret key is letters, digits and underscores, and does not start with a digit — %q is not", key)
		}
	}
	return nil
}

// An APIKey is one application's way in: a token guard mints once, hashed here,
// scoped to exactly one environment.
//
// One key, one environment, because that is what makes revocation mean
// something. A key that could read three environments is a key nobody dares
// rotate when one application is redeployed, and "which of the seven services
// is still using it" is not a question a hash can answer.
type APIKey struct {
	ID      int64  `json:"id"`
	EnvID   int64  `json:"env_id"`
	EnvName string `json:"env_name,omitempty"`
	// Workspace is which application's environment this reads. Carried on the
	// key rather than looked up beside it, because a list of twenty tokens
	// where three are called "production" is a list nobody dares revoke from.
	Workspace string `json:"workspace,omitempty"`
	Name      string `json:"name"`
	// Prefix is the readable head of the token — enough to tell two keys apart
	// in a list and in a log line, and not enough to be one.
	Prefix string `json:"prefix"`
	// Token is the whole thing, and it is set exactly once: in the answer to
	// the request that created it. Nothing reads it back, because nothing can.
	Token     string    `json:"token,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// LastUsedAt is written by the vault, throttled. It is the whole reason the
	// read side of this feature writes anything at all: a key nobody has used
	// since March is a key that can be deleted, and without this the only
	// honest answer is "no idea".
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	RevokedAt  time.Time `json:"revoked_at,omitempty"`
}

func (k APIKey) Validate() error {
	if strings.TrimSpace(k.Name) == "" {
		return errors.New("a key needs a name — what is holding it")
	}
	if len(k.Name) > 60 {
		return errors.New("a key name must be 60 characters or fewer")
	}
	if k.EnvID == 0 {
		return errors.New("a key belongs to one environment")
	}
	return nil
}

// Live reports a key that may still be presented: not revoked, not expired.
func (k APIKey) Live(now time.Time) bool {
	if !k.RevokedAt.IsZero() {
		return false
	}
	return k.ExpiresAt.IsZero() || k.ExpiresAt.After(now)
}

// An Import is one paste of .env text against one environment.
//
// It is a first-class thing rather than a loop over the save endpoint because
// the interesting part is what it is about to do: twelve new, three changed,
// forty-one already the same. A bulk write that reports one number is a bulk
// write people do not press.
type Import struct {
	EnvID int64  `json:"env_id"`
	Text  string `json:"text"`
	// Prune deletes the keys the text does not mention, so an environment can
	// be made to match a file exactly. Off by default: the common paste is a
	// handful of new values, and a default that silently emptied a group would
	// be the last time anybody used this.
	Prune bool `json:"prune,omitempty"`
	// DryRun asks what would happen and changes nothing. The page always asks
	// this first, which is where the counts on the confirm come from.
	DryRun bool `json:"dry_run,omitempty"`
}

// An ImportResult is what a paste did, or would do.
type ImportResult struct {
	Added     []string `json:"added"`
	Changed   []string `json:"changed"`
	Unchanged []string `json:"unchanged"`
	Pruned    []string `json:"pruned"`
	// Skipped names the lines that could not be a secret — a key with a space
	// in it, a line that is not a pair at all — with the reason. Named rather
	// than counted, because "3 lines skipped" sends somebody back to a
	// hundred-line file to find out which.
	Skipped []ImportSkip `json:"skipped,omitempty"`
	DryRun  bool         `json:"dry_run,omitempty"`
}

type ImportSkip struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// ParseEnv reads .env text into pairs, in the order they appeared.
//
// Deliberately the dialect people actually paste rather than a strict one:
// `export` prefixes are dropped, `#` starts a comment outside quotes, values
// may be bare, single-quoted or double-quoted, and a double-quoted value may
// run over several lines — which is how a PEM private key ends up in a .env
// file and is exactly the case a line-at-a-time parser gets wrong.
//
// Escapes are honoured inside double quotes only (\n, \r, \t, \\, \", \'),
// because that is where a shell honours them; a single-quoted value is
// verbatim, backslashes and all, which is what makes it the safe way to paste
// a password.
func ParseEnv(text string) ([]Secret, []ImportSkip) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var pairs []Secret
	var skipped []ImportSkip
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		name, rest, found := strings.Cut(line, "=")
		if !found {
			skipped = append(skipped, ImportSkip{Line: i + 1, Text: trimForReport(raw), Reason: "not a KEY=value line"})
			continue
		}
		name = strings.TrimSpace(name)
		if err := ValidateSecretKey(name); err != nil {
			skipped = append(skipped, ImportSkip{Line: i + 1, Text: trimForReport(raw), Reason: err.Error()})
			continue
		}
		value, consumed, err := readValue(rest, lines[i+1:])
		if err != nil {
			skipped = append(skipped, ImportSkip{Line: i + 1, Text: trimForReport(raw), Reason: err.Error()})
			continue
		}
		i += consumed
		pairs = append(pairs, Secret{Key: name, Value: value})
	}
	return pairs, skipped
}

// readValue takes everything after the first `=` and, for a quoted value that
// does not close on its line, as many following lines as it needs. It answers
// how many extra lines it swallowed so the caller can skip them.
func readValue(rest string, following []string) (string, int, error) {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return "", 0, nil
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		// Bare: a comment may follow, and trailing whitespace is not part of
		// the value. Nothing else is interpreted — a bare value with a
		// backslash in it is a Windows path far more often than an escape.
		if hash := strings.Index(rest, " #"); hash >= 0 {
			rest = rest[:hash]
		}
		return strings.TrimRight(rest, " \t"), 0, nil
	}
	body := rest[1:]
	for used := 0; ; used++ {
		if value, ok := closeQuote(body, quote); ok {
			return value, used, nil
		}
		if used >= len(following) {
			return "", 0, fmt.Errorf("a %c-quoted value that never closes", quote)
		}
		body += "\n" + following[used]
	}
}

// closeQuote finds the closing quote, honouring backslash escapes inside a
// double-quoted value and nothing at all inside a single-quoted one.
func closeQuote(body string, quote byte) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote == '"' && c == '\\' && i+1 < len(body) {
			i++
			switch body[i] {
			case 'n':
				out.WriteByte('\n')
			case 'r':
				out.WriteByte('\r')
			case 't':
				out.WriteByte('\t')
			case '\\', '"', '\'':
				out.WriteByte(body[i])
			default:
				out.WriteByte('\\')
				out.WriteByte(body[i])
			}
			continue
		}
		if c == quote {
			return out.String(), true
		}
		out.WriteByte(c)
	}
	return "", false
}

func trimForReport(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		return line[:80] + "…"
	}
	return line
}

// FormatEnv writes pairs back out as .env text — the other half of a paste,
// and what `guard-vault fetch` prints.
//
// Everything is double-quoted rather than only the values that need it: a file
// where some lines are quoted and some are not invites somebody to conclude
// the quoting means something.
func FormatEnv(secrets []Secret) string {
	var out strings.Builder
	for _, secret := range secrets {
		out.WriteString(secret.Key)
		out.WriteString(`="`)
		out.WriteString(escapeEnv(secret.Value))
		out.WriteString("\"\n")
	}
	return out.String()
}

func escapeEnv(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return replacer.Replace(value)
}
