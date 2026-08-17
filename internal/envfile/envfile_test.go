package envfile

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hushkey-app/guard/internal/telemetry/model"
)

var sample = []model.NodeEnvVar{
	{Key: "DATABASE_URL", Value: "postgres://u:p@10.0.0.5:5432/app"},
	{Key: "PASSWORD", Value: "p@ss'w0rd $(reboot)"},
	{Key: "TIMEOUT", Value: "90s"},
}

// Both files, because "the machine's environment" is a different file depending on
// what reads it: a box whose apps are all systemd units gets nothing from
// /etc/environment alone.
func TestInjectWritesBothPlacesAndRereadsSystemd(t *testing.T) {
	command, err := InjectCommand(sample)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{EnvironmentPath, SystemdPath, "mkdir -p /etc/systemd/system.conf.d", "daemon-reexec"} {
		if !strings.Contains(command, want) {
			t.Fatalf("the inject command has no %s in it:\n%s", want, command)
		}
	}
	// Each file: temp beside the target, mode, backup, rename. In that order,
	// twice.
	if got := strings.Count(command, "mktemp"); got != 2 {
		t.Fatalf("want two temp files, got %d", got)
	}
	if got := strings.Count(command, ".guard-bak"); got != 2 {
		t.Fatalf("want two backups, got %d", got)
	}
	if got := strings.Count(command, "mv -f"); got != 2 {
		t.Fatalf("want two renames, got %d", got)
	}
}

// The values travel as base64 and never as shell text, so a password with a quote
// or a $ in it cannot become part of the command.
func TestValuesNeverReachTheCommandLine(t *testing.T) {
	command, err := InjectCommand(sample)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "reboot") || strings.Contains(command, "p@ss") {
		t.Fatalf("a value reached the command line:\n%s", command)
	}
	if !strings.Contains(command, base64.StdEncoding.EncodeToString([]byte(Render(sample)))) {
		t.Fatal("the environment file should be there as base64")
	}
}

func TestWhatTheFilesSay(t *testing.T) {
	env := Render(sample)
	if !strings.HasPrefix(env, "# Written by guard.") {
		t.Fatalf("the file should say what wrote it:\n%s", env)
	}
	if !strings.Contains(env, "TIMEOUT=90s") {
		t.Fatalf("a plain value should be written plainly:\n%s", env)
	}
	// A quote in the value means double quotes and escapes, because a literal
	// newline or a broken quote is a second variable to whatever parses this.
	if !strings.Contains(env, `PASSWORD="p@ss'w0rd $(reboot)"`) {
		t.Fatalf("the awkward value came out as:\n%s", env)
	}

	unit := RenderSystemd(sample)
	if !strings.Contains(unit, "[Manager]") {
		t.Fatalf("a systemd drop-in needs its section:\n%s", unit)
	}
	if got := strings.Count(unit, "DefaultEnvironment="); got != len(sample) {
		t.Fatalf("want one DefaultEnvironment line per variable, got %d", got)
	}
}

func TestAKeyThatIsNotAKeyIsRefused(t *testing.T) {
	if _, err := InjectCommand([]model.NodeEnvVar{{Key: "not a key", Value: "x"}}); err == nil {
		t.Fatal("a key with a space in it is not an environment variable")
	}
	// And the same name twice, which would be one line silently winning over
	// another.
	if _, err := InjectCommand([]model.NodeEnvVar{{Key: "A", Value: "1"}, {Key: "A", Value: "2"}}); err == nil {
		t.Fatal("the same variable twice is not a set")
	}
}
