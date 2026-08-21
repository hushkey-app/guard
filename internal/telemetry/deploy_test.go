package telemetry

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

func template(t *testing.T, store *Store) model.DeployTemplate {
	t.Helper()
	saved, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name:        "pack",
		ServiceName: "app",
		Image:       "sjc.vultrcr.com/pack/app",
		Path:        "/srv/pack",
		ComposeYAML: "services:\n  app:\n    image: pack:${TAG}\n",
		HealthPath:  "/health",
		Vars:        []model.TemplateVar{{Key: "LOG_LEVEL", Source: model.VarStatic, Value: "info"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestAGroupFollowsTheCluster(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	group, err := store.SaveDeployGroup(model.DeployGroup{Name: "pack", NodeIDs: []int64{node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Nodes) != 1 || group.Nodes[0].Name != "VPS-1" {
		t.Fatalf("membership is %+v", group.Nodes)
	}
	if !group.Nodes[0].HasPassword {
		t.Fatal("a machine with a stored login should say so, because that is one of the two reasons a deploy refuses")
	}
	// The group holds ids and the cluster holds machines, so deleting the
	// machine empties the group by itself. There is no second list to go stale.
	if err := store.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	group, err = store.DeployGroup(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(group.Nodes) != 0 {
		t.Fatalf("a deleted machine is still in the group: %+v", group.Nodes)
	}
}

func TestAGroupWillNotHoldAMachineThatIsNotThere(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	if _, err := store.SaveDeployGroup(model.DeployGroup{Name: "pack", NodeIDs: []int64{404}}); err == nil {
		t.Fatal("a group naming a machine that does not exist should be refused at the save, not at the deploy")
	}
}

func TestSavingATemplateWritesTheNextVersion(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	first := template(t, store)
	if first.Version != 1 {
		t.Fatalf("first version is %d", first.Version)
	}

	edited := first
	edited.ComposeYAML = "services:\n  app:\n    image: pack:${TAG}\n    restart: always\n"
	second, err := store.SaveDeployTemplate(edited)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || second.ID != first.ID {
		t.Fatalf("second save is %d/%d", second.ID, second.Version)
	}
	// The whole point: what a past run points at still says what was deployed.
	back, err := store.DeployTemplate(first.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(back.ComposeYAML, "restart: always") {
		t.Fatal("editing a template rewrote the version a past run refers to")
	}
	// And version zero is the newest, which is what a page asks for.
	latest, err := store.DeployTemplate(first.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Version != 2 {
		t.Fatalf("newest is %d", latest.Version)
	}
	if len(latest.Versions) != 2 {
		t.Fatalf("history is %+v", latest.Versions)
	}
}

func TestATemplateWillNotWriteOutsideItsDirectory(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	for _, path := range []string{"srv/pack", "/srv/../etc", "/etc", "/srv/pack; rm -rf /", "/srv/$(whoami)"} {
		_, err := store.SaveDeployTemplate(model.DeployTemplate{
			Name: "pack", ServiceName: "app", Image: "r/app", Path: path, ComposeYAML: "services: {}",
		})
		if err == nil {
			t.Fatalf("%q was accepted as a directory to write into", path)
		}
	}
}

func TestATemplateMayNotDeclareTAG(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	_, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name: "pack", ServiceName: "app", Image: "r/app", Path: "/srv/pack", ComposeYAML: "services: {}",
		Vars: []model.TemplateVar{{Key: "TAG", Source: model.VarStatic, Value: "latest"}},
	})
	if err == nil {
		t.Fatal("TAG is the deploy's own variable; a template that could set it could deploy something other than the tag that was chosen")
	}
}

func TestVaultVariablesAreResolvedAtDeployTime(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	space, err := store.SaveWorkspace(model.Workspace{Name: "pack"})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := store.Envs(space.ID)
	if err != nil || len(envs) == 0 {
		t.Fatalf("envs %+v %v", envs, err)
	}
	env := envs[0]
	if _, err := store.SaveSecret(model.Secret{EnvID: env.ID, Key: "DATABASE_URL", Value: "postgres://real"}); err != nil {
		t.Fatal(err)
	}

	saved, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name: "pack", ServiceName: "app", Image: "r/app", Path: "/srv/pack", ComposeYAML: "services: {}",
		SecretEnvID: env.ID,
		Vars: []model.TemplateVar{
			{Key: "LOG_LEVEL", Source: model.VarStatic, Value: "info"},
			// A value posted alongside a vault source is dropped: the one thing
			// this design does not do is keep a second copy.
			{Key: "DATABASE_URL", Source: model.VarVault, Value: "postgres://leaked"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, declared := range saved.Vars {
		if declared.Source == model.VarVault && declared.Value != "" {
			t.Fatal("a vault variable stored a value beside the reference")
		}
	}

	resolved, err := store.ResolveDeployVars(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 2 || resolved[1].Value != "postgres://real" {
		t.Fatalf("resolved %+v", resolved)
	}

	// A key that is not there stops the deploy rather than writing an empty
	// line: an application booting with DATABASE_URL set to nothing fails in a
	// way that takes an hour to trace back to here.
	saved.Vars = append(saved.Vars, model.TemplateVar{Key: "REDIS_URL", Source: model.VarVault})
	missing, err := store.SaveDeployTemplate(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveDeployVars(missing); err == nil {
		t.Fatal("a missing vault key should refuse the deploy")
	}
}

func TestALockedMachineRefusesADeploy(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	if _, err := store.DeployTarget(node.ID); err != nil {
		t.Fatalf("an unlocked machine should be deployable: %v", err)
	}
	node.Locked = true
	if _, err := store.SaveNode(node); err != nil {
		t.Fatal(err)
	}
	// A deploy writes files and runs docker. It is the command line wearing a
	// template's name, so the lock has to reach it.
	if _, err := store.DeployTarget(node.ID); !errors.Is(err, ErrDeployLocked) {
		t.Fatalf("a locked machine accepted a deploy: %v", err)
	}
}

func TestWhatAMachineIsRunningMovesOnlyOnHealthy(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	state, err := store.DeployStateFor(node.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentTag != "" {
		t.Fatal("a machine that has never had a healthy deploy should claim nothing")
	}
	if err := store.RecordDeployHealthy(node.ID, "app", "v1", 1, 1); err != nil {
		t.Fatal(err)
	}
	// Nothing to go back to yet: one good deploy is not a rollback target for
	// itself.
	state, err = store.DeployStateFor(node.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentTag != "v1" || state.CanRollBack() {
		t.Fatalf("after one deploy the state is %+v", state)
	}
	if err := store.RecordDeployHealthy(node.ID, "app", "v2", 1, 1); err != nil {
		t.Fatal(err)
	}
	state, err = store.DeployStateFor(node.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	// The one before this, which is the only thing worth calling a rollback
	// target. A tag that failed its gate never reaches here at all.
	if state.CurrentTag != "v2" || state.LastGoodTag != "v1" || !state.CanRollBack() {
		t.Fatalf("state is %+v", state)
	}
}

func TestRollingBackFollowsTheComposeFileToo(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)

	// The same image tag, a new compose file. This is the ordinary case for
	// anybody deploying `latest` or a fixed base image and editing the template
	// — and the first version of this recorded only the tag, so "the version
	// before" was unknowable and the button never appeared.
	if err := store.RecordDeployHealthy(node.ID, "app", "alpine", 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeployHealthy(node.ID, "app", "alpine", 1, 6); err != nil {
		t.Fatal(err)
	}
	state, err := store.DeployStateFor(node.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentVersion != 6 || state.LastGoodVersion != 5 {
		t.Fatalf("state is %+v", state)
	}
	if !state.CanRollBack() {
		t.Fatal("a new compose file with the same tag should still be rollback-able")
	}

	// Deploying the identical thing again is not a step, so it must not push
	// the real previous version out of reach.
	if err := store.RecordDeployHealthy(node.ID, "app", "alpine", 1, 6); err != nil {
		t.Fatal(err)
	}
	state, err = store.DeployStateFor(node.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if state.LastGoodVersion != 5 {
		t.Fatalf("redeploying the same version lost the one before it: %+v", state)
	}
}

func TestARestartLeavesInterruptedRatherThanResuming(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	one := machine(t, store)
	two, err := store.SaveNode(Node{Name: "VPS-2", URL: "http://localhost:8001", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeployHealthy(one.ID, "app", "v1", 1, 1); err != nil {
		t.Fatal(err)
	}

	run, err := store.CreateDeployRun(model.DeployRun{
		GroupName: "pack", ServiceName: "app", Tag: "v2", Mode: model.ModeSequential,
		Instances: []model.DeployInstance{
			{NodeID: one.ID, NodeName: one.Name, PreviousTag: "v1"},
			{NodeID: two.ID, NodeName: two.Name},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := run.Instances[0]
	first.Status = model.InstanceHealthCheck
	if err := store.SetDeployInstance(first); err != nil {
		t.Fatal(err)
	}

	touched, err := store.SweepDeployRuns()
	if err != nil {
		t.Fatal(err)
	}
	if touched != 1 {
		t.Fatalf("swept %d", touched)
	}
	after, err := store.DeployRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.RunInterrupted {
		t.Fatalf("run is %q", after.Status)
	}
	if after.Instances[0].Status != model.InstanceInterrupted {
		t.Fatalf("the machine being deployed to is %q", after.Instances[0].Status)
	}
	// The machine it never reached was not failed: nothing was done to it, and
	// a row saying "failed" about an untouched box is what gets somebody to
	// reboot a healthy machine at 3am.
	if after.Instances[1].Status != model.InstanceSkipped {
		t.Fatalf("the machine it never reached is %q", after.Instances[1].Status)
	}
	// And nothing was resumed or assumed: the machine still claims the tag that
	// actually answered.
	state, err := store.DeployStateFor(one.ID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentTag != "v1" {
		t.Fatalf("an interrupted deploy moved what the machine claims to be running: %+v", state)
	}
}

func TestDeploysAreInTheBackupAndTheirHistoryIsNot(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	summary, err := store.BackupSummary()
	if err != nil {
		t.Fatal(err)
	}
	carried := map[string]bool{}
	for _, table := range summary.Tables {
		carried[table.Name] = true
	}
	// The configuration travels — a replacement machine is provisioned by
	// deploying to it, which needs the compose file to have come with the
	// backup.
	for _, want := range []string{"deploy_groups", "deploy_group_nodes", "deploy_templates", "deploy_state"} {
		if !carried[want] {
			t.Fatalf("%s is not in the backup", want)
		}
	}
	// The history does not. It is what happened on the instance that is gone.
	for _, table := range summary.Tables {
		if strings.HasPrefix(table.Name, "deploy_run") {
			t.Fatalf("%s should not travel", table.Name)
		}
	}
}

func TestATemplateIsThreeAnswersRatherThanEight(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	// A name, a compose file, and where to check. The service name, the image
	// and the directory are derived — every one of them is a field that could
	// otherwise be typed wrong in a way nothing checks.
	saved, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name:        "Pack Web",
		HealthPath:  "/health",
		ComposeYAML: "services:\n  web:\n    image: sjc.vultrcr.com/pack/app:${TAG}\n    ports: [\"80:80\"]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Path != "/guard/pack-web" {
		t.Fatalf("writes to %q", saved.Path)
	}
	if saved.ServiceName != "pack-web" {
		t.Fatalf("service is %q", saved.ServiceName)
	}
	// The image is the one the compose file tags. A separate field could name a
	// different repository to the one that actually gets pulled, and nothing
	// would notice, because guard never reads that field at deploy time.
	if saved.Image != "sjc.vultrcr.com/pack/app" {
		t.Fatalf("image is %q", saved.Image)
	}
}

func TestAComposeFileThatIgnoresTheTagIsRefused(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	// Deploying it would pull whatever it already said and change nothing,
	// which looks exactly like a successful deploy of the wrong version.
	_, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name:        "pack",
		ComposeYAML: "services:\n  web:\n    image: nginx:latest\n",
	})
	if err == nil {
		t.Fatal("a compose file that never mentions ${TAG} was accepted")
	}
	if !strings.Contains(err.Error(), "${TAG}") {
		t.Fatalf("refused with %q", err)
	}
}

func TestATemplateDeploysOneImage(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })

	_, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name: "pack",
		ComposeYAML: "services:\n  web:\n    image: r/web:${TAG}\n" +
			"  worker:\n    image: r/worker:${TAG}\n",
	})
	if err == nil {
		t.Fatal("a compose file tagging two different images was accepted")
	}

	// Two services off the *same* image is the ordinary case and is fine.
	same, err := store.SaveDeployTemplate(model.DeployTemplate{
		Name: "pack",
		ComposeYAML: "services:\n  web:\n    image: r/app:${TAG}\n" +
			"  worker:\n    image: r/app:${TAG}\n    command: [\"worker\"]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if same.Image != "r/app" {
		t.Fatalf("image is %q", same.Image)
	}
}

func TestTheHistoryIsPagedAndCapped(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	group, err := store.SaveDeployGroup(model.DeployGroup{Name: "pack", NodeIDs: []int64{node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	press := func(tag string) model.DeployRun {
		t.Helper()
		run, err := store.CreateDeployRun(model.DeployRun{
			GroupID: group.ID, GroupName: group.Name, ServiceName: "app", Tag: tag,
			Mode:      model.ModeSequential,
			Instances: []model.DeployInstance{{NodeID: node.ID, NodeName: node.Name}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	for i := range 5 {
		run := press(fmt.Sprintf("v%d", i))
		if err := store.SetDeployRunStatus(run.ID, model.RunHealthy); err != nil {
			t.Fatal(err)
		}
	}

	total, err := store.CountDeployRuns()
	if err != nil || total != 5 {
		t.Fatalf("total is %d (%v)", total, err)
	}
	// A page, newest first, and the one behind it.
	first, err := store.DeployRuns(3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 || first[0].Tag != "v4" {
		t.Fatalf("first page is %d rows starting at %q", len(first), first[0].Tag)
	}
	second, err := store.DeployRuns(3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].Tag != "v1" {
		t.Fatalf("second page is %d rows starting at %q", len(second), second[0].Tag)
	}
}

func TestTheHistoryStopsGrowingAndKeepsWhatIsLive(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	node := machine(t, store)
	group, err := store.SaveDeployGroup(model.DeployGroup{Name: "pack", NodeIDs: []int64{node.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// One that never finished, pressed first and therefore the oldest. It must
	// survive every prune that follows: a run still waiting for somebody is the
	// one row that must not vanish from under the page watching it.
	stuck, err := store.CreateDeployRun(model.DeployRun{
		GroupID: group.ID, GroupName: group.Name, ServiceName: "app", Tag: "stuck",
		Instances: []model.DeployInstance{{NodeID: node.ID, NodeName: node.Name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeployRunStatus(stuck.ID, model.RunAwaiting); err != nil {
		t.Fatal(err)
	}
	for i := range deployRunRetention + 10 {
		run, err := store.CreateDeployRun(model.DeployRun{
			GroupID: group.ID, GroupName: group.Name, ServiceName: "app",
			Tag:       fmt.Sprintf("v%d", i),
			Instances: []model.DeployInstance{{NodeID: node.ID, NodeName: node.Name}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetDeployRunStatus(run.ID, model.RunHealthy); err != nil {
			t.Fatal(err)
		}
	}

	total, err := store.CountDeployRuns()
	if err != nil {
		t.Fatal(err)
	}
	// The cap, plus the unfinished one that is exempt from it.
	if total > deployRunRetention+1 {
		t.Fatalf("the history grew to %d", total)
	}
	if _, err := store.DeployRun(stuck.ID); err != nil {
		t.Fatalf("the run still waiting for somebody was pruned: %v", err)
	}
	// And the rows behind the runs went with them.
	var orphans int
	if err := store.db.QueryRow(`SELECT count(*) FROM deploy_run_instances i
WHERE NOT EXISTS (SELECT 1 FROM deploy_runs r WHERE r.id = i.run_id)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("%d machine rows outlived their run", orphans)
	}
}
