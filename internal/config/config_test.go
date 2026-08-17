package config

import (
	"os"
	"strings"
	"testing"
)

type memory struct{ values map[string]string }

func (m *memory) Config() (map[string]string, error) {
	out := map[string]string{}
	for name, value := range m.values {
		out[name] = value
	}
	return out, nil
}

func (m *memory) SetConfig(values map[string]string) error {
	for name, value := range values {
		if value == "" {
			delete(m.values, name)
			continue
		}
		m.values[name] = value
	}
	return nil
}

func load(t *testing.T, stored map[string]string) (*Set, *memory) {
	t.Helper()
	t.Setenv("GUARD_CONFIG_IGNORE", "")
	// Off unless the test asked for it before calling this. These run as a
	// development build, so the default would be a `.env` written into the package
	// directory — which is both litter and a way for one test's save to become the
	// next test's environment.
	if _, wanted := os.LookupEnv("GUARD_DOTENV"); !wanted {
		t.Setenv("GUARD_DOTENV", "0")
	}
	store := &memory{values: map[string]string{}}
	for name, value := range stored {
		store.values[name] = value
	}
	set, err := Load(store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return set, store
}

func find(t *testing.T, state State, name string) Value {
	t.Helper()
	for _, group := range state.Groups {
		for _, value := range group.Values {
			if value.Name == name {
				return value
			}
		}
	}
	t.Fatalf("%s is not in the catalogue", name)
	return Value{}
}

// The whole design in one test: what is stored becomes the environment, and
// everything above this package goes on reading os.Getenv.
func TestStoredValuesBecomeTheEnvironment(t *testing.T) {
	t.Setenv("GUARD_ALERT_INTERVAL", "9m")
	load(t, map[string]string{"GUARD_ALERT_INTERVAL": "45s"})
	if got := os.Getenv("GUARD_ALERT_INTERVAL"); got != "45s" {
		t.Fatalf("want the stored value in the environment, got %q", got)
	}
}

func TestStoredOutranksTheEnvironmentAndSaysWhere(t *testing.T) {
	t.Setenv("GUARD_RUM_RELEASE", "from-the-unit-file")
	set, _ := load(t, map[string]string{"GUARD_RUM_RELEASE": "from-the-dashboard"})
	state, err := set.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	value := find(t, state, "GUARD_RUM_RELEASE")
	if value.Value != "from-the-dashboard" || value.Source != "stored" {
		t.Fatalf("want the stored value, got %q from %q", value.Value, value.Source)
	}
	// And the environment is still what it falls back to, which is only
	// answerable because the environment is read before it is overwritten.
	if _, err := set.Save(map[string]string{"GUARD_RUM_RELEASE": ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	state, _ = set.State()
	value = find(t, state, "GUARD_RUM_RELEASE")
	if value.Value != "from-the-unit-file" || value.Source != "environment" {
		t.Fatalf("want the unit file's value back, got %q from %q", value.Value, value.Source)
	}
}

func TestPendingIsSavedButNotRunning(t *testing.T) {
	set, _ := load(t, nil)
	state, _ := set.State()
	if state.Pending {
		t.Fatal("nothing has been changed yet")
	}
	state, err := set.Save(map[string]string{"GUARD_MONITOR_INTERVAL": "10s"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !state.Pending || !find(t, state, "GUARD_MONITOR_INTERVAL").Pending {
		t.Fatal("a saved value this process is not running is pending")
	}
}

func TestBootstrapValuesCannotBeStored(t *testing.T) {
	set, store := load(t, nil)
	for _, name := range []string{"GUARD_DB_PATH", "GUARD_SECRET_KEY"} {
		if _, err := set.Save(map[string]string{name: "somewhere-else"}); err == nil {
			t.Fatalf("%s must be refused: guard needs it before it can read the database", name)
		}
	}
	if len(store.values) != 0 {
		t.Fatal("a refused save must write nothing")
	}
}

// The key that seals every secret in the database is the one value that never
// leaves the process, unlike the tokens, which are the point of the card.
func TestTheSecretKeyIsNeverSentOut(t *testing.T) {
	t.Setenv("GUARD_SECRET_KEY", "0123456789abcdef")
	set, _ := load(t, nil)
	state, _ := set.State()
	value := find(t, state, "GUARD_SECRET_KEY")
	if value.Value != "" {
		t.Fatalf("the secret key must never be sent to a browser, got %q", value.Value)
	}
	if !value.IsSet {
		t.Fatal("but the page still has to be able to say that one is set")
	}
	if token := find(t, state, "GUARD_TOKEN"); token.Hidden {
		t.Fatal("the operator token is shown in full on purpose")
	}
}

func TestUnknownNamesAreRefused(t *testing.T) {
	set, _ := load(t, nil)
	if _, err := set.Save(map[string]string{"LD_PRELOAD": "/tmp/x.so"}); err == nil {
		t.Fatal("the catalogue is the whole set of names that may be written")
	}
}

func TestValuesAreValidatedBeforeTheyAreStored(t *testing.T) {
	set, store := load(t, nil)
	cases := map[string]string{
		"GUARD_MONITOR_INTERVAL": "soon",
		"GUARD_RUM_PER_MINUTE":   "lots",
		"GUARD_ALERT_WEBHOOK":    "chat.example.com/hook",
		"GUARD_RUM_SERVICE":      "one\ntwo",
	}
	for name, value := range cases {
		if _, err := set.Save(map[string]string{name: value}); err == nil {
			t.Fatalf("%s=%q should have been refused", name, value)
		}
	}
	if len(store.values) != 0 {
		t.Fatal("a refused save must write nothing")
	}
}

// Guard treats half a provider's credentials as fatal at startup, on purpose.
// The moment to say so is while somebody is still looking at the field.
func TestHalfASignInConfigurationIsRefused(t *testing.T) {
	set, _ := load(t, nil)
	if _, err := set.Save(map[string]string{"GUARD_GOOGLE_CLIENT_ID": "id.apps.googleusercontent.com"}); err == nil {
		t.Fatal("a client id with no secret is a guard that will not start")
	}
	if _, err := set.Save(map[string]string{
		"GUARD_GOOGLE_CLIENT_ID":     "id.apps.googleusercontent.com",
		"GUARD_GOOGLE_CLIENT_SECRET": "shh",
	}); err != nil {
		t.Fatalf("both halves together: %v", err)
	}
	// And taking one away again is refused for the same reason.
	if _, err := set.Save(map[string]string{"GUARD_GOOGLE_CLIENT_SECRET": ""}); err == nil {
		t.Fatal("removing one half leaves the same broken configuration")
	}
}

func TestAppleWantsAllFourOrNone(t *testing.T) {
	set, _ := load(t, nil)
	if _, err := set.Save(map[string]string{"GUARD_APPLE_CLIENT_ID": "app.guard.signin"}); err == nil {
		t.Fatal("one of Apple's four is not a configuration")
	}
	_, err := set.Save(map[string]string{
		"GUARD_APPLE_CLIENT_ID":   "app.guard.signin",
		"GUARD_APPLE_TEAM_ID":     "TEAM123456",
		"GUARD_APPLE_KEY_ID":      "KEY1234567",
		"GUARD_APPLE_PRIVATE_KEY": "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----",
	})
	if err != nil {
		t.Fatalf("all four together: %v", err)
	}
}

// A PEM key is the reason multiline exists: its own line breaks are the value.
func TestMultilineKeepsItsLineBreaks(t *testing.T) {
	set, store := load(t, nil)
	key := "-----BEGIN PRIVATE KEY-----\nline one\nline two\n-----END PRIVATE KEY-----"
	if _, err := set.Save(map[string]string{
		"GUARD_APPLE_CLIENT_ID":   "app.guard.signin",
		"GUARD_APPLE_TEAM_ID":     "TEAM123456",
		"GUARD_APPLE_KEY_ID":      "KEY1234567",
		"GUARD_APPLE_PRIVATE_KEY": "\n" + key + "\n",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := store.values["GUARD_APPLE_PRIVATE_KEY"]; got != key {
		t.Fatalf("the key came back changed:\n%q", got)
	}
}

// The way back from a stored value that stops guard from starting.
func TestIgnoreSkipsTheStoredConfiguration(t *testing.T) {
	t.Setenv("GUARD_RUM_RELEASE", "from-the-unit-file")
	t.Setenv("GUARD_CONFIG_IGNORE", "1")
	store := &memory{values: map[string]string{"GUARD_RUM_RELEASE": "stored-value"}}
	set, err := Load(store)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("GUARD_RUM_RELEASE"); got != "from-the-unit-file" {
		t.Fatalf("stored configuration was applied anyway: %q", got)
	}
	state, _ := set.State()
	if !state.Ignored {
		t.Fatal("the page has to say that what is stored is not what is running")
	}
}

func TestEveryEntryIsUniqueAndInAGroupThePageDraws(t *testing.T) {
	seen := map[string]bool{}
	groups := map[string]bool{}
	for _, name := range groupOrder() {
		groups[name] = true
	}
	for _, entry := range Entries {
		if seen[entry.Name] {
			t.Fatalf("%s is in the catalogue twice", entry.Name)
		}
		seen[entry.Name] = true
		if !strings.HasPrefix(entry.Name, "GUARD_") {
			t.Fatalf("%s is not one of guard's variables", entry.Name)
		}
		if !groups[entry.Group] {
			t.Fatalf("%s is in group %q, which the page never draws", entry.Name, entry.Group)
		}
	}
}

// The one thing on this page that is minted rather than typed.
func TestGeneratingACredential(t *testing.T) {
	set, store := load(t, nil)
	state, err := set.Generate("GUARD_OTEL_SECRET")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	value := find(t, state, "GUARD_OTEL_SECRET")
	if len(value.Value) != 64 {
		t.Fatalf("want 32 bytes of hex, got %q", value.Value)
	}
	if store.values["GUARD_OTEL_SECRET"] != value.Value {
		t.Fatal("the value reported is not the value stored")
	}
	// It lands like any other saved value: stored, and not in force until a
	// restart. A card that claimed otherwise would be a card lying about which
	// secret the collectors are presenting.
	if value.Source != "stored" || !value.Pending {
		t.Fatalf("generated value reads as %+v", value)
	}
	// Twice in a row is two different values, which is the whole point.
	again, err := set.Generate("GUARD_OTEL_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if find(t, again, "GUARD_OTEL_SECRET").Value == value.Value {
		t.Fatal("generating again produced the same value")
	}
	// And it did not touch the other one: rotating the collector's secret and
	// rotating the operator's token are separate days.
	if find(t, again, "GUARD_TOKEN").IsSet {
		t.Fatal("generating one credential must not touch the other")
	}
}

// A generate button is only honest where guard is the thing that issues the
// value. Everything else in the catalogue comes from somewhere with an opinion.
func TestOnlyTheTwoTokensCanBeGenerated(t *testing.T) {
	set, store := load(t, nil)
	generatable := map[string]bool{"GUARD_TOKEN": true, "GUARD_OTEL_SECRET": true}
	for _, entry := range Entries {
		if entry.Generate != generatable[entry.Name] {
			t.Fatalf("%s is marked generatable=%v", entry.Name, entry.Generate)
		}
		if entry.Generate {
			continue
		}
		if _, err := set.Generate(entry.Name); err == nil {
			t.Fatalf("%s should not be mintable — nothing here issues it but its provider", entry.Name)
		}
	}
	if _, err := set.Generate("NOT_A_SETTING"); err == nil {
		t.Fatal("a name that is not in the catalogue is not a credential")
	}
	if len(store.values) != 0 {
		t.Fatal("a refused generate must write nothing")
	}
}

func TestRestartIsRefusedWhenNothingWouldStartGuardAgain(t *testing.T) {
	set, _ := load(t, nil)
	err := set.Restart()
	if err == nil {
		t.Fatal("exiting where nothing restarts guard is stopping, not restarting")
	}
	if !strings.Contains(err.Error(), "by hand") {
		t.Fatalf("want an answer that says what to do instead, got %q", err)
	}
	called := false
	set.Restartable(func() { called = true })
	if err := set.Restart(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !called {
		t.Fatal("want the restart to have been asked for")
	}
}
