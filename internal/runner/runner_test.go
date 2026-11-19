//go:build unix

package runner_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/runner"
)

var fakeKilledErr = sync.OnceValue(func() error {
	return exec.Command("sh", "-c", "exit 1").Run()
})

func TestRun_EvaluatesKilledAndSurvivedMutantsInParallel(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")

	scriptContent := `#!/usr/bin/env bash
check() {
    if [[ "$1" -eq 0 ]] && [[ "$2" -ne 1 ]]; then
        return 0
    fi
    return 1
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# dummy bats file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			return []byte("1..1\nok 1\n"), nil
		}
		mutatedBytes, err := os.ReadFile(filepath.Join(dir, "target.sh"))
		if err != nil {
			return nil, err
		}
		content := string(mutatedBytes)
		if strings.Contains(content, "||") {
			return []byte("not ok 1\n"), fakeKilledErr()
		}
		return []byte("ok 1\n"), nil
	}

	summary, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		Jobs:       4,
		Executor:   executor,
	}, mutants)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if summary.Total != len(mutants) || summary.Killed != 1 || summary.Survived != len(mutants)-1 {
		t.Errorf("unexpected summary counts: %+v", summary)
	}
}

func TestRun_DetectsTimeout(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")

	scriptContent := `#!/usr/bin/env bash
if [[ "$1" -eq 0 ]]; then
    return 0
fi
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# dummy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	summary, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		Jobs:       2,
		Timeout:    20 * time.Millisecond,
		Executor:   executor,
	}, mutants)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if summary.Timeout != len(mutants) || summary.Score() != 100.0 {
		t.Errorf("expected all %d mutants to timeout (100%% score), got %+v", len(mutants), summary)
	}
}

func TestBuildFuncTestFilters(t *testing.T) {
	script := `#!/usr/bin/env bash
func_a() {
    if [[ "$1" -eq 0 ]]; then return 0; fi
    return 1
}
func_b() {
    if [[ "$1" -eq 1 ]]; then return 0; fi
    return 1
}
`
	batsSrc := `#!/usr/bin/env bats
@test "func_a: handles \"quoted\" 0" {
    if true; then
        echo "nested brace"
    fi
    {
        run func_a 0
    }
    [ "$status" -eq 0 ]
  }

@test "func_b: returns 0" { run func_b 1; [ "$status" -eq 0 ]; }
`
	mutants, err := mutator.Generate("script.sh", []byte(script))
	if err != nil {
		t.Fatal(err)
	}
	filters := runner.BuildFuncTestFilters([]byte(batsSrc), mutants)
	if filters["func_a"] == "" || !strings.Contains(filters["func_a"], `func_a: handles "quoted" 0`) {
		t.Errorf("expected filter for func_a with unescaped quote, got %q", filters["func_a"])
	}
	if strings.Contains(filters["func_a"], "func_b") {
		t.Errorf("func_a filter should not include func_b: %q", filters["func_a"])
	}
	if filters["func_b"] == "" || !strings.Contains(filters["func_b"], "func_b: returns 0") {
		t.Errorf("expected filter for one-line func_b test block, got %q", filters["func_b"])
	}
}

func TestBuildFuncTestFilters_SkipsFilterWhenCalledByOtherFunction(t *testing.T) {
	script := `#!/usr/bin/env bash
helper_func() {
    if [[ "$1" -eq 0 ]]; then return 0; fi
    return 1
}
cleanup_func() {
    if [[ -f "/tmp/lock" ]]; then return 0; fi
    return 1
}
main_func() {
    trap 'cleanup_func' EXIT
    helper_func "$1"
    if [[ "$?" -eq 0 ]]; then return 0; fi
    return 1
}
`
	batsSrc := `#!/usr/bin/env bats
@test "helper_func: direct test" {
    run helper_func 0
    [ "$status" -eq 0 ]
}

@test "cleanup_func: direct test" {
    run cleanup_func
    [ "$status" -eq 1 ]
}

@test "main_func: indirect test" {
    run main_func 0
    [ "$status" -eq 0 ]
}
`
	mutants, err := mutator.Generate("script.sh", []byte(script))
	if err != nil {
		t.Fatal(err)
	}

	filters := runner.BuildFuncTestFilters([]byte(batsSrc), mutants, []byte(script))

	// helper_func and cleanup_func (via trap) are called by main_func, so they should NOT be filtered (must run all tests)
	if filter, ok := filters["helper_func"]; ok && filter != "" {
		t.Errorf("expected helper_func to have no filter (run all tests), got %q", filter)
	}
	if filter, ok := filters["cleanup_func"]; ok && filter != "" {
		t.Errorf("expected cleanup_func (invoked via trap) to have no filter (run all tests), got %q", filter)
	}

	// main_func is not called by any other function, so it SHOULD be filtered to only its tests
	if filter, ok := filters["main_func"]; !ok || !strings.Contains(filter, "main_func: indirect test") {
		t.Errorf("expected main_func to be filtered to its test, got %q", filter)
	}
}

