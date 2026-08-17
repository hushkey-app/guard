package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheDevelopmentDotEnvIsTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GUARD_UPDATE_REPO=from-dotenv\nGUARD_RUM_ORIGINS=web\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUARD_DOTENV", path)
	t.Setenv("GUARD_UPDATE_REPO", "")
	t.Setenv("GUARD_RUM_ORIGINS", "")
	set, _ := load(t, nil)

	if got := os.Getenv("GUARD_UPDATE_REPO"); got != "from-dotenv" {
		t.Fatalf("the file was not read: %q", got)
	}
	// And it reads as the environment on the page, not as something stored —
	// which is what makes clearing a stored value fall back to it.
	state, _ := set.State()
	value := find(t, state, "GUARD_UPDATE_REPO")
	if value.Source != "environment" {
		t.Fatalf("a value from the file reads as %q", value.Source)
	}
}

// The conventional rule, and the one that keeps `GUARD_DB_PATH=x make dev`
// meaning what it says.
func TestARealEnvironmentVariableBeatsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GUARD_RUM_ORIGINS=from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUARD_DOTENV", path)
	t.Setenv("GUARD_RUM_ORIGINS", "from-the-shell")
	load(t, nil)
	if got := os.Getenv("GUARD_RUM_ORIGINS"); got != "from-the-shell" {
		t.Fatalf("the file overrode the shell: %q", got)
	}
}

func TestSavingWritesTheFileAndKeepsWhatIsNotGuardsPd(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	original := "# my own notes\nSTRIPE_KEY=sk_test_123\n\nGUARD_RUM_ORIGINS=stale\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUARD_DOTENV", path)
	t.Setenv("GUARD_RUM_ORIGINS", "")
	set, _ := load(t, nil)

	if _, err := set.Save(map[string]string{"GUARD_SSH_TIMEOUT": "10s", "GUARD_UPDATE_REPO": "someone/guard"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	written := string(raw)
	// Somebody else's lines, and their comment, survive a settings save.
	if !strings.Contains(written, "# my own notes") || !strings.Contains(written, "STRIPE_KEY=sk_test_123") {
		t.Fatalf("the file lost lines that are not guard's:\n%s", written)
	}
	if !strings.Contains(written, "GUARD_SSH_TIMEOUT=10s") || !strings.Contains(written, "GUARD_UPDATE_REPO=someone/guard") {
		t.Fatalf("the saved values are not in the file:\n%s", written)
	}
	// A guard variable this file set by hand and the database says nothing about
	// stays — see the hand-written case below — but it moves into the block, once.
	if strings.Count(written, "GUARD_RUM_ORIGINS=") != 1 {
		t.Fatalf("a guard line was duplicated:\n%s", written)
	}
	// Written twice, the file does not grow.
	if _, err := set.Save(map[string]string{"GUARD_SSH_TIMEOUT": "20s"}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if strings.Count(string(again), "# guard —") != 1 {
		t.Fatalf("the header was written twice:\n%s", again)
	}
	if strings.Count(string(again), "GUARD_SSH_TIMEOUT=") != 1 {
		t.Fatalf("a variable was written twice:\n%s", again)
	}
	// It holds the operator token, so it is not world-readable.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the file came out %v", info.Mode().Perm())
	}
}

// A released binary on a server must not write a .env into whatever directory
// systemd started it in.
func TestItIsOffForAReleaseBuildAndCanBeTurnedOff(t *testing.T) {
	t.Setenv("GUARD_DOTENV", "0")
	if dotEnv() != "" {
		t.Fatal("GUARD_DOTENV=0 turns it off")
	}
	os.Unsetenv("GUARD_DOTENV")
	// The build under test is a development one, so the default is on; the
	// release case is the same predicate the update card uses.
	if dotEnv() != DefaultDotEnv {
		t.Fatalf("a development build defaults to %q, got %q", DefaultDotEnv, dotEnv())
	}
}

// This file is one of the two places somebody is invited to edit, so a guard
// variable typed into it by hand survives a settings save that says nothing about
// it. Losing it would make the invitation a trap.
func TestAHandWrittenGuardLineSurvivesASave(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GUARD_RUM_ORIGINS=typed-by-hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUARD_DOTENV", path)
	t.Setenv("GUARD_RUM_ORIGINS", "")
	set, _ := load(t, nil)
	if _, err := set.Save(map[string]string{"GUARD_SSH_TIMEOUT": "10s"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "GUARD_RUM_ORIGINS=typed-by-hand") {
		t.Fatalf("the hand-written line was dropped:\n%s", raw)
	}
	// And once it *is* stored, the stored value is what the file says — one line,
	// not two.
	if _, err := set.Save(map[string]string{"GUARD_RUM_ORIGINS": "from-the-page"}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Count(string(raw), "GUARD_RUM_ORIGINS=") != 1 || !strings.Contains(string(raw), "from-the-page") {
		t.Fatalf("the stored value did not replace the typed one:\n%s", raw)
	}
}

// The mode has to be applied to a file that already exists, which is every save
// after the first.
func TestAnExistingFileIsTightenedRatherThanLeftOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("A=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUARD_DOTENV", path)
	set, _ := load(t, nil)
	if _, err := set.Save(map[string]string{"GUARD_SSH_TIMEOUT": "10s"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("the file is still %v", info.Mode().Perm())
	}
}
