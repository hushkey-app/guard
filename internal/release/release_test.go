package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func watching(t *testing.T, current string, handler http.HandlerFunc) (*Watch, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	return &Watch{
		Repo:      "hushkey-app/guard",
		Current:   current,
		Interval:  time.Minute,
		StatePath: filepath.Join(dir, "version"),
		// Point the client at the test server whatever URL is asked for.
		Client: &http.Client{Transport: rewrite{server.URL}},
	}, dir
}

// rewrite sends every request to the test server, so the code under test can
// keep building the real GitHub URL.
type rewrite struct{ base string }

func (r rewrite) RoundTrip(request *http.Request) (*http.Response, error) {
	target, err := http.NewRequest(request.Method, r.base+request.URL.Path, nil)
	if err != nil {
		return nil, err
	}
	target.Header = request.Header
	return http.DefaultTransport.RoundTrip(target)
}

func release(tag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			// GitHub refuses a request without one, so a change that dropped
			// the header would fail in production and pass in a test that did
			// not check.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example/r/` + tag + `","body":"notes"}`)) //nolint:errcheck
	}
}

func TestAnAvailableReleaseIsTheOneThatDiffers(t *testing.T) {
	watch, _ := watching(t, "v0.2.0", release("v0.3.0"))
	watch.Check(context.Background())

	state := watch.State()
	if !state.Available || state.Latest != "v0.3.0" || state.Current != "v0.2.0" {
		t.Fatalf("state is %+v", state)
	}
	if state.URL == "" || state.Notes == "" {
		t.Fatalf("nothing to show: %+v", state)
	}

	// Same version: nothing to offer.
	same, _ := watching(t, "v0.3.0", release("v0.3.0"))
	same.Check(context.Background())
	if same.State().Available {
		t.Fatalf("offered an update to the version already running: %+v", same.State())
	}
}

// GitHub's "latest" is the most recently published release, not the highest
// version, and republishing an older tag is how a fleet is rolled back. This
// must follow that rather than argue with it.
func TestAnOlderLatestIsStillOffered(t *testing.T) {
	watch, _ := watching(t, "v0.9.0", release("v0.4.0"))
	watch.Check(context.Background())
	if state := watch.State(); !state.Available || state.Latest != "v0.4.0" {
		t.Fatalf("state is %+v", state)
	}
}

func TestAFailedCheckKeepsTheLastAnswer(t *testing.T) {
	fail := false
	watch, _ := watching(t, "v0.2.0", func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		release("v0.3.0")(w, r)
	})
	watch.Check(context.Background())
	fail = true
	watch.Check(context.Background())

	state := watch.State()
	// Still knows about the release, and says the check failed rather than
	// implying there is nothing new.
	if !state.Available || state.Latest != "v0.3.0" {
		t.Fatalf("forgot the release on one failure: %+v", state)
	}
	if state.Error == "" {
		t.Fatal("a failed check was not reported")
	}
}

func TestNoReleasesYetIsNotAnError(t *testing.T) {
	watch, _ := watching(t, "v0.2.0", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	watch.Check(context.Background())
	state := watch.State()
	if state.Available || state.Latest != "" || state.Error != "" {
		t.Fatalf("a repo with no releases produced %+v", state)
	}
}

func TestPrereleasesAreNotOffered(t *testing.T) {
	watch, _ := watching(t, "v0.2.0", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.3.0-rc1","prerelease":true}`)) //nolint:errcheck
	})
	watch.Check(context.Background())
	if state := watch.State(); state.Available {
		t.Fatalf("offered a pre-release: %+v", state)
	}
}