func TestRun_CoverageSkipsUncoveredLinesAndRoutesTests(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")

	scriptContent := `#!/usr/bin/env bash
covered_fn() {
    case "$1" in
        ok)
            true
            ;;
        *)
            return 1
            ;;
    esac
}
uncovered_fn() {
    if [[ "$1" == "never" ]]; then
        return 0
    fi
    return 1
}
`
	batsContent := `#!/usr/bin/env bats
@test "covered_fn: checks ok" {
    run covered_fn "ok"
}
@test "other: unrelated test" {
    run true
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte(batsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		t.Fatal(err)
	}

	var evalCalls atomic.Int32
	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := evalCalls.Add(1)
		if n == 1 {
			// Baseline run: actually source the instrumented target.sh in bash so coverage trace is written!
			cmd := runner.DefaultCommandExecutor
			_, err := cmd(ctx, dir, "bash", "-c", `export BATS_TEST_DESCRIPTION="covered_fn: checks ok"; source target.sh; covered_fn "ok"`)
			return []byte("1..2\nok 1\nok 2\n"), err
		}
		// Mutant evaluation call: verify --filter was passed for "covered_fn: checks ok"
		if len(args) < 2 || args[0] != "--filter" || !strings.Contains(args[1], "covered_fn: checks ok") {
			t.Errorf("expected --filter with 'covered_fn: checks ok', got args: %v", args)
		}
		return []byte("not ok 1\n"), fakeKilledErr()
	}

	summary, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		Jobs:       2,
		NoCache:    true,
		Executor:   executor,
	}, mutants)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Mutants in uncovered_fn (and line 8 of covered_fn) should be marked Uncovered without calling executor
	if summary.Uncovered == 0 {
		t.Errorf("expected uncovered mutants to be detected and skipped, got Uncovered=0 (%+v)", summary)
	}
	// Both `true` (line 5) and standalone `;;` (line 6, CASE_CTRL via CoverageLine=5) in covered_fn must be evaluated and killed!
	if summary.Killed != 2 {
		t.Errorf("expected both covered statement and standalone CASE_CTRL in covered_fn to be evaluated and killed (2), got Killed=%d (%+v)", summary.Killed, summary)
	}
	// Total executor calls should be 1 (baseline) + summary.Killed (far fewer than 1 + summary.Total)
	if int(evalCalls.Load()) != 1+summary.Killed {
		t.Errorf("expected %d executor calls (1 baseline + %d covered), got %d", 1+summary.Killed, summary.Killed, evalCalls.Load())
	}
}

func TestRun_IncrementalCacheHitsSkipBatsExecution(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptContent := `#!/usr/bin/env bash
check() {
    if [[ "$1" -eq 0 ]]; then
        return 0
    fi
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		t.Fatal(err)
	}

	var firstRunCalls atomic.Int32
	exec1 := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if firstRunCalls.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		return []byte("not ok\n"), fakeKilledErr()
	}

	// 1st run: populates cache
	sum1, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		CacheFile:  cachePath,
		Executor:   exec1,
	}, mutants)
	if err != nil {
		t.Fatalf("1st Run failed: %v", err)
	}
	if sum1.CacheHits != 0 || sum1.Killed != len(mutants) {
		t.Fatalf("unexpected 1st run summary: %+v", sum1)
	}

	// 2nd run: all mutants should hit cache (only 1 baseline call to Executor)
	var secondRunCalls atomic.Int32
	exec2 := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		secondRunCalls.Add(1)
		return []byte("ok\n"), nil
	}

	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		CacheFile:  cachePath,
		Executor:   exec2,
	}, mutants)
	if err != nil {
		t.Fatalf("2nd Run failed: %v", err)
	}
	if sum2.CacheHits != len(mutants) || sum2.Killed != len(mutants) {
		t.Errorf("expected all %d mutants to hit cache and be KILLED, got %+v", len(mutants), sum2)
	}
	if secondRunCalls.Load() != 1 {
		t.Errorf("expected only 1 executor call (baseline) on cached run, got %d", secondRunCalls.Load())
	}
}

func TestRun_SandboxIsolatesHostFilesAndPreservesBinAndSymlinks(t *testing.T) {
	workDir := t.TempDir()
	extDir := t.TempDir()
	binDir := filepath.Join(workDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	helperPath := filepath.Join(binDir, "helper.sh")
	origHelper := "#!/usr/bin/env bash\necho original\n"
	if err := os.WriteFile(helperPath, []byte(origHelper), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(workDir, "helper_link.sh")
	if err := os.Symlink(filepath.Join("bin", "helper.sh"), symlinkPath); err != nil {
		t.Fatal(err)
	}

	// External symlink pointing outside workDir must be materialized as a copy so host file is safe
	extHostFile := filepath.Join(extDir, "external.sh")
	if err := os.WriteFile(extHostFile, []byte("external original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extHostFile, filepath.Join(workDir, "external_link.sh")); err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	scriptContent := "#!/usr/bin/env bash\nif [[ \"$1\" -eq 0 ]]; then return 0; fi\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil {
		t.Fatal(err)
	}

	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		// Verify bin/helper.sh exists and is restored to origHelper before EVERY mutant on the same worker
		sandboxHelper := filepath.Join(dir, "bin", "helper.sh")
		helperData, err := os.ReadFile(sandboxHelper)
		if err != nil {
			t.Errorf("expected bin/helper.sh in sandbox %s: %v", dir, err)
			return nil, err
		}
		if string(helperData) != origHelper {
			t.Errorf("worker sandbox was not restored between mutants! got %q, want %q", string(helperData), origHelper)
		}
		if _, err := os.Lstat(filepath.Join(dir, "helper_link.sh")); err != nil {
			t.Errorf("expected symlink helper_link.sh in sandbox %s: %v", dir, err)
			return nil, err
		}
		// Verify leftover file created by previous mutant was cleaned up
		leftoverFile := filepath.Join(dir, "leftover_artifact.tmp")
		if _, err := os.Stat(leftoverFile); err == nil {
			t.Errorf("worker sandbox did not clean up newly created file %s between mutants", leftoverFile)
		}
		// Corrupt both bin/helper.sh and external_link.sh in-place inside worker sandbox and create a new file
		_ = os.WriteFile(sandboxHelper, []byte("corrupted by test\n"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "external_link.sh"), []byte("corrupted external\n"), 0o644)
		_ = os.WriteFile(leftoverFile, []byte("leftover\n"), 0o644)
		return []byte("not ok\n"), fakeKilledErr()
	}

	var calls atomic.Int32
	wrappedExec := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		return executor(ctx, dir, name, args...)
	}

	// Use Jobs: 1 so all mutants execute sequentially on worker-0, proving inter-mutant restoration
	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		Jobs:       1,
		NoCache:    true,
		Executor:   wrappedExec,
	}, mutants); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Host file in workDir/bin/helper.sh and external host file must remain completely untouched
	hostBytes, err := os.ReadFile(helperPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hostBytes) != origHelper {
		t.Fatalf("host file was corrupted by worker sandbox! got %q, want %q", string(hostBytes), origHelper)
	}
	extBytes, err := os.ReadFile(extHostFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(extBytes) != "external original\n" {
		t.Fatalf("external symlinked host file was corrupted! got %q", string(extBytes))
	}
}

func TestRun_CacheInvalidatedWhenCalleeFunctionChanges(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptV1 := `#!/usr/bin/env bash
callee() {
    if [[ "$1" == "v1" ]]; then return 0; fi
    return 1
}
caller() {
    callee "$1"
    if [[ "$?" -eq 0 ]]; then return 0; fi
    return 1
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptV1), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutantsV1, err := mutator.Generate("target.sh", []byte(scriptV1))
	if err != nil {
		t.Fatal(err)
	}

	exec1 := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok\n"), nil
	}
	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        exec1,
	}, mutantsV1); err != nil {
		t.Fatal(err)
	}

	// Modify callee() while keeping caller() identical
	scriptV2 := strings.Replace(scriptV1, `"v1"`, `"v2"`, 1)
	if err := os.WriteFile(scriptPath, []byte(scriptV2), 0o755); err != nil {
		t.Fatal(err)
	}
	mutantsV2, err := mutator.Generate("target.sh", []byte(scriptV2))
	if err != nil {
		t.Fatal(err)
	}

	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        exec1,
	}, mutantsV2)
	if err != nil {
		t.Fatal(err)
	}

	// Because caller() invokes callee(), changing callee() must invalidate caller()'s cache too
	if sum2.CacheHits != 0 {
		t.Errorf("expected 0 cache hits when callee changed, got %d", sum2.CacheHits)
	}

	// Verify cache invalidation when a non-mutated helper function or global variable changes
	scriptV3 := `#!/usr/bin/env bash
GLOBAL_PREFIX="v1"
pure_helper() {
    echo "${GLOBAL_PREFIX}"
}
caller() {
    pure_helper
    if [[ "$1" -eq 0 ]]; then return 0; fi
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptV3), 0o755); err != nil {
		t.Fatal(err)
	}
	mutantsV3, err := mutator.Generate("target.sh", []byte(scriptV3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        exec1,
	}, mutantsV3); err != nil {
		t.Fatal(err)
	}

	// Change GLOBAL_PREFIX (top-level statement outside any function) -> must invalidate cache
	scriptV4 := strings.Replace(scriptV3, `GLOBAL_PREFIX="v1"`, `GLOBAL_PREFIX="v2"`, 1)
	if err := os.WriteFile(scriptPath, []byte(scriptV4), 0o755); err != nil {
		t.Fatal(err)
	}
	mutantsV4, err := mutator.Generate("target.sh", []byte(scriptV4))
	if err != nil {
		t.Fatal(err)
	}
	sum4, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        exec1,
	}, mutantsV4)
	if err != nil {
		t.Fatal(err)
	}
	if sum4.CacheHits != 0 {
		t.Errorf("expected 0 cache hits when top-level global variable changed, got %d", sum4.CacheHits)
	}
}

