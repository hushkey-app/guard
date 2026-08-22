package model

// What a person did in a browser: the beacon that says it, and the four nouns
// the page is drawn from.
//
// The organising idea is one sentence — the URL is the group, an action is a
// column — and every type here follows from it. A Beacon is one post from the
// tracker, deliberately short-keyed because it is JSON somebody's product
// serves to every visitor; a PathRow is one line of the grid, and an
// ActionCell is one square in it.
//
// Everything a browser sends is from a stranger, so the limits below are the
// edge of what guard will store, enforced before anything is written. They are
// refusals rather than clamps: a beacon quietly truncated is a tracker that
// looks like it is working and is not.

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The edge limits. A batch is bounded so one post cannot be a day's writes, a
// name is bounded so it can be a column header, and a value is bounded because
// props are labels rather than a place to put a document.
const (
	MaxBeaconEvents  = 50  // events in one post
	MaxActionName    = 64  // characters in an action name
	MaxEventProps    = 8   // props on one event
	MaxPropValue     = 200 // characters in a prop value
	MaxAnalyticsPath = 200 // characters in a path, after normalisation
	MaxSessionID     = 64  // characters in a session id
)

// A TrackEvent is one thing that happened: a name, when it happened in the
// browser's clock, and a flat bag of strings.
//
// Flat and string-only on purpose — Plausible's shape rather than GA4's. A
// nested value is a schema nobody agreed on, and a number here would be a
// number guard cannot say the units of.
type TrackEvent struct {
	Name  string            `json:"n"`
	At    int64             `json:"t,omitempty"` // milliseconds since the epoch, the browser's clock
	Props map[string]string `json:"d,omitempty"`
}

// A BeaconSource is where the session came from: the three UTM keys, read
// client-side before the query string is dropped.
//
// Three fields rather than a props system, because attribution is the one
// thing every analytics install wants and this is the cheapest version of it
// that is still worth reading.
type BeaconSource struct {
	Source   string `json:"s,omitempty"`
	Medium   string `json:"m,omitempty"`
	Campaign string `json:"c,omitempty"`
}

// A Beacon is one post from the tracker: the session, the page it is on, where
// it came from, and the events batched since the last flush.
//
// The keys are one letter because this payload crosses somebody else's
// visitors' networks and the tracker that writes it has a two-kilobyte budget.
type Beacon struct {
	Session  string       `json:"s"`
	Path     string       `json:"p"`
	Source   BeaconSource `json:"u,omitzero"`
	Referrer string       `json:"r,omitempty"` // the referrer host, never the full URL
	Events   []TrackEvent `json:"e"`
}

// Validate is the edge. It runs before a row is written and refuses the whole
// beacon rather than the offending event: a batch that arrives half-stored is a
// count nobody can reason about, and the tracker that sent it has a bug worth
// hearing about in full.
func (b Beacon) Validate() error {
	if err := validSessionID(b.Session); err != nil {
		return err
	}
	if strings.TrimSpace(b.Path) == "" {
		return errors.New("a beacon needs the path it happened on")
	}
	if len([]rune(b.Path)) > MaxAnalyticsPath {
		return fmt.Errorf("a path is at most %d characters", MaxAnalyticsPath)
	}
	if len([]rune(b.Referrer)) > MaxPropValue {
		return fmt.Errorf("a referrer host is at most %d characters", MaxPropValue)
	}
	for _, value := range []string{b.Source.Source, b.Source.Medium, b.Source.Campaign} {
		if len([]rune(value)) > MaxPropValue {
			return fmt.Errorf("a campaign field is at most %d characters", MaxPropValue)
		}
	}
	if len(b.Events) == 0 {
		return errors.New("a beacon with no events is a post nobody had to make")
	}
	if len(b.Events) > MaxBeaconEvents {
		return fmt.Errorf("at most %d events in one beacon", MaxBeaconEvents)
	}
	for _, event := range b.Events {
		if !ValidActionName(event.Name) {
			return fmt.Errorf("%q is not an action name: at most %d characters of [a-z0-9_.-]", event.Name, MaxActionName)
		}
		if len(event.Props) > MaxEventProps {
			return fmt.Errorf("at most %d props on one event", MaxEventProps)
		}
		for key, value := range event.Props {
			if key == "" || len([]rune(key)) > MaxActionName {
				return fmt.Errorf("a prop name is 1 to %d characters", MaxActionName)
			}
			if len([]rune(value)) > MaxPropValue {
				return fmt.Errorf("prop %q is over %d characters", key, MaxPropValue)
			}
		}
	}
	return nil
}

// validSessionID holds the session id to what can appear in a URL and a log
// line unquoted. It is the join key into the traces of the same visit, so a
// session id carrying anything a query string would have to escape is a link
// that breaks somewhere else.
func validSessionID(id string) error {
	if id == "" {
		return errors.New("a beacon needs a session id")
	}
	if len(id) > MaxSessionID {
		return fmt.Errorf("a session id is at most %d characters", MaxSessionID)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return errors.New("a session id is lowercase hexadecimal")
	}
	return nil
}

