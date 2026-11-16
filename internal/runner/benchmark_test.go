//go:build unix

package runner_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/runner"
)

func BenchmarkRun_WorkspaceSetupAndDispatch(b *testing.B) {
	workDir := b.TempDir()
	// Create 40 project files to simulate a real repository workspace
	for i := 0; i < 40; i++ {
		subDir := filepath.Join(workDir, fmt.Sprintf("pkg_%d", i%5))
		_ = os.MkdirAll(subDir, 0o755)
		content := make([]byte, 4096)
		if err := os.WriteFile(filepath.Join(subDir, fmt.Sprintf("helper_%d.sh", i)), content, 0o644); err != nil {
			b.Fatal(err)
		}
	}

	scriptContent := `#!/usr/bin/env bash
validate() {
    if [[ "$1" -ge 10 ]] && [[ "$1" -le 100 ]]; then
        return 0
    fi
    return 1
}
`
	if err := os.WriteFile(filepath.Join(workDir, "target.sh"), []byte(scriptContent), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "target.bats"), []byte("# bats\n"), 0o644); err != nil {
		b.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		b.Fatal(err)
	}

	noopExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok\n"), nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := runner.Run(context.Background(), runner.Config{
			WorkDir:    workDir,
			TargetFile: "target.sh",
			TestFile:   "target.bats",
			Jobs:       4,
			Executor:   noopExec,
		}, mutants)
		if err != nil {
			b.Fatal(err)
		}
	}
}