func TestRun_BatsArgsPassedToExecutor(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")

	scriptSrc := `#!/usr/bin/env bash
greet() {
    if [[ "$1" == "hello" ]]; then
        echo "world"
    fi
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptSrc))
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var capturedArgs [][]string

	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		mu.Lock()
		capturedArgs = append(capturedArgs, args)
		mu.Unlock()
		return []byte("ok"), nil
	}

	cfg := runner.Config{
		WorkDir:           workDir,
		TargetFile:        "target.sh",
		TestFile:          "target.bats",
		BatsArgs:          []string{"--timing", "--filter-tags", "unit"},
		DisableCoverage:   true,
		DisableTestFilter: true,
		Executor:          executor,
	}

	if _, err := runner.Run(context.Background(), cfg, mutants); err != nil {
		t.Fatalf("runner.Run() error: %v", err)
	}

	if len(capturedArgs) == 0 {
		t.Fatal("expected at least baseline run")
	}

	for i, args := range capturedArgs {
		if len(args) < 3 {
			t.Errorf("call #%d: expected at least 3 args, got %v", i, args)
			continue
		}
		if args[0] != "--timing" || args[1] != "--filter-tags" || args[2] != "unit" {
			t.Errorf("call #%d: expected BatsArgs prefix [--timing, --filter-tags, unit], got %v", i, args)
		}
	}
}

func TestRun_ContextCancelDoesNotPoisonResultsOrCache(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptSrc := `#!/usr/bin/env bash
check() {
    if [[ "$1" -eq 0 ]] && [[ "$2" -ne 1 ]]; then
        return 0
    fi
    return 1
}
`
	if err := os.WriteFile(scriptPath, []byte(scriptSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptSrc))
	if err != nil {
		t.Fatal(err)
	}
	if len(mutants) < 3 {
		t.Fatalf("expected at least 3 mutants, got %d", len(mutants))
	}

	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	execCanceled := func(execCtx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		if n == 1 {
			// Baseline passes
			return []byte("ok\n"), nil
		}
		if n == 2 {
			// 1st mutant survives
			return []byte("ok\n"), nil
		}
		// 2nd mutant is interrupted by SIGINT/cancel while running
		cancel()
		return []byte("killed by signal"), context.Canceled
	}

	sum, runErr := runner.Run(ctx, runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        execCanceled,
	}, mutants)
	if runErr == nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got sum=%+v, err=%v", sum, runErr)
	}

	if _, err := os.Stat(cachePath); err == nil {
		t.Errorf("cache file %s should NOT be saved on interrupted run", cachePath)
	}

	// Subsequent normal run where all mutants survive must report 0% (no poisoned cache)
	execHealthy := func(execCtx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte("ok\n"), nil
	}
	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            2,
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        execHealthy,
	}, mutants)
	if err != nil {
		t.Fatalf("2nd run failed: %v", err)
	}
	if sum2.Killed != 0 || sum2.CacheHits != 0 || sum2.Score() != 0.0 {
		t.Errorf("expected 0 killed, 0 cache hits, 0%% score after canceled run, got %+v", sum2)
	}
}

func TestRun_InfraFailureClassifiedAsErrorAndFails(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptSrc := "#!/usr/bin/env bash\nif [[ \"$1\" -eq 0 ]]; then return 0; fi\n"
	if err := os.WriteFile(scriptPath, []byte(scriptSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptSrc))
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	execInfraErr := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		// Non-*exec.ExitError simulates missing binary / exec start failure / WaitDelay
		return nil, errors.New("exec: bats: executable file not found in $PATH")
	}

	sum, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        execInfraErr,
	}, mutants)
	if err == nil {
		t.Fatal("expected runner.Run to fail when infrastructure errors occur")
	}
	if sum == nil {
		t.Fatal("expected non-nil Summary alongside error")
	}
	if sum.Errors != len(mutants) || sum.Killed != 0 {
		t.Errorf("expected Errors=%d and Killed=0, got %+v", len(mutants), sum)
	}
	for _, r := range sum.Results {
		if r.Status != runner.StatusError {
			t.Errorf("expected StatusError, got %s", r.Status)
		}
	}
	if _, statErr := os.Stat(cachePath); statErr == nil {
		t.Errorf("cache file should not persist ERROR results")
	}
}

func TestRun_NoCoverageAndEmptyMutantsRunBaselineInSandbox(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")

	// Place a helper in node_modules (skipped by sandbox manifest)
	nmDir := filepath.Join(workDir, "node_modules")
	if err := os.MkdirAll(nmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "helper.sh"), []byte("echo ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptSrc := "#!/usr/bin/env bash\nif [[ \"$1\" -eq 0 ]]; then return 0; fi\n"
	if err := os.WriteFile(scriptPath, []byte(scriptSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptSrc))
	if err != nil {
		t.Fatal(err)
	}

	// 1. --no-coverage must still execute baseline inside worker-0 sandbox (not real workDir)
	var baseDirs []string
	execCheckSandbox := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		baseDirs = append(baseDirs, dir)
		if _, err := os.Stat(filepath.Join(dir, "node_modules", "helper.sh")); err == nil {
			return []byte("ok\n"), nil
		}
		return []byte("missing helper"), fakeKilledErr()
	}

	_, err = runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		NoCache:         true,
		Executor:        execCheckSandbox,
	}, mutants)
	if err == nil || !strings.Contains(err.Error(), "baseline test suite failed") {
		t.Fatalf("expected baseline failure due to missing node_modules in sandbox, got: %v", err)
	}
	if len(baseDirs) != 1 || baseDirs[0] == workDir {
		t.Errorf("expected baseline to run inside worker-0 sandbox, got dir=%v (workDir=%s)", baseDirs, workDir)
	}

	// 2. Empty mutants must also run baseline inside worker-0 sandbox
	var emptyBaseDir string
	execEmpty := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		emptyBaseDir = dir
		return []byte("ok\n"), nil
	}
	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		Executor:   execEmpty,
	}, nil); err != nil {
		t.Fatalf("empty mutants Run failed: %v", err)
	}
	if emptyBaseDir == "" || emptyBaseDir == workDir {
		t.Errorf("expected empty-mutants baseline to run in sandbox, got %q (workDir=%q)", emptyBaseDir, workDir)
	}
}

func TestRun_TimeoutIsNotCached(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptContent := "#!/usr/bin/env bash\nif true; then\n    return 0\nfi\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil || len(mutants) == 0 {
		t.Fatalf("Generate failed: %v (len=%d)", err, len(mutants))
	}

	var calls1 atomic.Int32
	execTimeout := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls1.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	sum1, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		Timeout:         20 * time.Millisecond,
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        execTimeout,
	}, mutants)
	if err != nil {
		t.Fatalf("1st Run failed: %v", err)
	}
	if sum1.Timeout != len(mutants) {
		t.Fatalf("expected all %d mutants to time out, got %+v", len(mutants), sum1)
	}

	// Second run with fast executor must NOT hit cache because TIMEOUT is never cached.
	var calls2 atomic.Int32
	execKill := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls2.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		return []byte("not ok\n"), fakeKilledErr()
	}

	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		Timeout:         20 * time.Millisecond,
		DisableCoverage: true,
		CacheFile:       cachePath,
		Executor:        execKill,
	}, mutants)
	if err != nil {
		t.Fatalf("2nd Run failed: %v", err)
	}
	if sum2.CacheHits != 0 || sum2.Killed != len(mutants) {
		t.Errorf("expected 0 cache hits and %d KILLED after timeout, got %+v", len(mutants), sum2)
	}
}

func TestRun_CacheInvalidatedByHelperToolVersionAndMode(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	helperPath := filepath.Join(workDir, "helper.bash")
	cachePath := filepath.Join(workDir, ".bashtion-cache.json")

	scriptContent := "#!/usr/bin/env bash\ncheck() {\n    if [[ \"$1\" -eq 0 ]]; then\n        return 0\n    fi\n}\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helperPath, []byte("# helper v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil || len(mutants) == 0 {
		t.Fatalf("Generate failed: %v", err)
	}

	var calls atomic.Int32
	execKill := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte("ok\n"), nil
		}
		return []byte("not ok\n"), fakeKilledErr()
	}

	baseCfg := runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		DisableCoverage: true,
		CacheFile:       cachePath,
		ToolVersion:     "v1.0.0",
		Executor:        execKill,
	}

	// Populate cache
	calls.Store(0)
	if _, err := runner.Run(context.Background(), baseCfg, mutants); err != nil {
		t.Fatalf("baseline populate failed: %v", err)
	}

	// Sanity check: identical run hits cache
	calls.Store(0)
	hitSum, err := runner.Run(context.Background(), baseCfg, mutants)
	if err != nil {
		t.Fatalf("cache hit run failed: %v", err)
	}
	if hitSum.CacheHits != len(mutants) {
		t.Fatalf("expected full cache hit on identical run, got %d/%d", hitSum.CacheHits, len(mutants))
	}

	// 1. Modifying auxiliary workspace helper invalidates cache
	if err := os.WriteFile(helperPath, []byte("# helper v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	helperSum, err := runner.Run(context.Background(), baseCfg, mutants)
	if err != nil {
		t.Fatalf("helper-modified run failed: %v", err)
	}
	if helperSum.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after modifying workspace helper, got %d", helperSum.CacheHits)
	}

	// 2. Changing ToolVersion invalidates cache
	verCfg := baseCfg
	verCfg.ToolVersion = "v1.1.0"
	calls.Store(0)
	verSum, err := runner.Run(context.Background(), verCfg, mutants)
	if err != nil {
		t.Fatalf("version-changed run failed: %v", err)
	}
	if verSum.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after changing ToolVersion, got %d", verSum.CacheHits)
	}

	// 3. Changing filter mode or bats args invalidates cache
	modeCfg := verCfg
	modeCfg.DisableTestFilter = true
	calls.Store(0)
	modeSum, err := runner.Run(context.Background(), modeCfg, mutants)
	if err != nil {
		t.Fatalf("filter-mode-changed run failed: %v", err)
	}
	if modeSum.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after toggling DisableTestFilter, got %d", modeSum.CacheHits)
	}

	argsCfg := modeCfg
	argsCfg.BatsArgs = []string{"--tap"}
	calls.Store(0)
	argsSum, err := runner.Run(context.Background(), argsCfg, mutants)
	if err != nil {
		t.Fatalf("bats-args-changed run failed: %v", err)
	}
	if argsSum.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after changing BatsArgs, got %d", argsSum.CacheHits)
	}
}

func TestDefaultCommandExecutor_KillsBackgroundSubshellOnNormalExit(t *testing.T) {
	dir := t.TempDir()
	out, err := runner.DefaultCommandExecutor(
		context.Background(),
		dir,
		"sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 & echo $!; exit 0",
	)
	if err != nil {
		t.Fatalf("DefaultCommandExecutor failed: %v (out=%s)", err, string(out))
	}
	pidStr := strings.TrimSpace(string(out))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		t.Fatalf("expected valid child PID in output, got %q (err=%v)", string(out), err)
	}

	// Poll briefly to allow kernel signal delivery after pgid SIGKILL.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		if killErr := syscall.Kill(pid, 0); errors.Is(killErr, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("background child process %d survived DefaultCommandExecutor exit", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRestoreWorkerFiles_SkipsUnmodifiedAndRestoresModifiedFiles(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	untouchedPath := filepath.Join(workDir, "untouched.txt")
	mutatedFixturePath := filepath.Join(workDir, "fixture.txt")

	scriptContent := "#!/usr/bin/env bash\ncheck() {\n    if [[ \"$1\" -eq 0 ]] && [[ \"$1\" -lt 10 ]]; then\n        return 0\n    fi\n}\n"
	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untouchedPath, []byte("stable data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mutatedFixturePath, []byte("clean fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("target.sh", []byte(scriptContent))
	if err != nil || len(mutants) < 2 {
		t.Fatalf("Generate failed: %v (len=%d)", err, len(mutants))
	}

	var calls atomic.Int32
	var mtimes []time.Time
	var mu sync.Mutex
	var sawCorruptedFixture atomic.Bool

	execCheck := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		info, err := os.Stat(filepath.Join(dir, "untouched.txt"))
		if err != nil {
			return nil, err
		}
		mu.Lock()
		mtimes = append(mtimes, info.ModTime())
		mu.Unlock()

		fxBytes, err := os.ReadFile(filepath.Join(dir, "fixture.txt"))
		if err != nil {
			return nil, err
		}
		if string(fxBytes) != "clean fixture\n" {
			sawCorruptedFixture.Store(true)
		}

		if n == 1 {
			return []byte("ok\n"), nil
		}
		// Ensure mtime resolution can detect a rewrite if one occurred, and dirty fixture.txt.
		time.Sleep(15 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "fixture.txt"), []byte("dirty fixture\n"), 0o644)
		return []byte("not ok\n"), fakeKilledErr()
	}

	sum, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		Jobs:            1,
		DisableCoverage: true,
		NoCache:         true,
		Executor:        execCheck,
	}, mutants[:2])
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if sum.Killed != 2 {
		t.Fatalf("expected 2 killed mutants, got %+v", sum)
	}
	if sawCorruptedFixture.Load() {
		t.Errorf("fixture.txt was not restored before subsequent mutant execution")
	}
	if len(mtimes) < 3 {
		t.Fatalf("expected at least 3 mtime samples, got %d", len(mtimes))
	}
	for i := 1; i < len(mtimes); i++ {
		if !mtimes[i].Equal(mtimes[0]) {
			t.Errorf("untouched.txt was redundantly rewritten: initial mtime=%v, run %d mtime=%v", mtimes[0], i, mtimes[i])
		}
	}
}

func TestRun_WorkspaceBoundsAndExplicitWorkspaceAndGitFallback(t *testing.T) {
	// 1. File count budget and byte size cap enforcement
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\nreturn 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(workDir, "extra_"+strconv.Itoa(i)+".txt"), []byte(strings.Repeat("a", 64)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, err := runner.Run(context.Background(), runner.Config{
		WorkDir:           workDir,
		TargetFile:        "target.sh",
		TestFile:          "target.bats",
		MaxWorkspaceFiles: 3,
		NoCache:           true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "file count exceeds limit") {
		t.Errorf("expected MaxWorkspaceFiles error, got: %v", err)
	}

	_, err = runner.Run(context.Background(), runner.Config{
		WorkDir:           workDir,
		TargetFile:        "target.sh",
		TestFile:          "target.bats",
		MaxWorkspaceBytes: 100,
		NoCache:           true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "size exceeds limit") {
		t.Errorf("expected MaxWorkspaceBytes error, got: %v", err)
	}

	// 2. Non-git parent escape without --workspace must fail (never fall back to arbitrary common ancestor)
	subDir := filepath.Join(workDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), runner.Config{
		WorkDir:    subDir,
		TargetFile: filepath.Join("..", "target.sh"),
		TestFile:   filepath.Join("..", "target.bats"),
		NoCache:    true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "--workspace") {
		t.Errorf("expected workspace boundary error when running outside git root, got: %v", err)
	}

	// 3. Explicit WorkspaceDir resolves parent target/test cleanly
	sumWS, err := runner.Run(context.Background(), runner.Config{
		WorkDir:      subDir,
		WorkspaceDir: "..",
		TargetFile:   filepath.Join("..", "target.sh"),
		TestFile:     filepath.Join("..", "target.bats"),
		NoCache:      true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err != nil || sumWS == nil {
		t.Fatalf("expected explicit --workspace to succeed, got: %v", err)
	}

	// 4. Shared Git repository falls back to git toplevel cleanly from subDir
	if out, gitErr := exec.Command("git", "-C", workDir, "init").CombinedOutput(); gitErr != nil {
		t.Fatalf("git init failed: %v (%s)", gitErr, out)
	}
	sumGit, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    subDir,
		TargetFile: filepath.Join("..", "target.sh"),
		TestFile:   filepath.Join("..", "target.bats"),
		NoCache:    true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err != nil || sumGit == nil {
		t.Fatalf("expected shared git root fallback to succeed, got: %v", err)
	}
}

func TestRun_WorkspaceIncludeExcludeAndWarnings(t *testing.T) {
	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "target.sh")
	testPath := filepath.Join(workDir, "target.bats")
	batsSupportDir := filepath.Join(workDir, "node_modules", "bats-support")
	otherPkgDir := filepath.Join(workDir, "node_modules", "other-pkg")
	fixturesDir := filepath.Join(workDir, "fixtures")
	gitDir := filepath.Join(workDir, ".git")

	for _, d := range []string{batsSupportDir, otherPkgDir, fixturesDir, gitDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\nreturn 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("# bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batsSupportDir, "load.bash"), []byte("# helper\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherPkgDir, "index.js"), []byte("// ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixturesDir, "skip.txt"), []byte("exclude me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/outside/path", filepath.Join(workDir, "broken.sh")); err != nil {
		t.Fatal(err)
	}

	// Default run emits diagnostic warnings for default-skipped dirs (.git, node_modules) and broken symlink.
	sumDefault, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "target.sh",
		TestFile:   "target.bats",
		NoCache:    true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			return []byte("ok\n"), nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("default Run failed: %v", err)
	}
	joinedWarn := strings.Join(sumDefault.Warnings, "\n")
	if !strings.Contains(joinedWarn, ".git") || !strings.Contains(joinedWarn, "node_modules") {
		t.Errorf("expected warnings for skipped .git/node_modules, got: %v", sumDefault.Warnings)
	}
	if !strings.Contains(joinedWarn, "broken.sh") {
		t.Errorf("expected warning for dropped broken symlink, got: %v", sumDefault.Warnings)
	}

	// Include only node_modules/bats-support and exclude fixtures/skip.txt.
	var hasSupport, hasOtherPkg, hasSkippedFixture bool
	_, err = runner.Run(context.Background(), runner.Config{
		WorkDir:           workDir,
		TargetFile:        "target.sh",
		TestFile:          "target.bats",
		WorkspaceIncludes: []string{"node_modules/bats-support"},
		WorkspaceExcludes: []string{"fixtures/skip.txt"},
		NoCache:           true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			if _, statErr := os.Stat(filepath.Join(dir, "node_modules", "bats-support", "load.bash")); statErr == nil {
				hasSupport = true
			}
			if _, statErr := os.Stat(filepath.Join(dir, "node_modules", "other-pkg", "index.js")); statErr == nil {
				hasOtherPkg = true
			}
			if _, statErr := os.Stat(filepath.Join(dir, "fixtures", "skip.txt")); statErr == nil {
				hasSkippedFixture = true
			}
			return []byte("ok\n"), nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("include/exclude Run failed: %v", err)
	}
	if !hasSupport {
		t.Errorf("expected included node_modules/bats-support/load.bash inside sandbox")
	}
	if hasOtherPkg {
		t.Errorf("expected unincluded node_modules/other-pkg/index.js to remain skipped")
	}
	if hasSkippedFixture {
		t.Errorf("expected excluded fixtures/skip.txt to be omitted from sandbox")
	}
}

func TestBuildFuncTestFilters_WordBoundaryAndTopLevelCalls(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "app.sh")
	testPath := filepath.Join(tmpDir, "app.bats")

	// `log` is a substring of `login`; word-boundary matching must NOT pair `log` with "login validation".
	// `leaf` is called from top-level code (outside any FuncDecl), so a mutant inside `leaf` should
	// map through the top-level / test mapping when present.
	script := `#!/usr/bin/env bash
log() {
  echo "$1"
}

leaf() {
  echo "leaf"
}

leaf
`
	bats := `@test "login validation" {
  run login
}

@test "audit log output" {
  run log "ok"
}

@test "runs script top level" {
  run bash app.sh
}
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte(bats), 0o644); err != nil {
		t.Fatal(err)
	}

	mLog := mutator.Mutant{
		ID:       1,
		FilePath: "app.sh",
		FuncName: "log",
		Line:     3,
	}
	mLeaf := mutator.Mutant{
		ID:       2,
		FilePath: "app.sh",
		FuncName: "leaf",
		Line:     7,
	}
	filters := runner.BuildFuncTestFilters([]byte(bats), []mutator.Mutant{mLog, mLeaf}, []byte(script))
	filter, ok := filters["log"]
	if !ok {
		t.Fatal("expected filter for function log")
	}
	if strings.Contains(filter, "login validation") {
		t.Errorf("filter for log should not match 'login validation', got %q", filter)
	}
	if !strings.Contains(filter, "audit log output") {
		t.Errorf("filter for log should match 'audit log output', got %q", filter)
	}
	// leaf is invoked from script top level (and referenced in test), verify edge analysis doesn't skip top-level calls.
	if leafFilter, ok := filters["leaf"]; ok && !strings.Contains(leafFilter, "runs script top level") && !strings.Contains(leafFilter, "leaf") {
		t.Errorf("unexpected leaf filter: %q", leafFilter)
	}
}

