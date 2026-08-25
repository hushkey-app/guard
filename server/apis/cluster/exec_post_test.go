package cluster

import (
	"strings"
	"testing"
)

func TestCommandAcceptsMultiLineShellScripts(t *testing.T) {
	command := Command{NodeID: 7, Command: "sudo tee /etc/example >/dev/null <<'EOF'\nKEY=value\nEOF"}
	if err := command.Validate(); err != nil {
		t.Fatalf("multi-line script was rejected: %v", err)
	}
}

func TestCommandCapsScriptsAt64KiB(t *testing.T) {
	command := Command{NodeID: 7, Command: strings.Repeat("x", (64<<10)+1)}
	if err := command.Validate(); err == nil {
		t.Fatal("oversized script was accepted")
	}
}
