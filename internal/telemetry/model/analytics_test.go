package model

import (
	"strings"
	"testing"
)

func TestNormalisePathGroupsWhatIsOnePage(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/pricing", "/pricing"},
		{"/pricing?utm_source=news", "/pricing"}, // the visit, not the page
		{"/pricing#plans", "/pricing"},           // the same page, scrolled
		{"/pricing/?a=1#x", "/pricing"},          // both, and the slash
		{"/Pricing/Team", "/pricing/team"},       // a link typed in capitals
		{"/docs/", "/docs"},                      // the trailing slash
		{"/docs///", "/docs"},                    // however many of them
		{"/", "/"},                               // except the root, which is a page
		{"", "/"},                                // and a beacon that named nothing
		{"  /pricing  ", "/pricing"},             // whitespace somebody pasted
		{"pricing", "/pricing"},                  // a path is rooted
		{"?utm_source=news", "/"},                // a query alone is the root
	} {
		if got := NormalisePath(c.in); got != c.want {
			t.Errorf("NormalisePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalisePathTruncatesOnARune(t *testing.T) {
	long := "/" + strings.Repeat("é", MaxAnalyticsPath+40)
	got := NormalisePath(long)
	if n := len([]rune(got)); n != MaxAnalyticsPath {
		t.Fatalf("truncated to %d runes, want %d", n, MaxAnalyticsPath)
	}
	if !strings.HasSuffix(got, "é") {
		t.Fatal("a path cut on a byte renders as a replacement glyph")
	}
}

func TestValidActionName(t *testing.T) {
	for _, name := range []string{"page_view", "checkout.start", "docs-search", "a", "v2"} {
		if !ValidActionName(name) {
			t.Errorf("%q was refused", name)
		}
	}
	for _, name := range []string{
		"",             // no name at all
		"Signup_Click", // folding case would merge two teams' events
		"signup click", // a space is a column header nobody can filter on
		"signup/click",
		"signup:click",
		strings.Repeat("a", MaxActionName+1), // over the ceiling
	} {
		if ValidActionName(name) {
			t.Errorf("%q was accepted", name)
		}
	}
}

// beacon is the shape that must pass, so each rejection below differs from a
// good beacon in exactly one way.
func beacon() Beacon {
	return Beacon{
		Session:  "6f2a9c1d4e8b7a30",
		Path:     "/pricing",
		Source:   BeaconSource{Source: "google", Medium: "cpc", Campaign: "spring"},
		Referrer: "news.ycombinator.com",
		Events: []TrackEvent{
			{Name: "page_view", At: 1755900000123},
			{Name: "signup_click", At: 1755900004411, Props: map[string]string{"plan": "team"}},
		},
	}
}

func TestBeaconValidateAcceptsTheTrackersOwnPost(t *testing.T) {
	if err := beacon().Validate(); err != nil {
		t.Fatalf("the spec's own example was refused: %v", err)
	}
}

func TestBeaconValidateRefusesTheEdge(t *testing.T) {
	events := func(n int) []TrackEvent {
		out := make([]TrackEvent, n)
		for i := range out {
			out[i] = TrackEvent{Name: "page_view"}
		}
		return out
	}
	props := func(n int) map[string]string {
		out := make(map[string]string, n)
		for i := range n {
			out[string(rune('a'+i))] = "v"
		}
		return out
	}
	for _, c := range []struct {
		why   string
		alter func(*Beacon)
	}{
		{"no session id", func(b *Beacon) { b.Session = "" }},
		{"a session id over the ceiling", func(b *Beacon) { b.Session = strings.Repeat("a", MaxSessionID+1) }},
		{"a session id that would need escaping", func(b *Beacon) { b.Session = "6f2a/../30" }},
		{"an upper-case session id", func(b *Beacon) { b.Session = "6F2A9C1D4E8B7A30" }},
		{"no path", func(b *Beacon) { b.Path = "  " }},
		{"a path over the ceiling", func(b *Beacon) { b.Path = "/" + strings.Repeat("a", MaxAnalyticsPath) }},
		{"a referrer over the ceiling", func(b *Beacon) { b.Referrer = strings.Repeat("a", MaxPropValue+1) }},
		{"a campaign over the ceiling", func(b *Beacon) { b.Source.Campaign = strings.Repeat("a", MaxPropValue+1) }},
		{"no events", func(b *Beacon) { b.Events = nil }},
		{"more events than a batch may carry", func(b *Beacon) { b.Events = events(MaxBeaconEvents + 1) }},
		{"an action name outside the alphabet", func(b *Beacon) { b.Events[0].Name = "Signup Click" }},
		{"an action name over the ceiling", func(b *Beacon) { b.Events[0].Name = strings.Repeat("a", MaxActionName+1) }},
		{"more props than an event may carry", func(b *Beacon) { b.Events[1].Props = props(MaxEventProps + 1) }},
		{"a prop with no name", func(b *Beacon) { b.Events[1].Props = map[string]string{"": "team"} }},
		{"a prop name over the ceiling", func(b *Beacon) {
			b.Events[1].Props = map[string]string{strings.Repeat("k", MaxActionName+1): "team"}
		}},
		{"a prop value over the ceiling", func(b *Beacon) {
			b.Events[1].Props = map[string]string{"plan": strings.Repeat("a", MaxPropValue+1)}
		}},
	} {
		b := beacon()
		c.alter(&b)
		if err := b.Validate(); err == nil {
			t.Errorf("%s was accepted", c.why)
		}
	}
}

func TestBeaconValidateAcceptsTheLimitsThemselves(t *testing.T) {
	// The ceilings are inclusive: a batch of exactly fifty is a full flush from
	// the tracker guard ships, not an attack.
	b := beacon()
	b.Path = "/" + strings.Repeat("a", MaxAnalyticsPath-1)
	b.Events = make([]TrackEvent, MaxBeaconEvents)
	for i := range b.Events {
		b.Events[i] = TrackEvent{Name: strings.Repeat("a", MaxActionName)}
	}
	b.Events[0].Props = map[string]string{"plan": strings.Repeat("a", MaxPropValue)}
	if err := b.Validate(); err != nil {
		t.Fatalf("a beacon on every limit was refused: %v", err)
	}
}

func TestActionValidate(t *testing.T) {
	if err := (Action{Name: "signup_click", Pinned: true}).Validate(); err != nil {
		t.Fatalf("a discovered name was refused: %v", err)
	}
	if err := (Action{Name: "Signup Click"}).Validate(); err == nil {
		t.Error("a name no beacon could carry was accepted")
	}
	if err := (Action{Name: "signup_click", Position: -1}).Validate(); err == nil {
		t.Error("a negative column position was accepted")
	}
}

func TestPathRuleValidate(t *testing.T) {
	if err := (PathRule{Pattern: "/users/*", Replacement: "/users/:id"}).Validate(); err != nil {
		t.Fatalf("the spec's own rule was refused: %v", err)
	}
	for _, rule := range []PathRule{
		{Replacement: "/users/:id"},                     // nothing to match
		{Pattern: "/users/*"},                           // nothing to collapse to
		{Pattern: "users/*", Replacement: "/users/:id"}, // a rule matches a path
		{Pattern: "/users/*", Replacement: "users/:id"}, // and produces one
		{Pattern: "/users/*", Replacement: "/users/:id", Position: -1},
	} {
		if err := rule.Validate(); err == nil {
			t.Errorf("%+v was accepted", rule)
		}
	}
}