func TestRun_FilteredSurvivedTriggersUnfilteredFallbackKill(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "app.sh")
	testPath := filepath.Join(tmpDir, "app.bats")

	script := `#!/usr/bin/env bash
greet() {
  if [ "$1" = "hi" ]; then
    return 0
  fi
  return 1
}

farewell() {
  return 0
}
`
	bats := `@test "greet test" {
  run greet "hi"
}

@test "farewell test" {
  run farewell
}
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte(bats), 0o644); err != nil {
		t.Fatal(err)
	}
	mutants, err := mutator.Generate("app.sh", []byte(script))
	if err != nil || len(mutants) == 0 {
		t.Fatalf("mutator.Generate failed: err=%v len=%d", err, len(mutants))
	}
	// Keep one mutant from `greet` and one from `farewell` so BuildFuncTestFilters activates (len(uniqueFuncs) > 1).
	var picked []mutator.Mutant
	seenFunc := make(map[string]bool)
	for _, m := range mutants {
		if m.FuncName != "" && !seenFunc[m.FuncName] {
			seenFunc[m.FuncName] = true
			picked = append(picked, m)
		}
	}
	if len(picked) < 2 {
		t.Fatalf("expected mutants in at least 2 functions, got %d", len(picked))
	}

	var calls atomic.Int32
	var sawFiltered, sawUnfiltered atomic.Bool
	summary, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         tmpDir,
		TargetFile:      "app.sh",
		TestFile:        "app.bats",
		DisableCoverage: true,
		NoCache:         true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			n := calls.Add(1)
			if n == 1 {
				// Uninstrumented baseline succeeds.
				return []byte("ok\n"), nil
			}
			hasFilter := false
			for _, a := range args {
				if a == "--filter" {
					hasFilter = true
					break
				}
			}
			if hasFilter {
				sawFiltered.Store(true)
				// Simulate filtered test surviving the mutant.
				return []byte("ok 1\n"), nil
			}
			// Simulate unfiltered full suite catching/killing the mutant.
			sawUnfiltered.Store(true)
			return []byte("not ok 2\n"), fakeKilledErr()
		},
	}, picked)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if !sawFiltered.Load() || !sawUnfiltered.Load() {
		t.Fatalf("expected filtered run followed by unfiltered fallback, sawFiltered=%v sawUnfiltered=%v", sawFiltered.Load(), sawUnfiltered.Load())
	}
	if summary.Killed != len(picked) || summary.Survived != 0 {
		t.Errorf("expected Killed=%d Survived=0 after unfiltered fallback, got Killed=%d Survived=%d", len(picked), summary.Killed, summary.Survived)
	}
}

func TestRun_InstrumentedBaselineFailureFallsBackToUninstrumented(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "app.sh")
	testPath := filepath.Join(tmpDir, "app.bats")

	orig := "#!/usr/bin/env bash\nfoo() {\n  return 0\n}\n"
	if err := os.WriteFile(scriptPath, []byte(orig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("@test \"foo\" { run foo; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("app.sh", []byte(orig))
	if err != nil || len(mutants) == 0 {
		t.Fatalf("mutator.Generate failed: err=%v len=%d", err, len(mutants))
	}
	mutants = mutants[:1]

	summary, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    tmpDir,
		TargetFile: "app.sh",
		TestFile:   "app.bats",
		NoCache:    true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			data, readErr := os.ReadFile(filepath.Join(dir, "app.sh"))
			if readErr != nil {
				return nil, readErr
			}
			// Fail baseline when coverage preamble is injected, succeed on plain original/mutated.
			if strings.Contains(string(data), "__bashtion_cov_file") {
				return []byte("not ok instrumented fail\n"), fakeKilledErr()
			}
			if strings.Contains(string(data), "return 0") {
				return []byte("ok 1\n"), nil
			}
			return []byte("not ok killed\n"), fakeKilledErr()
		},
	}, mutants)
	if err != nil {
		t.Fatalf("expected fallback to succeed without error, got: %v", err)
	}
	if summary.Killed != 1 {
		t.Errorf("expected Killed=1 after uninstrumented baseline fallback, got %d", summary.Killed)
	}
	joined := strings.Join(summary.Warnings, "\n")
	if !strings.Contains(joined, "instrumented baseline failed; fell back to uninstrumented baseline") {
		t.Errorf("expected fallback warning in summary.Warnings, got: %v", summary.Warnings)
	}
}

func TestRun_NoCoverageBaselineReceivesTargetInSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "target.sh")
	testPath := filepath.Join(tmpDir, "target.bats")

	if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\necho target\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte("@test \"t\" { run bash target.sh; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var baselineSawTarget atomic.Bool
	_, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         tmpDir,
		TargetFile:      "target.sh",
		TestFile:        "target.bats",
		DisableCoverage: true,
		NoCache:         true,
		Executor: func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			data, readErr := os.ReadFile(filepath.Join(dir, "target.sh"))
			if readErr == nil && strings.Contains(string(data), "echo target") {
				baselineSawTarget.Store(true)
			}
			return []byte("ok\n"), nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run --no-coverage baseline failed: %v", err)
	}
	if !baselineSawTarget.Load() {
		t.Fatal("expected uninstrumented baseline worker-0 sandbox to contain target.sh")
	}
}

func TestWorkspaceSession_ReusesSandboxAndRestoresModifiedFilesBetweenTargets(t *testing.T) {
	workDir := t.TempDir()
	srcA := "#!/usr/bin/env bash\na() {\n  return 0\n}\n"
	srcB := "#!/usr/bin/env bash\nb() {\n  return 0\n}\n"
	for name, content := range map[string]string{
		"a.sh":       srcA,
		"b.sh":       srcB,
		"shared.txt": "clean-state",
		"a.bats":     "@test \"a\" { true; }\n",
		"b.bats":     "@test \"b\" { true; }\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	ws, err := runner.NewWorkspaceSession(context.Background(), runner.WorkspaceConfig{
		WorkDir:     workDir,
		TargetFiles: []string{"a.sh", "b.sh"},
		TestFiles:   []string{"a.bats", "b.bats"},
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSession failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	mutsA, err := mutator.Generate("a.sh", []byte(srcA))
	if err != nil || len(mutsA) == 0 {
		t.Fatalf("mutator.Generate a.sh failed: %v", err)
	}
	mutsB, err := mutator.Generate("b.sh", []byte(srcB))
	if err != nil || len(mutsB) == 0 {
		t.Fatalf("mutator.Generate b.sh failed: %v", err)
	}

	var firstWorkerDir, secondTargetSharedContent string
	execMutatingShared := func(target string) runner.CommandExecutor {
		return func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
			switch target {
			case "a.sh":
				if firstWorkerDir == "" {
					firstWorkerDir = dir
				}
				// Dirty a shared workspace file during target A's execution.
				_ = os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("dirty-from-a"), 0o644)
			case "b.sh":
				if secondTargetSharedContent == "" {
					b, _ := os.ReadFile(filepath.Join(dir, "shared.txt"))
					secondTargetSharedContent = string(b)
				}
				if dir != firstWorkerDir {
					t.Errorf("expected WorkspaceSession to reuse worker dir %q, got %q", firstWorkerDir, dir)
				}
			}
			return []byte("ok 1\n"), nil
		}
	}

	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		Workspace:       ws,
		TargetFile:      "a.sh",
		TestFile:        "a.bats",
		Jobs:            1,
		DisableCoverage: true,
		NoCache:         true,
		Executor:        execMutatingShared("a.sh"),
	}, mutsA[:1]); err != nil {
		t.Fatalf("target A failed: %v", err)
	}

	if _, err := runner.Run(context.Background(), runner.Config{
		WorkDir:         workDir,
		Workspace:       ws,
		TargetFile:      "b.sh",
		TestFile:        "b.bats",
		Jobs:            1,
		DisableCoverage: true,
		NoCache:         true,
		Executor:        execMutatingShared("b.sh"),
	}, mutsB[:1]); err != nil {
		t.Fatalf("target B failed: %v", err)
	}

	if secondTargetSharedContent != "clean-state" {
		t.Errorf("expected shared.txt to be restored to clean-state between targets, got %q", secondTargetSharedContent)
	}
}

func TestBuildFuncTestFilters_BatsBlockSyntaxEdgeCases(t *testing.T) {
	target := []byte(`#!/usr/bin/env bash
