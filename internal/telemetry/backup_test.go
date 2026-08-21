package telemetry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

// configured fills a store with one of everything a backup is supposed to
// carry, and returns the machine so a caller can ask for its login back.
func configured(t *testing.T, store *Store) Node {
	t.Helper()
	password := "hunter2"
	node, err := store.SaveNode(Node{
		Name: "VPS-1", URL: "http://localhost:8000", Enabled: true,
		SSHAddress: "guard@10.0.0.5:22", Password: &password, Group: "eu",
		Tags: []model.NodeTag{{Label: "web", Colour: "blue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveActions(node.ID, []model.NodeAction{{Name: "Restart", Command: "systemctl restart app"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveNodeEnv(node.ID, []model.NodeEnvVar{{Key: "DATABASE_URL", Value: "postgres://localhost/app"}}); err != nil {
		t.Fatal(err)
	}
	token := "shhh"
	hook, err := store.SaveWebhook(model.Webhook{Name: "ops", URL: "https://example.test/hook", Token: &token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMonitor(model.Monitor{Metric: "cpu_percent", Op: "above", Threshold: 90, WebhookID: hook.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key := "vultr-key"
	if _, err := store.SaveProviderAccount(model.ProviderAccount{Name: "vultr", Provider: "vultr", APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveMember(model.Member{Email: "leo@example.test", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConfig(map[string]string{"GUARD_TOKEN": "a-token"}); err != nil {
		t.Fatal(err)
	}
	space, err := store.SaveWorkspace(model.Workspace{Name: "hushkey"})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := store.Envs(space.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) == 0 {
		t.Fatal("a new workspace has no environments")
	}
	if _, err := store.SaveSecret(model.Secret{EnvID: envs[0].ID, Key: "DB_PASSWORD", Value: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	return node
}

// transfer is what actually happens between the two presses: the document is
// JSON on somebody's disk in between, so every test that restores goes through
// a marshal and a decode rather than handing the struct over in memory.
func transfer(t *testing.T, doc model.Backup) model.Backup {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out model.Backup
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The whole promise, on two instances that share no key: what was configured on
// one is configured on the other, credentials included.
func TestBackupRoundTripCarriesTheConfiguration(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	node := configured(t, source)

	doc, err := source.ExportBackup("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Secrets != model.BackupSecretsPassphrase || doc.KDF == nil {
		t.Fatalf("a passphrase export says secrets=%q kdf=%v", doc.Secrets, doc.KDF)
	}

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	report, err := target.RestoreBackup(transfer(t, doc), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows == 0 || report.Sealed == 0 {
		t.Fatalf("restored %d rows and %d sealed values", report.Rows, report.Sealed)
	}

	nodes, err := target.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Name != "VPS-1" || nodes[0].ID != node.ID {
		t.Fatalf("machines are %+v", nodes)
	}
	if nodes[0].Group != "eu" || len(nodes[0].Tags) != 1 {
		t.Errorf("the machine lost its grouping or tags: %+v", nodes[0])
	}
	// The two ends hold different keys, so this only passes if the value was
	// opened here and re-sealed there.
	login, err := target.SSHLoginFor(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if login.Password != "hunter2" {
		t.Errorf("the SSH password came back as %q", login.Password)
	}
	vars, err := target.NodeEnv(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 1 || vars[0].Value != "postgres://localhost/app" {
		t.Errorf("the machine environment came back as %+v", vars)
	}
	values, err := target.Config()
	if err != nil {
		t.Fatal(err)
	}
	if values["GUARD_TOKEN"] != "a-token" {
		t.Errorf("stored configuration came back as %+v", values)
	}
	if !report.Restart {
		t.Error("a backup carrying stored configuration did not ask for a restart")
	}
	spaces, err := target.Workspaces()
	if err != nil {
		t.Fatal(err)
	}
	var hushkey model.Workspace
	for _, space := range spaces {
		if space.Name == "hushkey" {
			hushkey = space
		}
	}
	if hushkey.ID == 0 {
		t.Fatalf("workspaces are %+v", spaces)
	}
	envs, err := target.Envs(hushkey.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := target.Secret(envs[0].ID, "DB_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	if secret.Value != "s3cret" || secret.Unreadable {
		t.Errorf("the secret came back as %+v", secret)
	}
	members, err := target.Members()
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Email != "leo@example.test" {
		t.Errorf("members are %+v", members)
	}
	hooks, err := target.Webhooks()
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 || !hooks[0].HasToken {
		t.Errorf("destinations are %+v", hooks)
	}
	monitors, err := target.Monitors()
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 || monitors[0].Metric != "cpu_percent" {
		t.Errorf("machine rules are %+v", monitors)
	}
	actions, err := target.Actions(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Command != "systemctl restart app" {
		t.Errorf("commands are %+v", actions)
	}
}

// A backup taken without a passphrase carries the credentials as themselves,
// because the alternative is a restore that comes back as an instance with no
// logins and no keys — which is what "no stored key" on every cloud account the
// morning after a restore actually was.
func TestBackupWithoutAPassphraseCarriesTheCredentials(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	node := configured(t, source)

	doc, err := source.ExportBackup("")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Secrets != model.BackupSecretsPlain || doc.KDF != nil {
		t.Fatalf("an export with no passphrase says secrets=%q kdf=%v", doc.Secrets, doc.KDF)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The file is the credentials. That is the bargain, and the page says so —
	// a test that asserted the opposite would be asserting the bug.
	if !strings.Contains(string(raw), "hunter2") || !strings.Contains(string(raw), "s3cret") {
		t.Error("a backup with no passphrase left the credentials behind")
	}

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	report, err := target.RestoreBackup(transfer(t, doc), "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Sealed == 0 {
		t.Fatalf("restored %d sealed and %d blank values", report.Sealed, report.Blank)
	}
	login, err := target.SSHLoginFor(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if login.Password != "hunter2" {
		t.Errorf("the SSH password came back as %q", login.Password)
	}
	values, err := target.Config()
	if err != nil {
		t.Fatal(err)
	}
	if values["GUARD_TOKEN"] != "a-token" {
		t.Errorf("stored configuration came back as %+v", values)
	}
	accounts, err := target.ProviderAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].HasKey {
		t.Fatalf("cloud accounts are %+v", accounts)
	}
	if key, err := target.ProviderKeyFor(accounts[0].ID); err != nil || key != "vultr-key" {
		t.Errorf("the provider key came back as %q (%v)", key, err)
	}
}

// A passphrase export carries no readable credential at all.
func TestPassphraseBackupHoldsNoPlaintext(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	configured(t, source)
	raw, err := json.Marshal(mustExport(t, source, "the right words"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "s3cret", "vultr-key", "a-token", "shhh"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("%q is in a passphrase-sealed file", secret)
		}
	}
}

// The first version of this format left the credentials out. Those files still
// restore — with every credential blank, which is what they actually carry.
func TestRestoreOfAnOmittedBackupBlanksTheCredentials(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	node := configured(t, source)

	doc := mustExport(t, source, "")
	doc.Secrets = model.BackupSecretsOmitted
	sealed := map[string][]string{}
	for _, table := range backupTables {
		sealed[table.name] = table.sealed
	}
	for t2 := range doc.Tables {
		table := &doc.Tables[t2]
		for index, column := range table.Columns {
			if !contains(sealed[table.Name], column) {
				continue
			}
			for row := range table.Rows {
				table.Rows[row][index] = model.BackupNullValue()
			}
		}
	}

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	report, err := target.RestoreBackup(transfer(t, doc), "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Sealed != 0 || report.Blank == 0 {
		t.Fatalf("restored %d sealed and %d blank values", report.Sealed, report.Blank)
	}
	nodes, err := target.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].HasPassword {
		t.Fatalf("machines are %+v", nodes)
	}
	if _, err := target.SSHLoginFor(node.ID); err == nil {
		t.Error("a machine restored without its password still has a login")
	}
}

// A passphrase export is not readable with the wrong words, and a restore that
// cannot read the file has to leave the instance exactly as it found it.
func TestRestoreRefusesTheWrongPassphraseAndWritesNothing(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	configured(t, source)
	doc, err := source.ExportBackup("the right words")
	if err != nil {
		t.Fatal(err)
	}

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	before, err := target.SaveNode(Node{Name: "already here", URL: "http://localhost:9000", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := target.RestoreBackup(transfer(t, doc), "the wrong words"); !errors.Is(err, ErrBackupPassphrase) {
		t.Fatalf("a wrong passphrase answered %v", err)
	}
	if _, err := target.RestoreBackup(transfer(t, doc), ""); !errors.Is(err, ErrBackupPassphrase) {
		t.Fatalf("no passphrase at all answered %v", err)
	}
	nodes, err := target.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != before.ID {
		t.Fatalf("a refused restore changed the machines: %+v", nodes)
	}
}

// The one thing a backup must never be: a copy of the telemetry. And the one
// thing a restore must never do: throw it away.
func TestBackupHoldsNoTelemetryAndARestoreKeepsIt(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	configured(t, source)
	if err := source.Add(Event{Signal: "logs", Service: "api", Message: "hello", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}

	doc, err := source.ExportBackup("words")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range doc.Tables {
		for _, excluded := range backupExcluded {
			if table.Name == excluded {
				t.Errorf("the backup carries %s", table.Name)
			}
		}
	}

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	if err := target.Add(Event{Signal: "logs", Service: "web", Message: "still here", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.RestoreBackup(transfer(t, doc), "words"); err != nil {
		t.Fatal(err)
	}
	events, err := target.Query(Filter{Signal: "logs", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "still here" {
		t.Fatalf("a restore touched the telemetry: %+v", events)
	}
}

// Timestamps are nanoseconds, which is past where a JSON number decoded as a
// float still counts in ones, and a blob decoded as `any` is a base64 string
// rather than a blob. Both are what the value type's own encoding is for.
func TestBackupValuesSurviveJSON(t *testing.T) {
	const exact = int64(1787165860294656789)
	cases := []model.BackupValue{
		model.BackupIntValue(exact),
		model.BackupIntValue(-1),
		model.BackupRealValue(2),
		model.BackupRealValue(99.5),
		model.BackupTextValue("aGVsbG8="),
		model.BackupBlobValue([]byte{0, 1, 2, 250}),
		model.BackupBlobValue([]byte{}),
		model.BackupNullValue(),
	}
	for _, want := range cases {
		raw, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		var got model.BackupValue
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got.Kind != want.Kind || got.Int != want.Int || got.Real != want.Real || got.Text != want.Text ||
			string(got.Blob) != string(want.Blob) {
			t.Errorf("%s came back as %+v, not %+v", raw, got, want)
		}
	}
}

// The same thing end to end: a machine restored from a file was created at the
// moment it was created, to the nanosecond the database stored.
func TestBackupKeepsNanosecondPrecision(t *testing.T) {
	source := NewStore(100)
	t.Cleanup(func() { source.Close() })
	node := configured(t, source)

	target := NewStore(100)
	t.Cleanup(func() { target.Close() })
	if _, err := target.RestoreBackup(transfer(t, mustExport(t, source, "")), ""); err != nil {
		t.Fatal(err)
	}
	restored, err := target.Node(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !restored.CreatedAt.Equal(node.CreatedAt) {
		t.Fatalf("created at %s, restored as %s", node.CreatedAt, restored.CreatedAt)
	}
}

// A file from a newer guard may mean things this build would ignore, so it is
// refused rather than partly applied.
func TestRestoreRefusesANewerFormat(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	doc := mustExport(t, store, "")
	doc.Format = model.BackupFormat + 1
	if _, err := store.RestoreBackup(doc, ""); err == nil {
		t.Fatal("a newer format was accepted")
	}
	empty := model.Backup{Format: model.BackupFormat}
	if _, err := store.RestoreBackup(empty, ""); err == nil {
		t.Fatal("a file with no tables was accepted")
	}
}

// The summary is what the page draws before anything is pressed, so it has to
// agree with what an export actually takes.
func TestBackupSummaryMatchesTheExport(t *testing.T) {
	store := NewStore(100)
	t.Cleanup(func() { store.Close() })
	configured(t, store)

	summary, err := store.BackupSummary()
	if err != nil {
		t.Fatal(err)
	}
	doc := mustExport(t, store, "words")
	rows := map[string]int{}
	for _, table := range doc.Tables {
		rows[table.Name] = len(table.Rows)
	}
	var sealed int
	for _, line := range summary.Tables {
		if rows[line.Name] != line.Rows {
			t.Errorf("%s: the summary says %d rows, the export took %d", line.Name, line.Rows, rows[line.Name])
		}
		sealed += line.Sealed
	}
	if sealed != summary.Sealed || summary.Sealed == 0 {
		t.Errorf("the summary counts %d sealed values across %d", summary.Sealed, sealed)
	}
	if len(summary.Excluded) == 0 {
		t.Error("the summary names nothing it leaves out")
	}
}

func mustExport(t *testing.T, store *Store, passphrase string) model.Backup {
	t.Helper()
	doc, err := store.ExportBackup(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