// The file is read by a root-owned script that puts the value in a URL, so what
// may be written to it is exactly what guard has been told exists.
func TestOnlyTheKnownReleaseMayBeRequested(t *testing.T) {
	watch, dir := watching(t, "v0.2.0", release("v0.3.0"))
	watch.Check(context.Background())

	for _, bad := range []string{"", "v9.9.9", "../../etc/passwd", "latest; rm -rf /", "v0.3.0 && curl evil"} {
		if _, err := watch.Request(bad); err == nil {
			t.Fatalf("wrote %q to the version file", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "version")); err == nil {
		t.Fatal("a refused request still created the file")
	}

	state, err := watch.Request("v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if state.Wanted != "v0.3.0" {
		t.Fatalf("after requesting: %+v", state)
	}
	written, err := os.ReadFile(filepath.Join(dir, "version"))
	if err != nil || string(written) != "v0.3.0\n" {
		t.Fatalf("the file holds %q (%v)", written, err)
	}

	// "latest" is the other thing the updater understands.
	if _, err := watch.Request("latest"); err != nil {
		t.Fatal(err)
	}
}

// A laptop, a container, `go run .` — no /etc/guard, so no button.
func TestAnUnmanagedInstanceOffersNoButton(t *testing.T) {
	watch, _ := watching(t, "v0.2.0", release("v0.3.0"))
	watch.StatePath = filepath.Join(t.TempDir(), "nowhere", "version")
	watch.Check(context.Background())

	state := watch.State()
	if state.Managed {
		t.Fatalf("claimed it could be updated: %+v", state)
	}
	// It still says a release exists — the page links to it.
	if !state.Available || state.URL == "" {
		t.Fatalf("hid the release entirely: %+v", state)
	}
	if _, err := watch.Request("v0.3.0"); err == nil {
		t.Fatal("wrote a version file into a directory that does not exist")
	}
}

func TestDisabledWithoutARepo(t *testing.T) {
	watch := &Watch{Current: "v0.2.0", Interval: time.Minute}
	if watch.Enabled() {
		t.Fatal("enabled with no repository")
	}
	// Run returns rather than spinning, and Check does nothing.
	watch.Run(context.Background())
	watch.Check(context.Background())
	if state := watch.State(); state.Available || state.Latest != "" {
		t.Fatalf("a disabled watch produced %+v", state)
	}
}

// The pressed check leaves the box, so it needs a floor under it. GitHub allows
// an unauthenticated address sixty requests an hour and guard's own timer
// already spends four of them; a button anybody may press is a button somebody
// holds down, and spending the budget would take the sidebar's answer down with
// it for the rest of the hour.
func TestAPressedCheckIsThrottled(t *testing.T) {
	var calls atomic.Int64
	watch, _ := watching(t, "v1.0.0", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		release("v9.9.9")(w, r)
	})

	state, asked := watch.CheckNow(context.Background())
	if !asked || state.Latest != "v9.9.9" || !state.Available {
		t.Fatalf("first press: asked=%v state=%+v", asked, state)
	}
	for range 20 {
		if _, asked := watch.CheckNow(context.Background()); asked {
			t.Fatal("a press inside the gap left the box")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d requests for 21 presses", got)
	}
	// Throttled is "the answer you already have", never an empty one — the page
	// must not blank out because somebody pressed twice.
	if state := watch.State(); state.Latest != "v9.9.9" || !state.Available {
		t.Fatalf("the throttled press lost the answer: %+v", state)
	}
}

// A watcher with no repository is an instance with no outbound internet, which
// is a supported way to run guard rather than a broken one.
func TestAPressWithNothingToPollDoesNothing(t *testing.T) {
	watch := &Watch{Current: "v1.0.0"}
	if _, asked := watch.CheckNow(context.Background()); asked {
		t.Fatal("a disabled watcher asked something")
	}
}

// The watcher main.go actually builds — a repository and a version, no interval
// — has to poll.
//
// It did not, and nothing caught it: Enabled required an interval, main.go
// never passed one, and every test in this file did. So the sidebar's update
// card could not appear on any real instance, and the suite was green. The
// shape of the constructor is the test.
func TestTheWatcherMainBuildsIsEnabled(t *testing.T) {
	watch := &Watch{Repo: "hushkey-app/guard", Current: "v0.2.0"}
	if !watch.Enabled() {
		t.Fatal("the watcher main.go builds is disabled, so nothing ever polls")
	}

	// And it really asks, rather than merely reporting itself enabled.
	var calls atomic.Int64
	live, _ := watching(t, "v0.2.0", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		release("v9.9.9")(w, r)
	})
	live.Interval = 0
	live.Check(context.Background())
	if calls.Load() != 1 || live.State().Latest != "v9.9.9" {
		t.Fatalf("%d requests, state %+v", calls.Load(), live.State())
	}
}
