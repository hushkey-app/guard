// Package release watches for a newer guard and lets somebody ask for it.
//
// It does not install anything, and that is the design rather than an omission.
// The process holding every application's secrets should not also be the one
// that can replace binaries on the box — so guard's whole part is to notice
// that a release exists and to write down which version is wanted.
// `deploy/guard-update`, a root-owned unit on a timer, reads that file and does
// the work. It keeps working on the day guard will not start, which is the day
// somebody most wants to change the version.
//
// The polling is here rather than in the browser because the answer is the same
// for every open tab, GitHub's unauthenticated API allows 60 requests an hour
// per address, and a dashboard people leave open in four tabs would spend that
// in an afternoon.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hushkey-app/guard/internal/build"
)

// DefaultRepo is where guard looks for itself.
const DefaultRepo = "hushkey-app/guard"

// DefaultInterval is how often guard asks GitHub. Four requests an hour against
// an unauthenticated budget of sixty per address, shared with the updater on the
// same box — a number tuned to somebody else's rate limit is not a preference.
const DefaultInterval = 15 * time.Minute

// DefaultStatePath is the file the updater reads. Writing it is the only side
// effect this package has.
const DefaultStatePath = "/etc/guard/version"

// State is what the sidebar draws.
type State struct {
	// Current is what this binary is, from the version stamped at build time.
	Current string `json:"current"`
	// Latest is the newest release GitHub reports, empty until the first check
	// answers. Empty and Error empty together means "not checked yet", which
	// the page shows as nothing at all rather than as "up to date" — a claim
	// guard has not earned that early.
	Latest string `json:"latest,omitempty"`
	// Available says the two differ. Deliberately difference rather than
	// ordering: GitHub's "latest release" is the most recently published one,
	// not the highest version, so republishing an older tag is how a fleet is
	// rolled back and this must not argue with that.
	//
	// Never true for a development build. Difference is the right test between
	// two releases and the wrong one against a working tree, which differs from
	// every release by construction — so the card would otherwise sit in the
	// sidebar of every checkout offering to "update" a binary that is ahead of
	// the release it names.
	Available bool `json:"available"`
	// Development says this binary is not a published release: a commit past a
	// tag, a dirty tree, or nothing stamped. The sidebar stays quiet; the page
	// can still say what is running, which is the version somebody wants to see
	// while developing.
	Development bool   `json:"development"`
	URL         string `json:"url,omitempty"`
	Notes       string `json:"notes,omitempty"`
	// Wanted is what /etc/guard/version asks for, if anything. Once it names
	// the new release the button has done its job and the page says so — the
	// installing happens in the root-owned updater, started by a path unit.
	Wanted string `json:"wanted,omitempty"`
	// Managed says guard can write that file: a box set up with the units.
	// Elsewhere — a container, a laptop, `go run .` — the page shows that a
	// release exists and offers a link rather than a button that cannot work.
	Managed   bool      `json:"managed"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
	// Error is the last failed check, kept so the page can say "could not
	// reach GitHub" rather than quietly implying there is nothing new.
	Error string `json:"error,omitempty"`
}

// Watch polls for releases and holds the answer.
type Watch struct {
	// Repo is owner/name. Empty disables the whole thing, which is what an
	// instance with no outbound internet should do.
	Repo string
	// Current is this build's version, normally build.Version.
	Current string
	// Interval defaults to fifteen minutes: four requests an hour against a
	// sixty-an-hour budget, leaving room for the updater on the same address.
	Interval time.Duration
	// StatePath is the file the updater reads.
	StatePath string
	// Client is for tests. Nil means a plain client with a short timeout.
	Client *http.Client

	mu    sync.RWMutex
	state State
}

// Enabled reports whether there is anything to poll.
// A repository is the whole of it. Interval used to be part of this test, which
// made the feature dead in production for a subtle reason: main.go constructs
// the watcher without one, so Enabled was false, Run returned before its first
// tick, and no instance ever polled — while every test set an interval and so
// every test passed. Zero means "the default", exactly as the field's own
// comment says, and Run is where that default belongs.
func (w *Watch) Enabled() bool { return strings.TrimSpace(w.Repo) != "" }

// State returns the last answer, plus what the version file currently says.
//
// The file is read here rather than remembered, because the updater and a
// person with an editor both write it, and a cached copy would disagree with
// the box within a minute of anybody doing either.
func (w *Watch) State() State {
	w.mu.RLock()
	state := w.state
	w.mu.RUnlock()
	state.Current = w.Current
	state.Managed = w.manageable()
	state.Wanted = w.wanted()
	state.Development = build.IsDevelopment(state.Current)
	// Decided here rather than at check time, because whether this binary is a
	// release does not depend on what GitHub last answered.
	if state.Development {
		state.Available = false
	}
	return state
}

// Run polls until the context ends, checking once immediately so the first page
// load after a restart is right rather than empty for fifteen minutes.
func (w *Watch) Run(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	w.Check(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Check(ctx)
		}
	}
}

// MinimumGap is how close together two *asked-for* checks may be.
//
// GitHub allows an unauthenticated address sixty requests an hour and guard's
// own timer already spends four of them. A button anybody may press is a button
// somebody holds down, and spending the budget would take the sidebar's answer
// down with it for the rest of the hour — so a press inside the gap returns the
// answer that is already there rather than asking again. Ten seconds is long
// enough that a person leaning on it cannot exhaust anything and short enough
// that "check again" after fixing the network feels immediate.
const MinimumGap = 10 * time.Second

// CheckNow is the pressed check: the same request the timer makes, on demand,
// with a floor under how often it may actually leave the box.
//
// It reports whether it asked. The page says "checked just now" either way,
// because from the reader's side a fresh answer and a ten-second-old one are
// the same answer — but a caller that wants to know can tell.
func (w *Watch) CheckNow(ctx context.Context) (State, bool) {
	if !w.Enabled() {
		return w.State(), false
	}
	w.mu.RLock()
	last := w.state.CheckedAt
	w.mu.RUnlock()
	if !last.IsZero() && time.Since(last) < MinimumGap {
		return w.State(), false
	}
	w.Check(ctx)
	return w.State(), true
}

// Check asks GitHub once. A failure is recorded and the last good answer is
// kept: a dashboard that forgot there was an update because one request timed
// out is a dashboard that tells a different story every quarter of an hour.
func (w *Watch) Check(ctx context.Context) {
	if !w.Enabled() {
		return
	}
	latest, url, notes, err := w.fetch(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.CheckedAt = time.Now().UTC()
	if err != nil {
		w.state.Error = err.Error()
		slog.Debug("release check failed", slog.Any("err", err))
		return
	}
	w.state.Error = ""
	w.state.Latest = latest
	w.state.URL = url
	w.state.Notes = notes
	w.state.Available = latest != "" && latest != w.Current
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (w *Watch) fetch(ctx context.Context) (tag, url, notes string, err error) {
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	endpoint := "https://api.github.com/repos/" + strings.Trim(w.Repo, "/") + "/releases/latest"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", "", "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	// Named, because an API that rate-limits by address is one where being
	// identifiable is in everybody's interest.
	request.Header.Set("User-Agent", "guard/"+w.Current)
	response, err := client.Do(request)
	if err != nil {
		return "", "", "", err
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusNotFound:
		// No releases published yet. Not an error worth showing anybody.
		return "", "", "", nil
	case response.StatusCode == http.StatusForbidden:
		return "", "", "", errors.New("github is rate-limiting this address")
	case response.StatusCode != http.StatusOK:
		return "", "", "", fmt.Errorf("github answered %s", response.Status)
	}
	var body githubRelease
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return "", "", "", err
	}
	// Draft and pre-release ones are skipped by this endpoint already; the
	// check costs nothing and means a change at the far end cannot start
	// pushing release candidates at a fleet.
	if body.Draft || body.Prerelease {
		return "", "", "", nil
	}
	return strings.TrimSpace(body.TagName), body.HTMLURL, firstLines(body.Body), nil
}

// firstLines keeps the top of the release notes — enough for the sidebar to say
// what the release is, without pasting a page of generated changelog into it.
func firstLines(notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return ""
	}
	if len(notes) > 400 {
		notes = notes[:400] + "…"
	}
	return notes
}

// Request writes down which version this box should be on.
//
// It refuses anything but the release guard has actually seen, or "latest".
// The file is read by a root-owned script that puts the value in a URL, so the
// set of things it may contain is exactly the set guard has been told exists —
// not "a string that looks like a version", which is a validator somebody
// eventually widens.
func (w *Watch) Request(version string) (State, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return w.State(), errors.New("name a version")
	}
	state := w.State()
	if version != "latest" && version != state.Latest {
		return state, fmt.Errorf("%q is not the release this instance knows about", version)
	}
	if !state.Managed {
		return state, errors.New("this instance is not set up to be updated from the dashboard — there is no " + w.statePath())
	}
	path := w.statePath()
	if err := os.WriteFile(path, []byte(version+"\n"), 0o644); err != nil {
		return state, fmt.Errorf("could not write %s: %w", path, err)
	}
	slog.Info("update requested", slog.String("version", version), slog.String("path", path))
	return w.State(), nil
}

func (w *Watch) statePath() string {
	if strings.TrimSpace(w.StatePath) == "" {
		return DefaultStatePath
	}
	return w.StatePath
}

// manageable reports a box where the version file can be written: the directory
// exists. A laptop and a container do not have one, and there the page offers a
// link to the release instead of a button that would fail.
func (w *Watch) manageable() bool {
	info, err := os.Stat(filepath.Dir(w.statePath()))
	return err == nil && info.IsDir()
}

func (w *Watch) wanted() string {
	raw, err := os.ReadFile(w.statePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
