//go:build unix

package bashtion

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noissefnoc/bashtion/internal/cli"
)

func requireBats(t *testing.T) string {
	t.Helper()
	batsPath, err := exec.LookPath("bats")
	if err != nil {
		t.Skip("skipping real bats integration test: bats executable not found in PATH")
	}
	if err := exec.Command(batsPath, "--version").Run(); err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) {
			t.Skipf("skipping real bats integration test: sandbox denied executing %s: %v", batsPath, err)
		}
		t.Fatalf("bats executable %s failed --version check: %v", batsPath, err)
	}
	return batsPath
}

type jsonReportEnvelope struct {
	TotalMutants int `json:"total_mutants"`
	Killed       int `json:"killed"`
	Survived     int `json:"survived"`
	Uncovered    int `json:"uncovered"`
	CacheHits    int `json:"cache_hits"`
	Timeout      int `json:"timeout"`
	Errors       int `json:"errors"`
}

func decodeJSONReport(t *testing.T, data []byte) jsonReportEnvelope {
	t.Helper()
	var env jsonReportEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("failed to decode JSON report: %v\nraw:\n%s", err, string(data))
	}
	return env
}

func TestIntegration_HeaderCommentsCoverageAndSubstringFuncs(t *testing.T) {
	batsPath := requireBats(t)
	workDir := t.TempDir()
	xdgCache := filepath.Join(t.TempDir(), "xdg-cache")
	t.Setenv("XDG_CACHE_HOME", xdgCache)

	script := `#!/usr/bin/env bash
# Summary: Greeter & log validator
# Usage: app.sh <arg>

log() {
  if [ -n "$1" ]; then
    return 0
  fi
  return 1
}

login_check() {
  if [ "$1" = "admin" ]; then
    return 0
  fi
  return 1
}

unused_feature() {
  if [ "$1" = "never" ]; then
    return 0
  fi
  return 1
}
`
	bats := `setup() {
  source ./app.sh
}

@test "header comment and log check" {
  head -n 2 app.sh | grep -q "^# Summary: Greeter & log validator$"
  run log "ok"
  [ "$status" -eq 0 ]
  run log ""
  [ "$status" -eq 1 ]
}

@test "login_check accepts admin" {
  run login_check "admin"
  [ "$status" -eq 0 ]
  run login_check "guest"
  [ "$status" -eq 1 ]
}
`
	if err := os.WriteFile(filepath.Join(workDir, "app.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "app.bats"), []byte(bats), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := MainCommandWithDeps(&Options{
		Target:  "app.sh",
		Test:    "app.bats",
		BatsBin: batsPath,
		Jobs:    2,
		Format:  "json",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{WorkDir: workDir})
	if err != nil {
		t.Fatalf("integration run failed: %v\nstderr:\n%s", err, stderr.String())
	}

	rep := decodeJSONReport(t, stdout.Bytes())
	if rep.TotalMutants == 0 {
		t.Fatal("expected generated mutants > 0")
	}
	if rep.Errors != 0 {
		t.Errorf("expected 0 errors, got %d\nstderr:\n%s", rep.Errors, stderr.String())
	}
	if rep.Killed == 0 {
		t.Errorf("expected killed mutants > 0, summary=%+v", rep)
	}
	if rep.Uncovered == 0 {
		t.Errorf("expected unused_feature to produce uncovered mutants, summary=%+v", rep)
	}
}

func TestIntegration_LINENOAssertionFallsBackToUninstrumented(t *testing.T) {
	batsPath := requireBats(t)
	workDir := t.TempDir()

	// Line 3 checks $LINENO == 3. Coverage preamble insertion shifts it to line 4, failing instrumented baseline
	// and exercising automatic uninstrumented baseline fallback with real bash + bats.
	script := `#!/usr/bin/env bash
check_line() {
  [ "$LINENO" -eq 3 ] || return 1 # bashtion: disable
  if [ "$1" = "ok" ]; then
    return 0
  fi
  return 1
}
`
	bats := `setup() {
  source ./lineno_check.sh
}

@test "check_line honors exact LINENO and argument" {
  run check_line "ok"
  [ "$status" -eq 0 ]
  run check_line "bad"
  [ "$status" -eq 1 ]
}
`
	if err := os.WriteFile(filepath.Join(workDir, "lineno_check.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "lineno_check.bats"), []byte(bats), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := MainCommandWithDeps(&Options{
		Target:  "lineno_check.sh",
		Test:    "lineno_check.bats",
		BatsBin: batsPath,
		Jobs:    2,
		NoCache: true,
		Format:  "json",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{WorkDir: workDir})
	if err != nil {
		t.Fatalf("LINENO fallback run failed: %v\nstderr:\n%s", err, stderr.String())
	}

	if !strings.Contains(stderr.String(), "fell back to uninstrumented baseline") {
		t.Errorf("expected uninstrumented baseline fallback warning on stderr, got:\n%s", stderr.String())
	}
	rep := decodeJSONReport(t, stdout.Bytes())
	if rep.Killed == 0 || rep.Errors != 0 {
		t.Errorf("expected mutants killed cleanly after fallback, got summary=%+v", rep)
	}
}

func TestIntegration_SubshellTimeoutProcessGroupKill(t *testing.T) {
	batsPath := requireBats(t)
	workDir := t.TempDir()

	// Mutating `false` -> `true` spawns a 30s background sleep inside the test process group.
	script := `#!/usr/bin/env bash
maybe_wait() {
  if false; then
    sleep 30 &
    wait
  fi
  return 0
}
`
	bats := `setup() {
  source ./timeout_target.sh
}

@test "maybe_wait completes quickly" {
  run maybe_wait
  [ "$status" -eq 0 ]
}
`
	if err := os.WriteFile(filepath.Join(workDir, "timeout_target.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "timeout_target.bats"), []byte(bats), 0o644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := MainCommandWithDeps(&Options{
		Target:  "timeout_target.sh",
		Test:    "timeout_target.bats",
		BatsBin: batsPath,
		Jobs:    1,
		Timeout: 400 * time.Millisecond,
		NoCache: true,
		Format:  "json",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{WorkDir: workDir})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("timeout integration run failed: %v\nstderr:\n%s", err, stderr.String())
	}
	if elapsed > 10*time.Second {
		t.Fatalf("expected process-group kill well before 30s sleep, took %v", elapsed)
	}

	rep := decodeJSONReport(t, stdout.Bytes())
	if rep.Timeout == 0 {
		t.Errorf("expected at least 1 TIMEOUT mutant when false->true triggers sleep 30 &, summary=%+v", rep)
	}
	if rep.Errors != 0 {
		t.Errorf("expected 0 infrastructure errors on timeout, summary=%+v", rep)
	}
}

func TestIntegration_MultiTargetWorkspaceRestoreAndXDGCache(t *testing.T) {
	batsPath := requireBats(t)
	workDir := t.TempDir()
	xdgCache := filepath.Join(t.TempDir(), "xdg-cache")
	t.Setenv("XDG_CACHE_HOME", xdgCache)

	for _, dir := range []string{"src", "lib", "test"} {
		if err := os.MkdirAll(filepath.Join(workDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Same-named files in distinct dirs (`src/common.sh` vs `lib/common.sh`) plus literal comma filename.
	files := map[string]string{
		"shared.state": "clean",
		"src/common.sh": `#!/usr/bin/env bash
src_fn() {
  if [ "$1" = "src" ]; then
    return 0
  fi
  return 1
}
`,
		"lib/common.sh": `#!/usr/bin/env bash
lib_fn() {
  if [ "$1" = "lib" ]; then
    return 0
  fi
  return 1
}
`,
		"src/foo,bar.sh": `#!/usr/bin/env bash
comma_fn() {
  return 0
}
`,
		"test/a_lib.bats": `@test "lib_fn works and dirties shared state" {
  [ "$(cat shared.state)" = "clean" ]
  echo "dirty-lib" > shared.state
  source ./lib/common.sh
  run lib_fn "lib"
  [ "$status" -eq 0 ]
  run lib_fn "other"
  [ "$status" -eq 1 ]
}
`,
		"test/z_src.bats": `@test "src_fn works and dirties shared state" {
  [ "$(cat shared.state)" = "clean" ]
  echo "dirty-src" > shared.state
  source ./src/common.sh
  run src_fn "src"
  [ "$status" -eq 0 ]
  run src_fn "other"
  [ "$status" -eq 1 ]
}
`,
		"test/comma.bats": `@test "foo,bar comma_fn works" {
  [ "$(cat shared.state)" = "clean" ]
  source ./src/foo,bar.sh
  run comma_fn
  [ "$status" -eq 0 ]
}
`,
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(rel)), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	runOnce := func() (jsonReportEnvelope, error) {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		opts := &Options{
			Targets: []string{"src/common.sh", "lib/common.sh", "src/foo,bar.sh"},
			BatsBin: batsPath,
			Jobs:    2,
			Format:  "json",
		}
		err := MainCommandWithDeps(opts, &cli.ProcInout{
			Stdin:  strings.NewReader(""),
			Stdout: stdout,
			Stderr: stderr,
		}, Deps{WorkDir: workDir})
		if err != nil {
			t.Logf("stderr:\n%s", stderr.String())
			return jsonReportEnvelope{}, err
		}
		return decodeJSONReport(t, stdout.Bytes()), nil
	}

	first, err := runOnce()
	if err != nil {
		t.Fatalf("first multi-target integration run failed: %v", err)
	}
	if first.TotalMutants == 0 || first.Killed != first.TotalMutants {
		t.Errorf("expected all mutants killed on first run, got summary=%+v", first)
	}

	// Ensure workDir has no .bashtion-cache.json and XDG_CACHE_HOME holds the cache file instead.
	if _, err := os.Stat(filepath.Join(workDir, ".bashtion-cache.json")); !os.IsNotExist(err) {
		t.Errorf("expected clean workDir without .bashtion-cache.json, stat err=%v", err)
	}
	xdgEntries, err := os.ReadDir(filepath.Join(xdgCache, "bashtion"))
	if err != nil || len(xdgEntries) == 0 {
		t.Fatalf("expected cache file under %s, err=%v entries=%d", filepath.Join(xdgCache, "bashtion"), err, len(xdgEntries))
	}

	// Second run should reuse cached outcomes.
	second, err := runOnce()
	if err != nil {
		t.Fatalf("second multi-target integration run failed: %v", err)
	}
	if second.CacheHits != second.TotalMutants || second.CacheHits == 0 {
		t.Errorf("expected full cache hit on second run, got CacheHits=%d Total=%d", second.CacheHits, second.TotalMutants)
	}
}

func TestIntegration_MultiTestFilesForSingleTargetAndStructuredUncovered(t *testing.T) {
	batsPath := requireBats(t)
	workDir := t.TempDir()

	script := `#!/usr/bin/env bash
check_low() {
  if [[ "$1" -ge 10 ]]; then
    return 0
  fi
  return 1
}

check_high() {
  if [[ "$1" -le 100 ]]; then
    return 0
  fi
  return 1
}

dead_path() {
  if [[ "$1" == "never" ]]; then
    return 0
  fi
  return 1
}
`
	batsA := `setup() {
  source ./range.sh
}

@test "check_low boundary" {
  run check_low 10
  [ "$status" -eq 0 ]
  run check_low 9
  [ "$status" -eq 1 ]
}
`
	batsB := `setup() {
  source ./range.sh
}

@test "check_high boundary" {
  run check_high 100
  [ "$status" -eq 0 ]
  run check_high 101
  [ "$status" -eq 1 ]
}
`
	for name, body := range map[string]string{
		"range.sh":  script,
		"low.bats":  batsA,
		"high.bats": batsB,
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	jsonOut := filepath.Join(workDir, "out.json")
	err := MainCommandWithDeps(&Options{
		Target:  "range.sh",
		Test:    "low.bats",
		Tests:   []string{"high.bats"},
		BatsBin: batsPath,
		Jobs:    2,
		NoCache: true,
		Format:  "text",
		JSONOut: jsonOut,
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: stdout,
		Stderr: stderr,
	}, Deps{WorkDir: workDir})
	if err != nil {
		t.Fatalf("multi-test integration run failed: %v\nstderr:\n%s", err, stderr.String())
	}

	rawJSON, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("failed to read --json-out file: %v", err)
	}
	var rep struct {
		CoverageEnabled      bool     `json:"coverage_enabled"`
		TotalMutants         int      `json:"total_mutants"`
		CoveredMutants       int      `json:"covered_mutants"`
		Killed               int      `json:"killed"`
		Survived             int      `json:"survived"`
		Uncovered            int      `json:"uncovered"`
		CoveredMutationScore *float64 `json:"covered_mutation_score"`
		TestFiles            []string `json:"test_files"`
		UncoveredMutants     []struct {
			Key     string `json:"key"`
			Status  string `json:"status"`
			Covered *bool  `json:"covered"`
		} `json:"uncovered_mutants"`
	}
	if err := json.Unmarshal(rawJSON, &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, string(rawJSON))
	}
	if !rep.CoverageEnabled {
		t.Errorf("expected coverage_enabled=true")
	}
	if len(rep.TestFiles) != 2 {
		t.Errorf("expected 2 test_files, got %+v", rep.TestFiles)
	}
	if rep.Survived != 0 {
		t.Errorf("expected 0 covered survived mutants when both low.bats and high.bats run together, got %d", rep.Survived)
	}
	if rep.Uncovered == 0 || len(rep.UncoveredMutants) != rep.Uncovered {
		t.Errorf("expected dead_path mutants in uncovered_mutants, got uncovered=%d len=%d", rep.Uncovered, len(rep.UncoveredMutants))
	}
	if rep.CoveredMutationScore == nil || *rep.CoveredMutationScore != 100.0 {
		t.Errorf("expected covered_mutation_score=100.0, got %v", rep.CoveredMutationScore)
	}
	for _, u := range rep.UncoveredMutants {
		if u.Status != "UNCOVERED" || u.Covered == nil || *u.Covered || len(u.Key) != 16 {
			t.Errorf("unexpected uncovered mutant entry: %+v", u)
		}
	}
}
