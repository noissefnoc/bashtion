package cli

import (
	"io"
	"os"
	"strings"
)

// ProcInout encapsulates process standard streams so CLI commands can be tested in memory.
type ProcInout struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// NewProcInout returns a ProcInout wired to os.Stdin, os.Stdout, and os.Stderr.
func NewProcInout() *ProcInout {
	return &ProcInout{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

// Command is the standard signature for testable CLI commands.
type Command func(args []string, inout *ProcInout) int

// Run executes the given Command with os.Args[1:] and terminates with its exit status.
func Run(c Command) {
	args := os.Args[1:]
	exitStatus := c(args, NewProcInout())
	os.Exit(exitStatus)
}

// IsTerminal reports whether w is backed by a character device (terminal).
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// SupportsColor reports whether ANSI color escape sequences should be emitted to w.
// It honors the NO_COLOR standard, TERM=dumb/empty, and TTY detection.
func SupportsColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	term := strings.TrimSpace(os.Getenv("TERM"))
	if term == "" || strings.EqualFold(term, "dumb") {
		return false
	}
	return IsTerminal(w)
}

// IsInteractive reports whether interactive terminal behavior is appropriate for w.
// Returns false in CI environments, when TERM=dumb, or when w is not a terminal.
func IsInteractive(w io.Writer) bool {
	if ci := strings.TrimSpace(os.Getenv("CI")); ci != "" && !strings.EqualFold(ci, "false") && ci != "0" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	return IsTerminal(w)
}
