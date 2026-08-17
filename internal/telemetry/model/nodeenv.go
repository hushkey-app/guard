package model

// A machine's environment: the variables guard keeps for one box, and how they
// are written out.
//
// ParseEnv beside this reads the paste; this renders it back. Both directions
// exist because the dashboard is one box per machine — somebody pastes a block of
// KEY=value lines, guard stores the pairs, and the same pairs are rendered into
// the machine's environment when they are injected.

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// bare is a value that needs no quoting at all: what a person would have typed
// unquoted, and what keeps the written file readable.
var bare = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// EnvQuote renders one value for an env file.
//
// Three forms, chosen for the smallest that is still correct:
//
//   - bare, when the value has nothing a shell would look at twice;
//   - single-quoted, which is verbatim — the safe form for a password, because
//     nothing inside it is interpreted;
//   - double-quoted with escapes, when the value contains a single quote or a
//     line break. A newline is written as \n rather than left literal: the parser
//     here reads both, but a literal one is a second variable as far as
//     /etc/environment and systemd are concerned, and those are what read what
//     guard writes.
func EnvQuote(value string) string {
	if value == "" {
		return ""
	}
	if bare.MatchString(value) {
		return value
	}
	if !strings.ContainsAny(value, "'\n\r") {
		return "'" + value + "'"
	}
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}

// A NodeEnvVar is one variable guard keeps for one machine.
//
// Stored, rather than read off the box: this is the list somebody edits, and
// injecting it is what puts it on the machine. The value is sealed at rest with
// the same keeper the SSH passwords use, because a machine's environment is where
// its database password is.
type NodeEnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// RenderEnvVars is a set of variables as env-file text, one per line, in order —
// what the textarea shows and what gets written.
func RenderEnvVars(vars []NodeEnvVar) string {
	var out strings.Builder
	for _, entry := range vars {
		out.WriteString(entry.Key + "=" + EnvQuote(entry.Value) + "\n")
	}
	return out.String()
}

// ParseEnvVars reads the pasted block into variables, with the lines it could not
// read named. The dialect is ParseEnv's — `export` prefixes, comments, quoted
// values, and a quoted value running over several lines.
func ParseEnvVars(text string) ([]NodeEnvVar, []ImportSkip) {
	pairs, skipped := ParseEnv(text)
	vars := make([]NodeEnvVar, 0, len(pairs))
	for _, pair := range pairs {
		vars = append(vars, NodeEnvVar{Key: pair.Key, Value: pair.Value})
	}
	return vars, skipped
}

// NodeEnvState is what the machine list says about an environment without
// carrying it: how many variables, and the two dates that tell somebody whether
// what is stored is what the box has.
type NodeEnvState struct {
	Count int `json:"count"`
	// SavedAt is when the list was last changed in guard, InjectedAt when it was
	// last written to the machine. Injected before saved means the box is behind,
	// which is the one thing this pair exists to say.
	SavedAt    time.Time `json:"saved_at,omitempty"`
	InjectedAt time.Time `json:"injected_at,omitempty"`
}

// Pending reports that the machine has not been given what is stored.
func (s NodeEnvState) Pending() bool {
	if s.Count == 0 {
		return false
	}
	return s.InjectedAt.IsZero() || s.InjectedAt.Before(s.SavedAt)
}

// ValidateEnvVars checks a whole set before any of it is stored: real keys, no
// duplicates, and nothing that would become a second variable when written out.
func ValidateEnvVars(vars []NodeEnvVar) error {
	seen := map[string]bool{}
	for _, entry := range vars {
		key := strings.TrimSpace(entry.Key)
		if err := ValidateSecretKey(key); err != nil {
			return err
		}
		if seen[key] {
			return errors.New(key + " is set twice — the last would win and the first would be a line nobody can explain")
		}
		seen[key] = true
	}
	return nil
}