fn_tab() { return 0; }
fn_nextbrace() { return 0; }
fn_heredoc() { return 0; }
fn_quote() { return 0; }
`)
	mutants, err := mutator.Generate("target.sh", target)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	batsSrc := []byte(`#!/usr/bin/env bats
@test	"tab test" {
    fn_tab
}

@test "brace on next line"
{
    fn_nextbrace
}

@test "heredoc with braces" {
    cat <<'EOF'
}
@test "fake" {
EOF
    fn_heredoc
}

@test "multiline double quote with brace" {
    msg="hello
}
world"
    fn_quote
}
`)
	filters := runner.BuildFuncTestFilters(batsSrc, mutants, target)
	for fn, wantTitle := range map[string]string{
		"fn_tab":       "tab test",
		"fn_nextbrace": "brace on next line",
		"fn_heredoc":   "heredoc with braces",
		"fn_quote":     "multiline double quote with brace",
	} {
		got := filters[fn]
		if !strings.Contains(got, wantTitle) {
			t.Errorf("BuildFuncTestFilters[%q] = %q, want substring %q", fn, got, wantTitle)
		}
	}
}

func TestRun_BatsArgsFilterChmod000ExcludedTargetsAndEnvCache(t *testing.T) {
	workDir := t.TempDir()
	for _, d := range []string{"vendor/lib", "vendor/pkg"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targetRel := filepath.Join("vendor", "lib", "mod.sh")
	testRel := filepath.Join("vendor", "lib", "mod.bats")
	targetSrc := "#!/usr/bin/env bash\nhelper() {\n    return 0\n}\n"
	if err := os.WriteFile(filepath.Join(workDir, targetRel), []byte(targetSrc), 0o755); err != nil {
		t.Fatal(err)
	}
	// .bats includes `set +x`, so baseline xtrace coverage must be skipped.
	if err := os.WriteFile(filepath.Join(workDir, testRel), []byte("#!/usr/bin/env bats\n@test \"h\" {\n    set +x\n    helper\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "vendor", "pkg", "helper.sh"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := runner.NewWorkspaceSession(context.Background(), runner.WorkspaceConfig{
		WorkDir:           workDir,
		TargetFiles:       []string{targetRel},
		TestFiles:         []string{testRel},
		WorkspaceIncludes: []string{"vendor/pkg/*.sh"},
		WorkspaceExcludes: []string{"vendor/lib/**"},
	})
	if err != nil {
		t.Fatalf("NewWorkspaceSession failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	mutants, err := mutator.Generate(targetRel, []byte(targetSrc))
	if err != nil || len(mutants) == 0 {
		t.Fatalf("Generate failed: %v", err)
	}

	var sawHelper, sawDualFilter atomic.Bool
	var calls atomic.Int32
	execChmodAndCheck := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		if _, err := os.Stat(filepath.Join(dir, "vendor", "pkg", "helper.sh")); err == nil {
			sawHelper.Store(true)
		}
		filterCount := 0
		for _, a := range args {
			if a == "--filter" || a == "-f" || strings.HasPrefix(a, "--filter=") {
				filterCount++
			}
		}
		if filterCount > 1 {
			sawDualFilter.Store(true)
		}
		// Simulate a test leaving behind a chmod 000 directory and chmod 444 file in sandbox.
		lockedDir := filepath.Join(dir, "locked_tmp")
		_ = os.MkdirAll(lockedDir, 0o755)
		_ = os.WriteFile(filepath.Join(lockedDir, "secret.txt"), []byte("x"), 0o444)
		_ = os.Chmod(lockedDir, 0o000)

		if n == 1 {
			// Verify instrumented code was NOT injected because .bats disables xtrace.
			b, _ := os.ReadFile(filepath.Join(dir, targetRel))
			if strings.Contains(string(b), "BASH_XTRACEFD") {
				t.Errorf("expected coverage instrumentation to be skipped when .bats has set +x")
			}
			return []byte("ok 1\n"), nil
		}
		return []byte("ok 1\n"), nil
	}

	cacheFile := filepath.Join(t.TempDir(), "cache.json")
	t.Setenv("HOME", "/tmp/home-a")
	t.Setenv("CI", "false")
	sum1, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		Workspace:  ws,
		TargetFile: targetRel,
		TestFile:   testRel,
		Jobs:       1,
		BatsArgs:   []string{"--filter", "^h$"},
		CacheFile:  cacheFile,
		Executor:   execChmodAndCheck,
	}, mutants)
	if err != nil {
		t.Fatalf("first Run failed: %v", err)
	}
	if !sawHelper.Load() {
		t.Errorf("expected vendor/pkg/helper.sh to be included via nested workspace include glob")
	}
	if sawDualFilter.Load() {
		t.Errorf("expected Bashtion not to append a second --filter when BatsArgs already specifies --filter")
	}
	// Baseline + 1 survived evaluation (no duplicate unfiltered rerun because user specified --filter)
	if calls.Load() != 2 {
		t.Errorf("expected 2 executor calls (baseline + 1 user-filtered mutant), got %d", calls.Load())
	}

	// Second run with changed HOME/CI must invalidate cache.
	t.Setenv("HOME", "/tmp/home-b")
	t.Setenv("CI", "true")
	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		Workspace:  ws,
		TargetFile: targetRel,
		TestFile:   testRel,
		Jobs:       1,
		BatsArgs:   []string{"--filter", "^h$"},
		CacheFile:  cacheFile,
		Executor:   execChmodAndCheck,
	}, mutants)
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if sum1.Total != sum2.Total || sum2.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after HOME/CI change, got %d", sum2.CacheHits)
	}
}

func TestRun_MultiTestFilesAndUncoveredStatusSeparation(t *testing.T) {
	workDir := t.TempDir()
	script := `#!/usr/bin/env bash
