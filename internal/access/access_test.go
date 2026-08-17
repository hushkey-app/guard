package access

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func keys(t *testing.T, running Credentials) *Keys {
	t.Helper()
	return &Keys{Path: filepath.Join(t.TempDir(), "tokens.env"), Running: running}
}

func TestGenerateWritesAndReadsBack(t *testing.T) {
	k := keys(t, Credentials{})
	state, err := k.Generate(NameToken)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(state.Token) != 64 {
		t.Fatalf("want 32 bytes of hex, got %q", state.Token)
	}
	// Nothing is in force until a start reads the file, and the card has to be
	// able to say so — a rotation the collectors have not seen yet is the whole
	// state this feature lives in.
	if !state.TokenPending {
		t.Fatal("a value written but not started into must read as pending")
	}
	if state.Secret != "" || state.SecretPending {
		t.Fatal("generating one credential must not touch the other")
	}
	// And it comes back from disk, not from memory: the process that answers
	// the next page load is a different one.
	again := (&Keys{Path: k.Path}).State()
	if again.Token != state.Token {
		t.Fatalf("file lost the value: %q vs %q", again.Token, state.Token)
	}
}

func TestRunningValueShowsWhenNothingIsWritten(t *testing.T) {
	k := keys(t, Credentials{Token: "from-guard-env"})
	state := k.State()
	if state.Token != "from-guard-env" {
		t.Fatalf("want the environment's value, got %q", state.Token)
	}
	if state.TokenPending {
		t.Fatal("what the process is running is not pending")
	}
}

func TestGeneratedValueMatchingTheProcessIsNotPending(t *testing.T) {
	k := keys(t, Credentials{})
	state, err := k.Generate(NameSecret)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// The restart happened: the next process starts with what the file says.
	restarted := &Keys{Path: k.Path, Running: Credentials{Secret: state.Secret}}
	if restarted.State().SecretPending {
		t.Fatal("after a restart the file and the process agree")
	}
}

func TestClearRemovesOnlyThatName(t *testing.T) {
	k := keys(t, Credentials{})
	if _, err := k.Generate(NameToken); err != nil {
		t.Fatalf("generate token: %v", err)
	}
	state, err := k.Generate(NameSecret)
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	secret := state.Secret
	state, err = k.Clear(NameToken)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if state.Token != "" {
		t.Fatalf("token survived the clear: %q", state.Token)
	}
	if state.Secret != secret {
		t.Fatal("clearing one credential must leave the other alone")
	}
}

// The file is loaded straight into guard's environment, so the set of names it
// may contain is closed. Without this a browser could set any environment
// variable on the box.
func TestOnlyTheTwoNamesAreWritable(t *testing.T) {
	k := keys(t, Credentials{})
	if _, err := k.Generate("LD_PRELOAD"); err == nil {
		t.Fatal("want a refusal for a name guard does not own")
	}
	if _, err := os.Stat(k.Path); err == nil {
		t.Fatal("a refused name must not have written a file")
	}
}

func TestUnwritableDirectoryRefusesInsteadOfPanicking(t *testing.T) {
	k := &Keys{Path: filepath.Join(t.TempDir(), "no-such-dir", "tokens.env")}
	state := k.State()
	if state.Managed {
		t.Fatal("a missing directory is not managed")
	}
	if _, err := k.Generate(NameToken); err == nil {
		t.Fatal("want a refusal where guard cannot write")
	}
}

func TestRestartIsRefusedWhenNothingWouldStartGuardAgain(t *testing.T) {
	k := keys(t, Credentials{})
	if err := k.Ask(); err == nil {
		t.Fatal("exiting where nothing restarts guard is stopping, not restarting")
	}
	called := false
	k.Restart = func() { called = true }
	if err := k.Ask(); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !called {
		t.Fatal("want the restart to have been asked for")
	}
}

// The file is rewritten whole on every press, so it must never accumulate a
// second line for a name — systemd takes the last one, and a stale duplicate
// above it is a rotation that silently did nothing.
func TestFileIsRewrittenWholeAndStaysReadableOnlyByGuard(t *testing.T) {
	k := keys(t, Credentials{})
	for i := 0; i < 3; i++ {
		if _, err := k.Generate(NameToken); err != nil {
			t.Fatalf("generate: %v", err)
		}
	}
	raw, err := os.ReadFile(k.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(raw), NameToken+"="); got != 1 {
		t.Fatalf("want one line for %s, got %d", NameToken, got)
	}
	info, err := os.Stat(k.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %v", info.Mode().Perm())
	}
}
