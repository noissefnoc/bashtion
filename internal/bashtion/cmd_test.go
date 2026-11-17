//go:build unix

package bashtion

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/noissefnoc/bashtion/internal/cli"
	"github.com/noissefnoc/bashtion/internal/report"
)

func TestMainCommandByArgs_Version(t *testing.T) {
	origVer := Version
	Version = "1.2.3"
	defer func() { Version = origVer }()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	code := MainCommandByArgs([]string{"--version"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	})
	if code != 0 {
		t.Fatalf("want exit code 0, got %d (stderr: %s)", code, stderr.String())
	}

	expected := "bashtion version 1.2.3\n"
	if stdout.String() != expected {
		t.Errorf("stdout = %q, want %q", stdout.String(), expected)
	}

	// When Version is "dev", MainCommandByArgs should use effectiveToolVersion() (e.g. build info / VCS revision).
	Version = "dev"
	stdout.Reset()
	code = MainCommandByArgs([]string{"-v"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	})
	if code != 0 {
		t.Fatalf("want exit code 0 for -v, got %d (stderr: %s)", code, stderr.String())
	}
	wantDev := "bashtion version " + effectiveToolVersion() + "\n"
	if stdout.String() != wantDev {
		t.Errorf("stdout for dev = %q, want %q", stdout.String(), wantDev)
	}
}

func TestMainCommandByArgs_Guide(t *testing.T) {
	for _, arg := range []string{"guide", "--guide", "--help-agent"} {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}

		code := MainCommandByArgs([]string{arg}, &cli.ProcInout{
			Stdin:  strings.NewReader(""),
			Stdout: stdout,
			Stderr: stderr,
		})
		if code != ExitSuccess {
			t.Fatalf("%s: want exit code %d, got %d (stderr: %s)", arg, ExitSuccess, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "# Bashtion Mutation Testing & Test Remediation Guide") {
			t.Errorf("%s: expected guide content in stdout, got:\n%s", arg, stdout.String())
		}
	}
}