covered_fn() {
    if [[ "$1" -eq 0 ]]; then
        return 0
    fi
    return 1
}
uncovered_fn() {
    if [[ "$1" -eq 1 ]]; then
        return 0
    fi
    return 1
}
`
	if err := os.WriteFile(filepath.Join(workDir, "app.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "a.bats"), []byte("@test \"t1\" { covered_fn 0; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "b.bats"), []byte("@test \"t2\" { covered_fn 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mutants, err := mutator.Generate("app.sh", []byte(script))
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var sawBothTests atomic.Bool
	cacheFile := filepath.Join(workDir, "custom-cache.json")

	executor := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		n := calls.Add(1)
		if len(args) >= 2 && args[len(args)-2] == "a.bats" && args[len(args)-1] == "b.bats" {
			sawBothTests.Store(true)
		}
		if n == 1 {
			// Emit baseline trace covering lines 3-6 of app.sh (covered_fn only)
			instrumented, readErr := os.ReadFile(filepath.Join(dir, "app.sh"))
			if readErr == nil {
				if idx := strings.Index(string(instrumented), "__bashtion_cov_file='"); idx >= 0 {
					rest := string(instrumented)[idx+len("__bashtion_cov_file='"):]
					if end := strings.IndexByte(rest, '\''); end >= 0 {
						tracePath := rest[:end]
						var trace strings.Builder
						for _, ln := range []int{4, 5, 6, 7} {
							trace.WriteString("+__BASHTION_COV__:t1__BASHTION_SEP__app.sh__BASHTION_SEP__")
							trace.WriteString(strconv.Itoa(ln))
							trace.WriteString("__BASHTION_END__ cmd\n")
						}
						_ = os.WriteFile(tracePath, []byte(trace.String()), 0o644)
					}
				}
			}
			return []byte("ok 1\nok 2\n"), nil
		}
		// Kill half, survive half among covered mutants
		if n%2 == 0 {
			return []byte("not ok 1\n"), fakeKilledErr()
		}
		return []byte("ok 1\nok 2\n"), nil
	}

	sum, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "app.sh",
		TestFiles:  []string{"a.bats", "b.bats"},
		Jobs:       1,
		CacheFile:  cacheFile,
		Executor:   executor,
	}, mutants)
	if err != nil {
		t.Fatalf("Run with multiple TestFiles failed: %v", err)
	}
	if !sawBothTests.Load() {
		t.Errorf("expected bats executor to receive both a.bats and b.bats")
	}
	if !sum.CoverageEnabled {
		t.Errorf("expected CoverageEnabled=true")
	}
	if sum.Uncovered == 0 || sum.Killed == 0 {
		t.Errorf("expected both uncovered and killed mutants, got %+v", sum)
	}
	if sum.Killed+sum.Survived+sum.Uncovered+sum.Timeout+sum.Errors != sum.Total {
		t.Errorf("disjoint status counts must equal Total: %+v", sum)
	}
	covScore, ok := sum.CoveredScore()
	if !ok || covScore <= sum.Score() {
		t.Errorf("expected CoveredScore (%.1f, ok=%v) > overall Score (%.1f)", covScore, ok, sum.Score())
	}
	for _, r := range sum.Results {
		if r.Covered == nil {
			t.Fatalf("expected non-nil Covered when coverage is active, got nil for %+v", r)
		}
		if r.Status == runner.StatusUncovered && *r.Covered {
			t.Errorf("UNCOVERED mutant must have Covered=false: %+v", r)
		}
		if r.Status != runner.StatusUncovered && !*r.Covered {
			t.Errorf("non-UNCOVERED mutant must have Covered=true: %+v", r)
		}
	}

	// Second run should hit cache for all mutants; modifying b.bats must invalidate cache.
	if err := os.WriteFile(filepath.Join(workDir, "b.bats"), []byte("@test \"t2\" { covered_fn 2; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls.Store(0)
	sum2, err := runner.Run(context.Background(), runner.Config{
		WorkDir:    workDir,
		TargetFile: "app.sh",
		TestFiles:  []string{"a.bats", "b.bats"},
		Jobs:       1,
		CacheFile:  cacheFile,
		Executor:   executor,
	}, mutants)
	if err != nil {
		t.Fatalf("second Run failed: %v", err)
	}
	if sum2.CacheHits != 0 {
		t.Errorf("expected 0 cache hits after modifying second test file b.bats, got %d", sum2.CacheHits)
	}
}