// ValidActionName says whether a name may be counted. Lowercase, and the three
// separators people actually type: `signup_click`, `checkout.start`,
// `docs-search`.
//
// One alphabet rather than a normalising pass, because `Signup_Click` and
// `signup_click` folded together would be two teams' events in one column and
// nobody could see it had happened.
func ValidActionName(name string) bool {
	if name == "" || len(name) > MaxActionName {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// NormalisePath is what makes a URL a group.
//
// The tracker does this before the beacon leaves, and guard does it again on
// arrival — the door is public, so what arrives is whatever somebody posted,
// and a path that skipped normalisation would be a second row for a page that
// already has one.
//
// Query and hash go because they are the visit rather than the page, the
// trailing slash goes because `/pricing/` and `/pricing` are one page, and the
// case goes because a link somebody typed in capitals is the same page too.
func NormalisePath(path string) string {
	path = strings.TrimSpace(path)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	path = strings.ToLower(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	if runes := []rune(path); len(runes) > MaxAnalyticsPath {
		// Cut on a rune, never a byte: half a character is a path that renders
		// as a replacement glyph on the page it is supposed to name.
		path = string(runes[:MaxAnalyticsPath])
	}
	return path
}

// An ActionCell is one square of the grid: how often an action happened on a
// path, and the number the column exists for — the sessions that did it over
// the sessions that saw the page.
//
// A path where an action was never seen carries no cell at all, so the
// renderer draws a dash. `0.0%` beside a column that page has no button for is
// a lie in a fixed-width font, and an empty window is silence rather than zero.
type ActionCell struct {
	Events   int64   `json:"events"`
	Sessions int64   `json:"sessions"`
	Rate     float64 `json:"rate"` // 0 to 1, of the sessions that saw the path
}

// A PathRow is one line of the grid: the page, what it was worth, and a cell
// per action that happened on it.
type PathRow struct {
	Path     string                `json:"path"`
	Views    int64                 `json:"views"`
	Sessions int64                 `json:"sessions"`
	Actions  map[string]ActionCell `json:"actions,omitempty"`
}

// An Action is a discovered name, and whether somebody decided it mattered.
//
// Discovery is the machine's half — names are never declared in advance,
// because a tool that wants registration before it records is a tool people
// abandon mid-instrumentation. Pinning is the person's half: a pinned action is
// a column, in a stored order, and the rest still count and still show when a
// path is opened.
type Action struct {
	Name      string    `json:"name"`
	Pinned    bool      `json:"pinned"`
	Position  int       `json:"position"`
	Events    int64     `json:"events,omitempty"`
	Sessions  int64     `json:"sessions,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitzero"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
}

func (a Action) Validate() error {
	if !ValidActionName(a.Name) {
		return fmt.Errorf("%q is not an action name: at most %d characters of [a-z0-9_.-]", a.Name, MaxActionName)
	}
	if a.Position < 0 {
		return errors.New("a position must not be negative")
	}
	return nil
}

// A PathRule collapses the dynamic segment of a URL into the page it is:
// `/users/*` → `/users/:id`. Ordered, and the first match wins.
//
// Applied at ingest, so a rule shapes what is stored rather than what is drawn.
// That is the honest half of a trade: changing a rule cannot rewrite the days
// already rolled up, and the page has to say so.
type PathRule struct {
	ID          int64  `json:"id"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Position    int    `json:"position"`
}

func (r PathRule) Validate() error {
	if strings.TrimSpace(r.Pattern) == "" {
		return errors.New("a path rule needs a pattern")
	}
	if strings.TrimSpace(r.Replacement) == "" {
		return errors.New("a path rule needs the path it collapses to")
	}
	if len([]rune(r.Pattern)) > MaxAnalyticsPath || len([]rune(r.Replacement)) > MaxAnalyticsPath {
		return fmt.Errorf("a path rule's halves are at most %d characters", MaxAnalyticsPath)
	}
	if !strings.HasPrefix(r.Pattern, "/") || !strings.HasPrefix(r.Replacement, "/") {
		return errors.New("a path rule matches a path, so both halves start with /")
	}
	if r.Position < 0 {
		return errors.New("a position must not be negative")
	}
	return nil
}

// AnalyticsHealth is the answer to "is the tracker working", and it is mostly
// what guard threw away.
//
// A tracker being silently dropped — a blocked script, a name past the cap, a
// batch refused at the edge — is the failure mode people take weeks to notice,
// so the counts of the refusals are as much of this as the counts of the
// events.
type AnalyticsHealth struct {
	Enabled        bool      `json:"enabled"` // an origin is allowed, so the door is open
	Rejected       int64     `json:"rejected"`
	ActionsRefused int64     `json:"actions_refused"` // names past the discovery cap
	PathsCapped    int64     `json:"paths_capped"`    // paths rolled into (other)
	Actions        int       `json:"actions"`         // distinct names known
	SeenRows       int64     `json:"seen_rows"`       // the table that grows with traffic
	LastEvent      time.Time `json:"last_event,omitzero"`
}
