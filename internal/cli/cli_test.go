package cli_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/noissefnoc/bashtion/internal/cli"
)

func TestNewProcInout(t *testing.T) {
	inout := cli.NewProcInout()
	if inout.Stdin != os.Stdin || inout.Stdout != os.Stdout || inout.Stderr != os.Stderr {
		t.Fatalf("expected standard OS streams, got %+v", inout)
	}
}

func TestSupportsColorAndIsInteractive(t *testing.T) {
	buf := &bytes.Buffer{}
	if cli.IsTerminal(buf) {
		t.Error("bytes.Buffer should not be reported as terminal")
	}
	if cli.SupportsColor(buf) {
		t.Error("non-terminal writer must not support color")
	}
	if cli.IsInteractive(buf) {
		t.Error("non-terminal writer must not be interactive")
	}

	// Verify NO_COLOR, TERM=dumb, and CI=true disable color/interactivity even when inspecting env.
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	if cli.SupportsColor(os.Stderr) {
		t.Error("expected NO_COLOR to disable SupportsColor")
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if cli.SupportsColor(os.Stderr) {
		t.Error("expected TERM=dumb to disable SupportsColor")
	}
	if cli.IsInteractive(os.Stderr) {
		t.Error("expected TERM=dumb to disable IsInteractive")
	}

	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "true")
	if cli.IsInteractive(os.Stderr) {
		t.Error("expected CI=true to disable IsInteractive")
	}
}