func TestMainCommandByArgs_ExitCodes(t *testing.T) {
	// 1. ExitUsageError (2) on flag / option parsing error
	stderr := &bytes.Buffer{}
	code := MainCommandByArgs([]string{"--target", "a.sh", "oops", "--min-score", "100"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: stderr,
	})
	if code != ExitUsageError {
		t.Fatalf("want exit code %d (ExitUsageError), got %d (stderr: %s)", ExitUsageError, code, stderr.String())
	}

	// 2. ExitSuccess (0) on help
	codeHelp := MainCommandByArgs([]string{"--help"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if codeHelp != ExitSuccess {
		t.Fatalf("want exit code %d for --help, got %d", ExitSuccess, codeHelp)
	}
}

func TestMainCommandByArgs_ListMode(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "check.sh")
	script := `#!/usr/bin/env bash
if [[ "$1" -eq 0 ]]; then
    return 0
fi
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	code := MainCommandByArgs([]string{"--target", scriptPath, "--list"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	})
	if code != 0 {
		t.Fatalf("want exit code 0, got %d (stderr: %s)", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "generated 2 AST mutants") {
		t.Errorf("expected 2 AST mutants in stdout, got:\n%s", out)
	}
	if !strings.Contains(out, "[TEST_BIN_OP]") || !strings.Contains(out, "[RETURN_CODE]") {
		t.Errorf("expected operator categories in stdout, got:\n%s", out)
	}
}

func TestMainCommandWithDeps_FullRun(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "check.sh")
	testPath := filepath.Join(tmpDir, "check.bats")

	script := `#!/usr/bin/env bash
if [[ "$1" -eq 0 ]]; then
    return 0
fi
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	var calls atomic.Int32
	fakeExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return []byte("1..1\nok 1\n"), nil
		}
		if n == 2 {
			return []byte("not ok 1\n"), exec.Command("sh", "-c", "exit 1").Run()
		}
		return []byte("ok 1\n"), nil
	}

	err := MainCommandWithDeps(&Options{
		Target:  scriptPath,
		Test:    testPath,
		BatsBin: "bats",
		Jobs:    2,
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{
		WorkDir:  tmpDir,
		Executor: fakeExec,
	})
	if err != nil {
		t.Fatalf("MainCommandWithDeps failed: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Mutation Score: 50.0%") {
		t.Errorf("expected Mutation Score: 50.0%%, got:\n%s", out)
	}
	if !strings.Contains(out, "Survived Mutants (1)") {
		t.Errorf("expected Survived Mutants (1), got:\n%s", out)
	}

	errOut := stderr.String()
	if !strings.Contains(errOut, "[01/02]") || !strings.Contains(errOut, "[02/02]") {
		t.Errorf("expected progress lines in stderr, got:\n%s", errOut)
	}
	if strings.Contains(out, "[01/02]") {
		t.Errorf("expected stdout to be free of progress lines, got:\n%s", out)
	}
}

func TestMainCommandWithDeps_LLMFormatAndMinScoreAndGitDiff(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "check.sh")
	testPath := filepath.Join(tmpDir, "check.bats")

	script := `#!/usr/bin/env bash
validate() {
    if [[ "$1" -eq 0 ]]; then
        return 0
    fi
    return 1
}
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	// Fake executor where mutant survives -> 0% score, below min-score 80%
	fakeExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok 1\n"), nil
	}
	// Only line 3 changed in git diff
	fakeDiff := func(ctx context.Context, workDir, ref, targetFile string) (map[uint]bool, error) {
		return map[uint]bool{3: true}, nil
	}

	err := MainCommandWithDeps(&Options{
		Target:   scriptPath,
		Test:     testPath,
		BatsBin:  "bats",
		Jobs:     1,
		Format:   report.FormatLLM,
		MinScore: 80.0,
		GitDiff:  "HEAD",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{
		WorkDir:      tmpDir,
		Executor:     fakeExec,
		ChangedLines: fakeDiff,
	})
	if err == nil || !strings.Contains(err.Error(), "below required threshold 80.0%") {
		t.Fatalf("expected min-score threshold error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "# bashtion LLM Agent Feedback") {
		t.Errorf("expected LLM feedback header, got:\n%s", out)
	}
	if !strings.Contains(out, "1 survived / 1 total") {
		t.Errorf("expected git-diff filter to restrict to 1 mutant on line 3, got:\n%s", out)
	}
}

func TestMainCommandWithDeps_MultiTargetGlobAndAutoMapping(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	testDir := filepath.Join(tmpDir, "test")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(testDir, 0o755); err != nil {
		t.Fatal(err)
	}

	scriptA := "#!/usr/bin/env bash\nif [[ \"$1\" -eq 0 ]]; then return 0; fi\n"
	scriptB := "#!/usr/bin/env bash\nif [[ \"$1\" == \"ok\" ]]; then return 0; fi\n"
	if err := os.WriteFile(filepath.Join(srcDir, "a.sh"), []byte(scriptA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "b.sh"), []byte(scriptB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "a.bats"), []byte("# bats a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "b.bats"), []byte("# bats b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	fakeExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok 1\n"), nil
	}

	err := MainCommandWithDeps(&Options{
		Target:    "src/*.sh",
		BatsBin:   "bats",
		Jobs:      2,
		CacheFile: filepath.Join(tmpDir, ".bashtion-cache.json"),
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{
		WorkDir:  tmpDir,
		Executor: fakeExec,
	})
	if err != nil {
		t.Fatalf("MainCommandWithDeps multi-target failed: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Total Mutants : 4") {
		t.Errorf("expected Total Mutants : 4 across a.sh and b.sh, got:\n%s", out)
	}
	if !strings.Contains(out, "src/a.sh") || !strings.Contains(out, "src/b.sh") {
		t.Errorf("expected both src/a.sh and src/b.sh in output, got:\n%s", out)
	}

	multiErrOut := stderr.String()
	if !strings.Contains(multiErrOut, "[03/04]") || !strings.Contains(multiErrOut, "[04/04]") {
		t.Errorf("expected global numerator/denominator [03/04] and [04/04] in multi-target progress, got:\n%s", multiErrOut)
	}
}

func TestMainCommandWithDeps_ZeroMutantsFailsWhenMinScoreSet(t *testing.T) {
	tmpDir := t.TempDir()
	emptyScript := "#!/usr/bin/env bash\n# only comments\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "empty.sh"), []byte(emptyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	emptyBats := "@test \"dummy\" { run true; }\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "empty.bats"), []byte(emptyBats), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	fakeExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok 1 dummy\n"), nil
	}

	err := MainCommandWithDeps(&Options{
		Target:   "empty.sh",
		Test:     "empty.bats",
		MinScore: 80.0,
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{
		WorkDir:  tmpDir,
		Executor: fakeExec,
	})
	if err == nil || !strings.Contains(err.Error(), "no mutants were generated to satisfy required min-score") {
		t.Fatalf("expected error when 0 mutants evaluated against --min-score, got: %v", err)
	}
}

func TestMainCommandWithDeps_OmittedTestResolvesInWorkDirAndFailsWhenMissing(t *testing.T) {
	tmpDir := t.TempDir()
	otherCwd := t.TempDir()
	t.Chdir(otherCwd)

	script := "#!/usr/bin/env bash\ngreet() {\n  echo hi\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "solo.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	opts, err := ParseOptions([]string{"--target", "solo.sh"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("ParseOptions should allow omitted --test and defer validation to discovery: %v", err)
	}

	err = MainCommandWithDeps(opts, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}, Deps{
		WorkDir: tmpDir,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok 1\n"), nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "could not auto-discover a .bats file") {
		t.Fatalf("expected discovery failure when matching .bats is missing in WorkDir, got: %v", err)
	}

	// Now add matching solo.bats in deps.WorkDir (not cwd!) and verify omitted --test resolves and runs.
	if err := os.WriteFile(filepath.Join(tmpDir, "solo.bats"), []byte("@test \"solo\" { run true; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err = MainCommandWithDeps(opts, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{
		WorkDir: tmpDir,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok 1\n"), nil
		},
	})
	if err != nil {
		t.Fatalf("MainCommandWithDeps failed when matching .bats exists in WorkDir: %v", err)
	}
	if !strings.Contains(stderr.String(), "solo.bats") {
		t.Errorf("expected resolved solo.bats in stderr header, got:\n%s", stderr.String())
	}
}

func TestResolveCacheFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	xdgCache := filepath.Join(tmpDir, "xdg-cache")
	workDir := filepath.Join(tmpDir, "repo")
	t.Setenv("XDG_CACHE_HOME", xdgCache)

	// Default on real runs goes under XDG cache dir, keeping workDir clean.
	gotDefault := resolveCacheFilePath(workDir, "", true)
	if !strings.HasPrefix(gotDefault, filepath.Join(xdgCache, "bashtion")+string(filepath.Separator)) {
		t.Errorf("expected XDG cache path, got %q", gotDefault)
	}

	// Non-real (mock executor) runs stay cache-free unless explicitly overridden.
	if gotMock := resolveCacheFilePath(workDir, "", false); gotMock != "" {
		t.Errorf("expected empty default cache path for mock runs, got %q", gotMock)
	}

	// Explicit relative override resolves under workDir.
	gotRel := resolveCacheFilePath(workDir, ".custom-cache.json", true)
	wantRel := filepath.Join(workDir, ".custom-cache.json")
	if gotRel != wantRel {
		t.Errorf("expected %q for relative explicit cache file, got %q", wantRel, gotRel)
	}

	// Explicit absolute override is preserved verbatim.
	absOverride := filepath.Join(tmpDir, "explicit.json")
	if gotAbs := resolveCacheFilePath(workDir, absOverride, true); gotAbs != absOverride {
		t.Errorf("expected %q for absolute override, got %q", absOverride, gotAbs)
	}
}

func TestMainCommandWithDeps_GitDiffZeroMutantsPassesMinScore_AndWorkDirReadIsolation(t *testing.T) {
	workDir := t.TempDir()
	cwdDir := t.TempDir()
	t.Chdir(cwdDir)

	// Create a conflicting same-named file in CWD with broken syntax; WorkDir copy is valid.
	if err := os.MkdirAll(filepath.Join(cwdDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, "sub", "target.sh"), []byte("if (((\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(workDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "sub", "target.sh"), []byte("#!/usr/bin/env bash\ncheck() { [[ \"$1\" == ok ]]; }\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "sub", "target.bats"), []byte("@test \"ok\" { check ok; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := ParseOptions([]string{
		"--target", "sub/target.sh",
		"--test", "sub/target.bats",
		"--git-diff", "HEAD",
		"--min-score", "80",
		"--format", "llm",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}

	stdout := &bytes.Buffer{}
	err = MainCommandWithDeps(opts, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: &bytes.Buffer{},
	}, Deps{
		WorkDir: workDir,
		ChangedLines: func(ctx context.Context, wd, ref, target string) (map[uint]bool, error) {
			// Empty map simulates no lines changed in target.sh -> 0 mutants generated.
			return map[uint]bool{}, nil
		},
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok 1\n"), nil
		},
	})
	if err != nil {
		t.Fatalf("expected 0 mutants with --git-diff to satisfy --min-score, got error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Status: NO_MUTANTS") {
		t.Errorf("expected Status: NO_MUTANTS in output, got:\n%s", stdout.String())
	}

	// Without --git-diff, 0 mutants under --min-score must still fail.
	if err := os.WriteFile(filepath.Join(workDir, "sub", "target.sh"), []byte("# comments only\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	optsNoDiff, err := ParseOptions([]string{
		"--target", "sub/target.sh",
		"--test", "sub/target.bats",
		"--min-score", "80",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := MainCommandWithDeps(optsNoDiff, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}, Deps{
		WorkDir: workDir,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok 1\n"), nil
		},
	}); err == nil {
		t.Errorf("expected error when 0 mutants generated with --min-score without --git-diff")
	}
}
