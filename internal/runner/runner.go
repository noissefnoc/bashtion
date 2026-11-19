//go:build unix

package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	stdpath "path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/noissefnoc/bashtion/internal/cache"
	"github.com/noissefnoc/bashtion/internal/coverage"
	"github.com/noissefnoc/bashtion/internal/discovery"
	"github.com/noissefnoc/bashtion/internal/mutator"
)

// Status represents the outcome of evaluating a mutant against the bats test suite.
type Status string

const (
	StatusKilled    Status = "KILLED"
	StatusSurvived  Status = "SURVIVED"
	StatusUncovered Status = "UNCOVERED"
	StatusTimeout   Status = "TIMEOUT"
	StatusError     Status = "ERROR"
)

// Result holds the evaluation result for a single mutant.
type Result struct {
	Mutant    mutator.Mutant
	Status    Status
	Covered   *bool
	Uncovered bool
	Cached    bool
	Duration  time.Duration
	Output    string
}

func boolPtr(v bool) *bool {
	return &v
}

// Summary aggregates all mutant results and phase timing breakdowns for a mutation testing run.
type Summary struct {
	TargetFile             string
	TestFile               string
	TestFiles              []string
	BaselineDuration       time.Duration
	WorkspaceSetupDuration time.Duration
	MutantEvalDuration     time.Duration
	TotalDuration          time.Duration
	Jobs                   int
	Total                  int
	Killed                 int
	Survived               int
	Uncovered              int
	CacheHits              int
	Timeout                int
	Errors                 int
	CoverageEnabled        bool
	CacheEnabled           bool
	CacheFile              string
	AllowZeroMutants       bool
	Warnings               []string
	Results                []Result
}

// Score returns the overall mutation score percentage (Killed + Timeout) / (Total - Errors) * 100.
// If valid mutants is <= 0 (e.g. no mutants generated), it returns 0.0.
func (s Summary) Score() float64 {
	valid := s.Total - s.Errors
	if valid <= 0 {
		return 0.0
	}
	return float64(s.Killed+s.Timeout) / float64(valid) * 100.0
}

// CoveredMutants returns the count of valid mutants that were executed during baseline coverage.
func (s Summary) CoveredMutants() int {
	covered := s.Total - s.Errors - s.Uncovered
	if covered < 0 {
		return 0
	}
	return covered
}

// CoveredScore returns the mutation score percentage computed strictly over covered mutants:
// (Killed + Timeout) / CoveredMutants * 100. Returns (0.0, false) when CoveredMutants is 0.
func (s Summary) CoveredScore() (float64, bool) {
	covered := s.CoveredMutants()
	if covered <= 0 {
		return 0.0, false
	}
	return float64(s.Killed+s.Timeout) / float64(covered) * 100.0, true
}

// CommandExecutor abstracts external command execution (e.g. bats) for unit testability.
type CommandExecutor func(ctx context.Context, dir, name string, args ...string) ([]byte, error)

// DefaultCommandExecutor executes a command in its own process group so timeouts and normal exits
// terminate any child subshells or background jobs spawned by the test suite.
func DefaultCommandExecutor(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 100 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out, err
}

// Config configures the mutation test runner.
type Config struct {
	WorkDir           string
	WorkspaceDir      string
	WorkspaceIncludes []string
	WorkspaceExcludes []string
	MaxWorkspaceFiles int
	MaxWorkspaceBytes int64
	// Workspace optionally shares a pre-scanned manifest and reusable worker sandboxes across multiple targets.
	Workspace         *WorkspaceSession
	TargetFile        string
	TestFile          string
	TestFiles         []string
	BatsBin           string
	BatsArgs          []string
	Jobs              int
	Timeout           time.Duration
	DisableTestFilter bool
	DisableCoverage   bool
	NoCache           bool
	CacheFile         string
	CacheStore        *cache.Store
	ToolVersion       string
	Executor          CommandExecutor
	OnProgress        func(completed, total int, res Result)
}

func (c Config) resolvedTestFiles() []string {
	if len(c.TestFiles) > 0 {
		return append([]string(nil), c.TestFiles...)
	}
	if c.TestFile != "" {
		return []string{c.TestFile}
	}
	return nil
}

// WorkspaceSession holds a pre-scanned workspace manifest and reusable worker sandbox directories
// across multiple target scripts sharing the same workspace root.
type WorkspaceSession struct {
	mu            sync.Mutex
	workRoot      string
	tmpRoot       string
	manifest      *workspaceManifest
	workerDirs    []string
	dirtyWorkers  []bool
	versionProbes map[string][2]string
}

// WorkspaceConfig configures a shared WorkspaceSession for multi-target execution.
type WorkspaceConfig struct {
	WorkDir           string
	WorkspaceDir      string
	TargetFiles       []string
	TestFiles         []string
	WorkspaceIncludes []string
	WorkspaceExcludes []string
	MaxWorkspaceFiles int
	MaxWorkspaceBytes int64
}

// NewWorkspaceSession scans the common workspace root once and creates a reusable sandbox root.
func NewWorkspaceSession(ctx context.Context, cfg WorkspaceConfig) (*WorkspaceSession, error) {
	workDir := cfg.WorkDir
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workDir = wd
	}
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	var absWorkspaceDir string
	if cfg.WorkspaceDir != "" {
		absWorkspaceDir = absOrJoin(absWorkDir, cfg.WorkspaceDir)
	}
	var absPaths []string
	for _, t := range cfg.TargetFiles {
		absPaths = append(absPaths, absOrJoin(absWorkDir, t))
	}
	for _, t := range cfg.TestFiles {
		if t != "" {
			absPaths = append(absPaths, absOrJoin(absWorkDir, t))
		}
	}
	workRoot, err := resolveSafeMultiWorkspaceRoot(ctx, absWorkDir, absWorkspaceDir, absPaths)
	if err != nil {
		return nil, err
	}

	keepRelPaths := make(map[string]bool, len(absPaths))
	for _, ap := range absPaths {
		if relP, relErr := filepath.Rel(workRoot, ap); relErr == nil && relP != "." && filepath.IsLocal(relP) {
			keepRelPaths[filepath.Clean(relP)] = true
		}
	}

	manifest, err := buildWorkspaceManifest(workRoot, manifestOptions{
		keepRelPaths: keepRelPaths,
		includes:     cfg.WorkspaceIncludes,
		excludes:     cfg.WorkspaceExcludes,
		maxFiles:     cfg.MaxWorkspaceFiles,
		maxBytes:     cfg.MaxWorkspaceBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan workspace: %w", err)
	}

	tmpRoot, err := os.MkdirTemp("", "bashtion-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp workspace: %w", err)
	}

	return &WorkspaceSession{
		workRoot: workRoot,
		tmpRoot:  tmpRoot,
		manifest: manifest,
	}, nil
}

// Close removes temporary sandbox directories allocated by the session.
func (ws *WorkspaceSession) Close() error {
	if ws == nil || ws.tmpRoot == "" {
		return nil
	}
	return removeAllWithChmodRetry(ws.tmpRoot)
}

func (ws *WorkspaceSession) ensureWorkers(count int) ([]string, []bool, error) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for len(ws.workerDirs) < count {
		idx := len(ws.workerDirs)
		wDir := filepath.Join(ws.tmpRoot, fmt.Sprintf("worker-%d", idx))
		if err := provisionWorkerDir(ws.manifest, wDir); err != nil {
			return nil, nil, fmt.Errorf("failed to prepare worker-%d workspace: %w", idx, err)
		}
		ws.workerDirs = append(ws.workerDirs, wDir)
		ws.dirtyWorkers = append(ws.dirtyWorkers, false)
	}
	dirs := append([]string(nil), ws.workerDirs[:count]...)
	initialDirty := append([]bool(nil), ws.dirtyWorkers[:count]...)
	return dirs, initialDirty, nil
}

func (ws *WorkspaceSession) markWorkersDirty(count int) {
	if ws == nil {
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for i := 0; i < count && i < len(ws.dirtyWorkers); i++ {
		ws.dirtyWorkers[i] = true
	}
}

func (ws *WorkspaceSession) probeToolVersions(ctx context.Context, batsBin string) (batsVer, bashVer string) {
	if ws == nil {
		return runVersionProbes(ctx, batsBin)
	}
	key := batsBin + "\x00" + os.Getenv("PATH")
	ws.mu.Lock()
	if pair, ok := ws.versionProbes[key]; ok {
		ws.mu.Unlock()
		return pair[0], pair[1]
	}
	ws.mu.Unlock()

	batsV, bashV := runVersionProbes(ctx, batsBin)

	ws.mu.Lock()
	if ws.versionProbes == nil {
		ws.versionProbes = make(map[string][2]string)
	}
	ws.versionProbes[key] = [2]string{batsV, bashV}
	ws.mu.Unlock()
	return batsV, bashV
}

// Run executes baseline verification followed by parallel worker-pool mutant testing.
func Run(ctx context.Context, cfg Config, mutants []mutator.Mutant) (*Summary, error) {
	runStart := time.Now()

	if cfg.BatsBin == "" {
		cfg.BatsBin = "bats"
	}
	usingDefaultExecutor := cfg.Executor == nil
	if cfg.Executor == nil {
		cfg.Executor = DefaultCommandExecutor
	}
	if cfg.Jobs <= 0 {
		cfg.Jobs = runtime.NumCPU()
	}
	if cfg.WorkDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		cfg.WorkDir = wd
	}

	absWorkDir, err := filepath.Abs(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	var absWorkspaceDir string
	if cfg.WorkspaceDir != "" {
		absWorkspaceDir = absOrJoin(absWorkDir, cfg.WorkspaceDir)
	}
	absTarget := absOrJoin(absWorkDir, cfg.TargetFile)
	testFiles := cfg.resolvedTestFiles()
	if len(testFiles) == 0 {
		return nil, errors.New("at least one bats test file is required")
	}
	testFileDisplay := strings.Join(testFiles, ", ")

	absTests := make([]string, len(testFiles))
	var combinedBatsBuf bytes.Buffer
	targetBytes, _ := os.ReadFile(absTarget)
	targetDisablesXtrace := coverage.DisablesXtrace(targetBytes)
	batsBytesByFile := make([][]byte, len(testFiles))
	for i, tf := range testFiles {
		absTests[i] = absOrJoin(absWorkDir, tf)
		b, _ := os.ReadFile(absTests[i])
		batsBytesByFile[i] = b
		if len(b) > 0 {
			if combinedBatsBuf.Len() > 0 {
				combinedBatsBuf.WriteByte('\n')
			}
			combinedBatsBuf.Write(b)
			if !targetDisablesXtrace && coverage.DisablesXtrace(b) {
				targetDisablesXtrace = true
			}
		}
	}
	batsBytes := combinedBatsBuf.Bytes()

	workerCount := cfg.Jobs
	if len(mutants) == 0 {
		workerCount = 1
	} else if workerCount > len(mutants) {
		workerCount = len(mutants)
	}

	wsStart := time.Now()
	var (
		workRoot           string
		relTarget          string
		relTests           []string
		manifest           *workspaceManifest
		tmpRoot            string
		workerDirs         []string
		workerInitialDirty []bool
	)

	if cfg.Workspace != nil {
		workRoot = cfg.Workspace.workRoot
		var ok bool
		relTarget, relTests, ok = checkTargetAndTestsInsideRoot(workRoot, absTarget, absTests)
		if !ok {
			return nil, fmt.Errorf("target %q and test(s) %v must reside within shared workspace root %q", absTarget, absTests, workRoot)
		}
		manifest = cfg.Workspace.manifest
		tmpRoot = cfg.Workspace.tmpRoot
		workerDirs, workerInitialDirty, err = cfg.Workspace.ensureWorkers(workerCount)
		if err != nil {
			return nil, err
		}
		if workerInitialDirty[0] {
			if err := restoreWorkerFiles(manifest, workerDirs[0], ""); err != nil {
				return nil, fmt.Errorf("failed to reset worker-0 workspace before baseline: %w", err)
			}
			workerInitialDirty[0] = false
		}
	} else {
		workRoot, relTarget, relTests, err = resolveSafeWorkspaceRootMultiTests(ctx, absWorkDir, absWorkspaceDir, absTarget, absTests)
		if err != nil {
			return nil, err
		}
		keepRelPaths := make(map[string]bool, len(relTests))
		for _, rt := range relTests {
			keepRelPaths[filepath.Clean(rt)] = true
		}
		manifest, err = buildWorkspaceManifest(workRoot, manifestOptions{
			skipRelTarget: relTarget,
			relTest:       relTests[0],
			keepRelPaths:  keepRelPaths,
			includes:      cfg.WorkspaceIncludes,
			excludes:      cfg.WorkspaceExcludes,
			maxFiles:      cfg.MaxWorkspaceFiles,
			maxBytes:      cfg.MaxWorkspaceBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan workspace: %w", err)
		}

		tmpRoot, err = os.MkdirTemp("", "bashtion-*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp workspace: %w", err)
		}
		defer func() { _ = removeAllWithChmodRetry(tmpRoot) }()

		workerDirs = make([]string, workerCount)
		workerInitialDirty = make([]bool, workerCount)
		for w := 0; w < workerCount; w++ {
			wDir := filepath.Join(tmpRoot, fmt.Sprintf("worker-%d", w))
			if err := provisionWorkerDir(manifest, wDir); err != nil {
				return nil, fmt.Errorf("failed to prepare worker-%d workspace: %w", w, err)
			}
			workerDirs[w] = wDir
		}
	}
	wsDuration := time.Since(wsStart)
	warnings := append([]string(nil), manifest.warnings...)

	safeTestArgs := make([]string, len(relTests))
	for i, rt := range relTests {
		safeTestArgs[i] = safeTestArg(rt)
	}

	// 1. Run single baseline test suite inside worker-0 sandbox (with optional BASH_XTRACEFD coverage)
	var covProfile *coverage.Profile
	baseStart := time.Now()
	baselineArgs := append(append([]string{}, cfg.BatsArgs...), safeTestArgs...)
	w0Target := filepath.Join(workerDirs[0], relTarget)
	var baseOut []byte
	var baseErr error

	writeTargetToWorker0 := func(data []byte) error {
		if err := os.MkdirAll(filepath.Dir(w0Target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(w0Target, data, 0o755)
	}

	if !cfg.DisableCoverage && !targetDisablesXtrace && len(targetBytes) > 0 && len(mutants) > 0 {
		tracePath := filepath.Join(tmpRoot, "baseline-coverage.trace")
		defer func() { _ = os.Remove(tracePath) }()
		instrumented, lineOffset := coverage.InstrumentScript(targetBytes, tracePath)
		if err := writeTargetToWorker0(instrumented); err != nil {
			return nil, fmt.Errorf("failed to write instrumented target in worker-0: %w", err)
		}
		baseOut, baseErr = cfg.Executor(ctx, workerDirs[0], cfg.BatsBin, baselineArgs...)
		if baseErr == nil && ctx.Err() == nil {
			if tf, err := os.Open(tracePath); err == nil {
				if prof, err := coverage.ParseTrace(tf, relTarget, lineOffset); err == nil && prof.HasAnyCoverage() {
					covProfile = prof
				}
				_ = tf.Close()
			}
		} else if baseErr != nil && ctx.Err() == nil {
			// Automatic fallback to uninstrumented baseline if instrumentation interfered with tests (e.g. LINENO, dash, DEBUG trap).
			if err := restoreWorkerFiles(manifest, workerDirs[0], relTarget); err != nil {
				return nil, fmt.Errorf("failed to restore worker-0 before uninstrumented baseline fallback: %w", err)
			}
			if err := writeTargetToWorker0(targetBytes); err != nil {
				return nil, fmt.Errorf("failed to write target in worker-0 for uninstrumented baseline fallback: %w", err)
			}
			fallbackOut, fallbackErr := cfg.Executor(ctx, workerDirs[0], cfg.BatsBin, baselineArgs...)
			if fallbackErr == nil && ctx.Err() == nil {
				baseOut = fallbackOut
				baseErr = nil
				covProfile = nil
				warnings = append(warnings, "instrumented baseline failed; fell back to uninstrumented baseline (line coverage disabled)")
			} else {
				baseOut = fallbackOut
				baseErr = fallbackErr
			}
		}
	} else {
		if err := writeTargetToWorker0(targetBytes); err != nil {
			return nil, fmt.Errorf("failed to write target in worker-0: %w", err)
		}
		baseOut, baseErr = cfg.Executor(ctx, workerDirs[0], cfg.BatsBin, baselineArgs...)
	}
	if cfg.Workspace != nil {
		cfg.Workspace.markWorkersDirty(1)
	}
	if err := restoreWorkerFiles(manifest, workerDirs[0], relTarget); err != nil {
		return nil, fmt.Errorf("failed to restore worker-0 workspace after baseline: %w", err)
	}
	baseDuration := time.Since(baseStart)

	if ctx.Err() != nil {
		return nil, fmt.Errorf("baseline test suite interrupted: %w", ctx.Err())
	}
	if baseErr != nil {
		return nil, fmt.Errorf("baseline test suite failed before mutation testing (%v):\n%s", baseErr, string(baseOut))
	}

	coverageActive := covProfile != nil && !targetDisablesXtrace

	if len(mutants) == 0 {
		return &Summary{
			TargetFile:             cfg.TargetFile,
			TestFile:               testFileDisplay,
			TestFiles:              testFiles,
			BaselineDuration:       baseDuration,
			WorkspaceSetupDuration: wsDuration,
			Jobs:                   cfg.Jobs,
			Total:                  0,
			CoverageEnabled:        coverageActive,
			CacheEnabled:           !cfg.NoCache,
			CacheFile:              cfg.CacheFile,
			Warnings:               warnings,
			Results:                []Result{},
			TotalDuration:          time.Since(runStart),
		}, nil
	}

	// Build optional per-function bats --filter regex map and identify functions called by other functions
	var funcFilters map[string]string
	var totalBatsBlocks int
	if len(batsBytes) > 0 {
		totalBatsBlocks = len(parseBatsTestBlocks(batsBytes))
	}
	if !cfg.DisableTestFilter && len(batsBytes) > 0 {
		funcFilters = BuildFuncTestFilters(batsBytes, mutants, targetBytes)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = maxDuration(5*time.Second, baseDuration*5)
	}

	summary := &Summary{
		TargetFile:             cfg.TargetFile,
		TestFile:               testFileDisplay,
		TestFiles:              testFiles,
		BaselineDuration:       baseDuration,
		WorkspaceSetupDuration: wsDuration,
		Jobs:                   workerCount,
		Total:                  len(mutants),
		CoverageEnabled:        coverageActive,
		CacheEnabled:           !cfg.NoCache,
		CacheFile:              cfg.CacheFile,
		Warnings:               warnings,
		Results:                make([]Result, len(mutants)),
	}

	// Prepare optional incremental cache store
	cacheStore := cfg.CacheStore
	cachePath := cfg.CacheFile
	if !cfg.NoCache && cacheStore == nil && cachePath != "" {
		var loadErr error
		cacheStore, loadErr = cache.Load(cachePath)
		if loadErr != nil {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("failed to read cache file %q (%v); pass --cache-file <writable-path>, set XDG_CACHE_HOME, or pass --no-cache to disable caching", cachePath, loadErr))
		}
	}

	// Compute cache keys per mutant if caching is active
	var cacheKeys []string
	if !cfg.NoCache && cacheStore != nil && len(targetBytes) > 0 {
		cacheMeta := buildCacheMeta(ctx, cfg, manifest, relTarget, coverageActive, usingDefaultExecutor)
		cacheTestBytes := serializeTestFilesForCache(relTests, batsBytesByFile)
		interdependentFuncs := findInterdependentFunctionsForMutants(targetBytes, mutants)
		topLevelBytes := extractTopLevelNonFuncBytes(targetBytes)
		cacheKeys = make([]string, len(mutants))
		for i, m := range mutants {
			scopeStart, scopeEnd := m.ScopeStart, m.ScopeEnd
			funcKeyName := m.FuncName
			var scopeSlice []byte
			if m.FuncName == "" || interdependentFuncs[m.FuncName] || scopeStart < 0 || scopeEnd > len(targetBytes) || scopeStart >= scopeEnd {
				scopeStart = 0
				funcKeyName = "<file>"
				scopeSlice = targetBytes
			} else {
				funcBytes := targetBytes[scopeStart:scopeEnd]
				if len(topLevelBytes) > 0 {
					combinedScope := make([]byte, 0, len(topLevelBytes)+1+len(funcBytes))
					combinedScope = append(combinedScope, topLevelBytes...)
					combinedScope = append(combinedScope, '\n')
					combinedScope = append(combinedScope, funcBytes...)
					scopeSlice = combinedScope
				} else {
					scopeSlice = funcBytes
				}
			}
			relOffset := m.StartOffset - scopeStart
			cacheKeys[i] = cache.ComputeKey(
				relTarget,
				funcKeyName,
				scopeSlice,
				cacheTestBytes,
				string(m.Category),
				m.Original,
				m.Replacement,
				relOffset,
				cacheMeta...,
			)
		}
	}

	// 2. Pre-evaluate uncovered lines and incremental cache hits before spawning worker jobs
	evalStart := time.Now()
	jobCh := make(chan int, len(mutants))
	completed := 0

	recordResult := func(idx int, res Result) {
		summary.Results[idx] = res
		switch res.Status {
		case StatusTimeout:
			summary.Timeout++
		case StatusKilled:
			summary.Killed++
		case StatusError:
			summary.Errors++
		case StatusUncovered:
			summary.Uncovered++
		case StatusSurvived:
			summary.Survived++
		}
		if res.Cached {
			summary.CacheHits++
		}
		completed++
		if cfg.OnProgress != nil {
			cfg.OnProgress(completed, len(mutants), res)
		}
	}

	pendingJobs := 0
	for i, m := range mutants {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("mutation testing interrupted: %w", ctx.Err())
		}
		if coverageActive && !isMutantCovered(covProfile, m) {
			res := Result{
				Mutant:    m,
				Status:    StatusUncovered,
				Covered:   boolPtr(false),
				Uncovered: true,
				Duration:  0,
			}
			if len(cacheKeys) > i && cacheKeys[i] != "" {
				cacheStore.Put(cacheKeys[i], cache.Entry{Status: string(StatusUncovered), Uncovered: true})
			}
			recordResult(i, res)
			continue
		}

		if len(cacheKeys) > i && cacheKeys[i] != "" {
			if entry, ok := cacheStore.Get(cacheKeys[i]); ok {
				st := Status(entry.Status)
				if st == StatusKilled || st == StatusSurvived || st == StatusUncovered {
					if entry.Uncovered {
						st = StatusUncovered
					}
					var cov *bool
					if coverageActive {
						cov = boolPtr(st != StatusUncovered)
					}
					res := Result{
						Mutant:    m,
						Status:    st,
						Covered:   cov,
						Uncovered: st == StatusUncovered,
						Cached:    true,
						Duration:  0,
					}
					recordResult(i, res)
					continue
				}
			}
		}

		jobCh <- i
		pendingJobs++
	}
	close(jobCh)

	if pendingJobs == 0 {
		summary.MutantEvalDuration = time.Since(evalStart)
		if !cfg.NoCache && cacheStore != nil && cachePath != "" {
			if saveErr := cacheStore.Save(cachePath); saveErr != nil {
				summary.Warnings = append(summary.Warnings, fmt.Sprintf("failed to write cache file %q (%v); pass --cache-file <writable-path>, set XDG_CACHE_HOME, or pass --no-cache to disable caching", cachePath, saveErr))
			}
		}
		summary.TotalDuration = time.Since(runStart)
		return summary, nil
	}

	// 3. Dispatch remaining mutants across worker pool
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	activeWorkers := workerCount
	if activeWorkers > pendingJobs {
		activeWorkers = pendingJobs
	}

	if cfg.Workspace != nil {
		cfg.Workspace.markWorkersDirty(activeWorkers)
	}

	for w := 0; w < activeWorkers; w++ {
		wDir := workerDirs[w]
		wInitialDirty := workerInitialDirty[w]
		sandboxTarget := filepath.Join(wDir, relTarget)
		if err := os.MkdirAll(filepath.Dir(sandboxTarget), 0o755); err != nil {
			return nil, err
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			evalIndex := 0
			if wInitialDirty {
				evalIndex = 1
			}
			var mutBuf bytes.Buffer
			for {
				if ctx.Err() != nil {
					return
				}
				var idx int
				var ok bool
				select {
				case <-ctx.Done():
					return
				case idx, ok = <-jobCh:
					if !ok {
						return
					}
				}
				if ctx.Err() != nil {
					return
				}

				m := mutants[idx]
				var execErr error
				var status Status
				var out []byte
				var dur time.Duration

				if evalIndex > 0 {
					if err := restoreWorkerFiles(manifest, wDir, relTarget); err != nil {
						execErr = fmt.Errorf("failed to restore worker sandbox before mutant %d: %w", m.ID, err)
					}
				}
				evalIndex++

				if execErr == nil {
					if err := m.ApplyToBuffer(&mutBuf, targetBytes); err != nil {
						execErr = fmt.Errorf("failed to materialize mutant %d: %w", m.ID, err)
					} else if err := os.WriteFile(sandboxTarget, mutBuf.Bytes(), 0o755); err != nil {
						execErr = fmt.Errorf("failed to write mutant %d: %w", m.ID, err)
					}
				}

				if execErr != nil {
					status = StatusError
					out = []byte(execErr.Error())
				} else {
					runCtx, cancel := context.WithTimeout(ctx, timeout)
					start := time.Now()

					var filterRegex string
					if !cfg.DisableTestFilter {
						if covProfile != nil {
							filterRegex = filterRegexForMutant(covProfile, m, totalBatsBlocks)
						}
						if filterRegex == "" {
							filterRegex = funcFilters[m.FuncName]
						}
					}

					userFiltered := hasBatsFilterArg(cfg.BatsArgs)
					var args []string
					args = append(args, cfg.BatsArgs...)
					if filterRegex != "" && !userFiltered {
						args = append(args, "--filter", filterRegex)
					}
					args = append(args, safeTestArgs...)

					var runErr error
					out, runErr = cfg.Executor(runCtx, wDir, cfg.BatsBin, args...)
					dur = time.Since(start)
					runCtxErr := runCtx.Err()
					cancel()

					// Never classify or cache interrupted runs when the parent context is canceled.
					if ctx.Err() != nil {
						return
					}

					status, execErr = classifyMutantOutcome(runCtxErr, runErr, m.ID)

					// Verify survived mutants without test filter so heuristic filtering cannot produce false SURVIVED.
					if status == StatusSurvived && filterRegex != "" && !userFiltered && ctx.Err() == nil {
						if err := restoreWorkerFiles(manifest, wDir, relTarget); err != nil {
							status = StatusError
							execErr = fmt.Errorf("failed to restore worker sandbox before unfiltered rerun for mutant %d: %w", m.ID, err)
							out = []byte(execErr.Error())
						} else if err := os.WriteFile(sandboxTarget, mutBuf.Bytes(), 0o755); err != nil {
							status = StatusError
							execErr = fmt.Errorf("failed to write mutant %d for unfiltered rerun: %w", m.ID, err)
							out = []byte(execErr.Error())
						} else {
							rerunCtx, rerunCancel := context.WithTimeout(ctx, timeout)
							rerunStart := time.Now()
							rerunArgs := append(append([]string{}, cfg.BatsArgs...), safeTestArgs...)
							rerunOut, rerunErr := cfg.Executor(rerunCtx, wDir, cfg.BatsBin, rerunArgs...)
							dur += time.Since(rerunStart)
							rerunCtxErr := rerunCtx.Err()
							rerunCancel()

							if ctx.Err() != nil {
								return
							}
							status, execErr = classifyMutantOutcome(rerunCtxErr, rerunErr, m.ID)
							out = rerunOut
						}
					}
				}

				if ctx.Err() != nil {
					return
				}

				if (status == StatusKilled || status == StatusSurvived) && len(cacheKeys) > idx && cacheKeys[idx] != "" {
					cacheStore.Put(cacheKeys[idx], cache.Entry{Status: string(status)})
				}

				var cov *bool
				if coverageActive {
					cov = boolPtr(true)
				}
				res := Result{
					Mutant:   m,
					Status:   status,
					Covered:  cov,
					Duration: dur,
					Output:   string(out),
				}

				mu.Lock()
				if execErr != nil && firstErr == nil {
					firstErr = execErr
				}
				recordResult(idx, res)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	summary.MutantEvalDuration = time.Since(evalStart)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("mutation testing interrupted: %w", ctx.Err())
	}
	if summary.Errors > 0 {
		summary.TotalDuration = time.Since(runStart)
		return summary, fmt.Errorf("%d mutant evaluation error(s) occurred (first: %v)", summary.Errors, firstErr)
	}

	if !cfg.NoCache && cacheStore != nil && cachePath != "" {
		if saveErr := cacheStore.Save(cachePath); saveErr != nil {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("failed to write cache file %q (%v); pass --cache-file <writable-path>, set XDG_CACHE_HOME, or pass --no-cache to disable caching", cachePath, saveErr))
		}
	}

	summary.TotalDuration = time.Since(runStart)
	return summary, nil
}

func isMutantCovered(prof *coverage.Profile, m mutator.Mutant) bool {
	if prof == nil {
		return true
	}
	if prof.IsCovered(m.Line) {
		return true
	}
	if m.CoverageLine > 0 && prof.IsCovered(m.CoverageLine) {
		return true
	}
	return false
}

func filterRegexForMutant(prof *coverage.Profile, m mutator.Mutant, totalTests int) string {
	if prof == nil {
		return ""
	}
	if r := prof.FilterRegexForLine(m.Line, totalTests); r != "" {
		return r
	}
	if m.CoverageLine > 0 {
		return prof.FilterRegexForLine(m.CoverageLine, totalTests)
	}
	return ""
}

// findInterdependentFunctionsForMutants returns all functions that either call another function
// or are called by another function in targetBytes, so their cache keys depend on the full file.
func findInterdependentFunctionsForMutants(targetBytes []byte, _ []mutator.Mutant) map[string]bool {
	called, callers := analyzeFunctionCallEdges(targetBytes, nil)
	if len(called) == 0 && len(callers) == 0 {
		return nil
	}
	merged := make(map[string]bool, len(called)+len(callers))
	for k := range called {
		merged[k] = true
	}
	for k := range callers {
		merged[k] = true
	}
	return merged
}

// extractTopLevelNonFuncBytes returns all bytes in targetSrc outside function declarations
// (ignoring comments and whitespace) so changes to global variables/constants invalidate function caches.
func extractTopLevelNonFuncBytes(targetSrc []byte) []byte {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	parsedFile, err := parser.Parse(bytes.NewReader(targetSrc), "")
	if err != nil {
		return targetSrc
	}
	var buf bytes.Buffer
	for _, stmt := range parsedFile.Stmts {
		if _, isFunc := stmt.Cmd.(*syntax.FuncDecl); isFunc {
			continue
		}
		start := int(stmt.Pos().Offset())
		end := int(stmt.End().Offset())
		if start >= 0 && end <= len(targetSrc) && start < end {
			buf.Write(targetSrc[start:end])
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes()
}

type batsTestBlock struct {
	title string
	body  string
}

// BuildFuncTestFilters analyzes a .bats file and maps each mutated function name to a `bats --filter`
// regular expression when only a subset of @test blocks reference that function.
// If a mutated function is also called by other functions within targetSrc, it is not filtered
// so that caller test suites can also verify the mutation safely.
func BuildFuncTestFilters(batsSrc []byte, mutants []mutator.Mutant, targetSrc ...[]byte) map[string]string {
	blocks := parseBatsTestBlocks(batsSrc)
	if len(blocks) <= 1 {
		return nil
	}

	uniqueFuncs := make(map[string]struct{})
	for _, m := range mutants {
		if m.FuncName != "" {
			uniqueFuncs[m.FuncName] = struct{}{}
		}
	}
	if len(uniqueFuncs) <= 1 {
		return nil
	}

	var targetBytes []byte
	if len(targetSrc) > 0 && len(targetSrc[0]) > 0 {
		targetBytes = targetSrc[0]
	} else if len(mutants) > 0 && len(mutants[0].OriginalSrc) > 0 {
		targetBytes = mutants[0].OriginalSrc
	}

	calledByOthers := make(map[string]bool)
	if len(targetBytes) > 0 {
		calledByOthers = findFunctionsCalledByOtherFunctions(targetBytes, uniqueFuncs)
	}

	filters := make(map[string]string)
	for fn := range uniqueFuncs {
		// If this function is invoked by other functions in the target script, do not restrict tests to avoid false survival.
		if calledByOthers[fn] {
			continue
		}

		var matchedTitles []string
		for _, blk := range blocks {
			if containsIdentToken(blk.title, fn) || containsIdentToken(blk.body, fn) {
				matchedTitles = append(matchedTitles, regexp.QuoteMeta(blk.title))
			}
		}
		if len(matchedTitles) > 0 && len(matchedTitles) < len(blocks) {
			re := "^(" + strings.Join(matchedTitles, "|") + ")$"
			if len(re) <= 2048 {
				filters[fn] = re
			}
		}
	}
	return filters
}

// findFunctionsCalledByOtherFunctions analyzes target Bash source and detects functions that are called
// inside other function declarations.
func findFunctionsCalledByOtherFunctions(targetSrc []byte, candidateFuncs map[string]struct{}) map[string]bool {
	called, _ := analyzeFunctionCallEdges(targetSrc, candidateFuncs)
	return called
}

func analyzeFunctionCallEdges(targetSrc []byte, candidateFuncs map[string]struct{}) (called map[string]bool, callers map[string]bool) {
	called = make(map[string]bool)
	callers = make(map[string]bool)

	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	parsedFile, err := parser.Parse(bytes.NewReader(targetSrc), "")
	if err != nil {
		return called, callers
	}

	declaredFuncs := make(map[string]struct{})
	syntax.Walk(parsedFile, func(node syntax.Node) bool {
		if decl, ok := node.(*syntax.FuncDecl); ok && decl.Name != nil {
			declaredFuncs[decl.Name.Value] = struct{}{}
		}
		return true
	})
	for k := range candidateFuncs {
		declaredFuncs[k] = struct{}{}
	}
	if len(declaredFuncs) == 0 {
		return called, callers
	}

	recordCallee := func(callee, callerName string) {
		if callee == "" || callee == callerName {
			return
		}
		if _, isDeclared := declaredFuncs[callee]; isDeclared {
			called[callee] = true
			if callerName != "" {
				callers[callerName] = true
			}
		}
	}

	recordCallsInNode := func(scopeNode syntax.Node, callerName string) {
		syntax.Walk(scopeNode, func(inner syntax.Node) bool {
			if callerName == "" {
				if _, isFunc := inner.(*syntax.FuncDecl); isFunc {
					return false
				}
			}
			call, ok := inner.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			cmdLit := call.Args[0].Lit()
			inspectQuotedTokens := cmdLit == "trap" || cmdLit == "eval"
			for argIdx, argWord := range call.Args {
				if len(argWord.Parts) == 1 {
					if lit, ok := argWord.Parts[0].(*syntax.Lit); ok {
						recordCallee(lit.Value, callerName)
						continue
					}
				}
				if inspectQuotedTokens && argIdx > 0 {
					start := int(argWord.Pos().Offset())
					end := int(argWord.End().Offset())
					if start >= 0 && end <= len(targetSrc) && start < end {
						wordText := string(targetSrc[start:end])
						for fn := range declaredFuncs {
							if fn != callerName && containsIdentToken(wordText, fn) {
								recordCallee(fn, callerName)
							}
						}
					}
				}
			}
			return true
		})
	}

	// Scan top-level statements outside function declarations (e.g. `main "$@"` subprocess entrypoints).
	recordCallsInNode(parsedFile, "")

	syntax.Walk(parsedFile, func(node syntax.Node) bool {
		decl, ok := node.(*syntax.FuncDecl)
		if !ok || decl.Name == nil {
			return true
		}
		recordCallsInNode(decl.Body, decl.Name.Value)
		return false // Body already walked above
	})
	return called, callers
}

func classifyMutantOutcome(runCtxErr, runErr error, mutantID int) (Status, error) {
	var exitErr *exec.ExitError
	switch {
	case runCtxErr == context.DeadlineExceeded:
		return StatusTimeout, nil
	case runErr == nil:
		return StatusSurvived, nil
	case errors.As(runErr, &exitErr):
		return StatusKilled, nil
	default:
		return StatusError, fmt.Errorf("executor error for mutant %d: %w", mutantID, runErr)
	}
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

func containsIdentToken(text, ident string) bool {
	if ident == "" {
		return false
	}
	start := 0
	for start <= len(text)-len(ident) {
		idx := strings.Index(text[start:], ident)
		if idx < 0 {
			return false
		}
		abs := start + idx
		leftOK := abs == 0 || !isIdentChar(text[abs-1])
		rightPos := abs + len(ident)
		rightOK := rightPos >= len(text) || !isIdentChar(text[rightPos])
		if leftOK && rightOK {
			return true
		}
		start = abs + 1
	}
	return false
}

func hasBatsFilterArg(batsArgs []string) bool {
	for _, a := range batsArgs {
		if a == "--filter" || a == "-f" || strings.HasPrefix(a, "--filter=") || strings.HasPrefix(a, "-f=") {
			return true
		}
	}
	return false
}

func parseBatsTestBlocks(batsSrc []byte) []batsTestBlock {
	lines := strings.Split(string(batsSrc), "\n")
	var blocks []batsTestBlock
	var curTitle string
	var curBody strings.Builder
	inBlock := false
	awaitingOpenBrace := false
	braceDepth := 0
	var lex batsBlockLexer

	for _, rawLine := range lines {
		line := strings.TrimSuffix(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if strings.HasPrefix(trimmed, "@test") && len(trimmed) > len("@test") && (trimmed[len("@test")] == ' ' || trimmed[len("@test")] == '\t') {
				afterTest := strings.TrimLeft(trimmed[len("@test"):], " \t")
				title, rest := extractQuotedTitleAndRest(afterTest)
				curTitle = title
				curBody.Reset()
				lex = batsBlockLexer{}
				if curTitle == "" {
					continue
				}
				// Handle single-line `@test "title" { body }` blocks
				restTrim := strings.TrimSpace(rest)
				if strings.HasPrefix(restTrim, "{") && strings.HasSuffix(restTrim, "}") && len(restTrim) >= 2 && lex.scanLine(restTrim) == 0 {
					body := strings.TrimSpace(restTrim[1 : len(restTrim)-1])
					blocks = append(blocks, batsTestBlock{title: curTitle, body: body})
					curTitle = ""
					continue
				}
				lex = batsBlockLexer{}
				inBlock = true
				if restTrim == "" {
					awaitingOpenBrace = true
					braceDepth = 0
				} else {
					awaitingOpenBrace = false
					braceDepth = lex.scanLine(restTrim)
					if braceDepth <= 0 {
						braceDepth = 1
					}
				}
			}
			continue
		}

		if awaitingOpenBrace {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			awaitingOpenBrace = false
			delta := lex.scanLine(line)
			braceDepth += delta
			if braceDepth <= 0 {
				if openIdx := strings.IndexByte(line, '{'); openIdx >= 0 {
					closeIdx := strings.LastIndexByte(line, '}')
					if closeIdx > openIdx {
						blocks = append(blocks, batsTestBlock{
							title: curTitle,
							body:  strings.TrimSpace(line[openIdx+1 : closeIdx]),
						})
						inBlock = false
						curTitle = ""
						braceDepth = 0
						continue
					}
				}
				braceDepth = 1
			}
			continue
		}

		wasInHeredoc := lex.heredocDelim != ""
		braceDepth += lex.scanLine(line)
		if !wasInHeredoc && lex.heredocDelim == "" && !lex.inSingle && !lex.inDouble && braceDepth <= 0 && (trimmed == "}" || strings.HasSuffix(trimmed, "}")) {
			blocks = append(blocks, batsTestBlock{title: curTitle, body: curBody.String()})
			inBlock = false
			curTitle = ""
			braceDepth = 0
			lex = batsBlockLexer{}
			continue
		}
		curBody.WriteString(line)
		curBody.WriteByte('\n')
	}
	if inBlock && curTitle != "" {
		blocks = append(blocks, batsTestBlock{title: curTitle, body: curBody.String()})
	}
	return blocks
}

type batsBlockLexer struct {
	inSingle         bool
	inDouble         bool
	heredocDelim     string
	heredocStripTabs bool
}

func (l *batsBlockLexer) scanLine(line string) int {
	if l.heredocDelim != "" {
		candidate := line
		if l.heredocStripTabs {
			candidate = strings.TrimLeft(candidate, "\t")
		}
		if candidate == l.heredocDelim {
			l.heredocDelim = ""
			l.heredocStripTabs = false
		}
		return 0
	}

	delta := 0
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && !l.inSingle {
			i++
			continue
		}
		if c == '\'' && !l.inDouble {
			l.inSingle = !l.inSingle
		} else if c == '"' && !l.inSingle {
			l.inDouble = !l.inDouble
		} else if !l.inSingle && !l.inDouble {
			if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == ';') {
				break
			}
			if c == '<' && i+1 < len(line) && line[i+1] == '<' {
				if i+2 < len(line) && line[i+2] == '<' {
					i += 2
					continue
				}
				if delim, stripTabs, nextIdx, ok := parseHeredocStart(line, i+2); ok {
					if l.heredocDelim == "" {
						l.heredocDelim = delim
						l.heredocStripTabs = stripTabs
					}
					i = nextIdx - 1
					continue
				}
			}
			switch c {
			case '{':
				delta++
			case '}':
				delta--
			}
		}
	}
	return delta
}

func parseHeredocStart(line string, pos int) (delim string, stripTabs bool, nextIdx int, ok bool) {
	i := pos
	if i < len(line) && line[i] == '-' {
		stripTabs = true
		i++
	}
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= len(line) {
		return "", false, pos, false
	}
	quote := byte(0)
	if line[i] == '\'' || line[i] == '"' {
		quote = line[i]
		i++
	} else if line[i] == '\\' && i+1 < len(line) {
		i++
	}
	start := i
	for i < len(line) {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				if i > start {
					return line[start:i], stripTabs, i + 1, true
				}
				return "", false, pos, false
			}
		} else if ch == ' ' || ch == '\t' || ch == ';' || ch == ')' || ch == '&' || ch == '|' || ch == '>' || ch == '<' || ch == '#' {
			break
		}
		i++
	}
	if quote == 0 && i > start {
		return line[start:i], stripTabs, i, true
	}
	return "", false, pos, false
}

func extractQuotedTitleAndRest(s string) (title, rest string) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return "", ""
	}
	quote := s[0]
	if quote != '"' && quote != '\'' {
		return "", ""
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '\\' && quote == '"' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		if c == quote {
			return b.String(), s[i+1:]
		}
		b.WriteByte(c)
	}
	return "", ""
}

const (
	maxCachedFileBytes       = 4 * 1024 * 1024
	maxTotalManifestCacheMem = 32 * 1024 * 1024
	maxSandboxFileBytes      = 16 * 1024 * 1024
	defaultMaxWorkspaceFiles = 10000
	defaultMaxWorkspaceBytes = 128 * 1024 * 1024
)

type manifestFile struct {
	srcPath    string
	relPath    string
	mode       fs.FileMode
	size       int64
	data       []byte
	hasData    bool
	readOnly   bool
	isSymlink  bool
	linkTarget string
}

type workspaceManifest struct {
	dirs     []string
	files    []manifestFile
	knownRel map[string]bool
	warnings []string
}

type manifestOptions struct {
	skipRelTarget string
	relTest       string
	keepRelPaths  map[string]bool
	includes      []string
	excludes      []string
	maxFiles      int
	maxBytes      int64
}

type workspacePatternMatcher struct {
	raw    string
	globRe *regexp.Regexp
}

func normalizeWorkspacePatterns(patterns []string) []workspacePatternMatcher {
	var out []workspacePatternMatcher
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" || p == "." {
			continue
		}
		p = filepath.ToSlash(filepath.Clean(p))
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimSuffix(p, "/")
		if p == "" || p == "." {
			continue
		}
		var globRe *regexp.Regexp
		if strings.ContainsAny(p, "*?[{") {
			if re, err := discovery.CompileGlob(p); err == nil {
				globRe = re
			}
		}
		out = append(out, workspacePatternMatcher{raw: p, globRe: globRe})
	}
	return out
}

func matchWorkspacePattern(slashRel string, pm workspacePatternMatcher) bool {
	if strings.HasSuffix(pm.raw, "/**") {
		base := strings.TrimSuffix(pm.raw, "/**")
		return slashRel == base || strings.HasPrefix(slashRel, base+"/")
	}
	if pm.globRe != nil {
		return pm.globRe.MatchString(slashRel)
	}
	if strings.ContainsAny(pm.raw, "*?[") {
		if ok, err := stdpath.Match(pm.raw, slashRel); err == nil && ok {
			return true
		}
		return false
	}
	return slashRel == pm.raw || strings.HasPrefix(slashRel, pm.raw+"/")
}

func matchesAnyWorkspacePattern(slashRel string, patterns []workspacePatternMatcher) bool {
	for _, p := range patterns {
		if matchWorkspacePattern(slashRel, p) {
			return true
		}
	}
	return false
}

func isPrefixOfAnyWorkspacePattern(slashRel string, patterns []workspacePatternMatcher) bool {
	for _, pm := range patterns {
		p := pm.raw
		base := strings.TrimSuffix(strings.TrimSuffix(p, "/**"), "/*")
		if idx := strings.IndexAny(base, "*?[{"); idx >= 0 {
			base = stdpath.Dir(base[:idx])
			if base == "." || base == "" {
				return true
			}
		}
		if base == slashRel || strings.HasPrefix(base, slashRel+"/") {
			return true
		}
	}
	return false
}

func isDefaultSkippedName(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "bashtion-") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

func hasDefaultSkippedAncestor(slashRel string) bool {
	if slashRel == "" || slashRel == "." {
		return false
	}
	for _, seg := range strings.Split(slashRel, "/") {
		if isDefaultSkippedName(seg) {
			return true
		}
	}
	return false
}

func isAncestorRel(dirRel, fileRel string) bool {
	if dirRel == "" || fileRel == "" {
		return false
	}
	return fileRel == dirRel || strings.HasPrefix(fileRel, dirRel+string(filepath.Separator))
}

func buildWorkspaceManifest(srcDir string, opts manifestOptions) (*workspaceManifest, error) {
	m := &workspaceManifest{
		knownRel: map[string]bool{
			".": true,
		},
	}
	if opts.skipRelTarget != "" {
		m.knownRel[opts.skipRelTarget] = true
	}
	includes := normalizeWorkspacePatterns(opts.includes)
	excludes := normalizeWorkspacePatterns(opts.excludes)
	maxFiles := opts.maxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxWorkspaceFiles
	}
	maxBytes := opts.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxWorkspaceBytes
	}

	realSrcRoot, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		realSrcRoot = filepath.Clean(srcDir)
	}
	var (
		totalCachedBytes   int64
		totalWorkspaceSize int64
		fileCount          int
		skippedDefaultDirs []string
	)
	recordFileBudget := func(size int64) error {
		fileCount++
		if fileCount > maxFiles {
			return fmt.Errorf("workspace file count exceeds limit (%d > %d max files); narrow root with --workspace or exclude paths with --workspace-exclude", fileCount, maxFiles)
		}
		totalWorkspaceSize += size
		if totalWorkspaceSize > maxBytes {
			return fmt.Errorf("workspace copy size exceeds limit (%d > %d bytes); narrow root with --workspace or exclude paths with --workspace-exclude", totalWorkspaceSize, maxBytes)
		}
		return nil
	}

	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)

		if d.IsDir() {
			mustKeep := isAncestorRel(rel, opts.skipRelTarget) || isAncestorRel(rel, opts.relTest)
			if !mustKeep {
				for keepRel := range opts.keepRelPaths {
					if isAncestorRel(rel, keepRel) {
						mustKeep = true
						break
					}
				}
			}
			if !mustKeep && matchesAnyWorkspacePattern(slashRel, excludes) {
				return filepath.SkipDir
			}
			if !mustKeep && hasDefaultSkippedAncestor(slashRel) {
				if !matchesAnyWorkspacePattern(slashRel, includes) && !isPrefixOfAnyWorkspacePattern(slashRel, includes) {
					if !hasDefaultSkippedAncestor(stdpath.Dir(slashRel)) && !strings.HasPrefix(d.Name(), "bashtion-") {
						skippedDefaultDirs = append(skippedDefaultDirs, slashRel)
					}
					return filepath.SkipDir
				}
			}
			m.dirs = append(m.dirs, rel)
			m.knownRel[rel] = true
			return nil
		}

		baseName := d.Name()
		if (opts.skipRelTarget != "" && rel == opts.skipRelTarget) || baseName == cache.DefaultFileName || baseName == ".gomutants-cache.json" || baseName == "mutation-report.json" {
			return nil
		}
		if rel != opts.relTest && !opts.keepRelPaths[rel] {
			if matchesAnyWorkspacePattern(slashRel, excludes) {
				return nil
			}
			parentSlash := stdpath.Dir(slashRel)
			if hasDefaultSkippedAncestor(parentSlash) && !matchesAnyWorkspacePattern(slashRel, includes) {
				return nil
			}
		}

		if d.Type()&fs.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				m.warnings = append(m.warnings, fmt.Sprintf("sandbox skipped unreadable symlink %q: %v", slashRel, err))
				return nil
			}
			resolvedTarget, evalErr := filepath.EvalSymlinks(path)
			if evalErr != nil {
				resolvedTarget = linkTarget
				if !filepath.IsAbs(resolvedTarget) {
					resolvedTarget = filepath.Clean(filepath.Join(filepath.Dir(path), linkTarget))
				}
			}
			relToRoot, relErr := filepath.Rel(realSrcRoot, resolvedTarget)
			escapesRoot := filepath.IsAbs(linkTarget) || evalErr != nil || relErr != nil || !filepath.IsLocal(relToRoot)
			if !escapesRoot {
				if err := recordFileBudget(0); err != nil {
					return err
				}
				m.files = append(m.files, manifestFile{
					srcPath:    path,
					relPath:    rel,
					isSymlink:  true,
					linkTarget: linkTarget,
				})
				m.knownRel[rel] = true
				return nil
			}
			// Symlink points outside workspace root: materialize regular file copy to protect host files and avoid broken relative links
			targetInfo, err := os.Stat(path)
			if err != nil || !targetInfo.Mode().IsRegular() || targetInfo.Size() > maxSandboxFileBytes {
				m.warnings = append(m.warnings, fmt.Sprintf("sandbox skipped symlink %q -> %q (broken, non-regular/directory outside root, or exceeds %d byte limit)", slashRel, linkTarget, maxSandboxFileBytes))
				return nil
			}
			if err := recordFileBudget(targetInfo.Size()); err != nil {
				return err
			}
			mf := manifestFile{
				srcPath: path,
				relPath: rel,
				mode:    targetInfo.Mode(),
				size:    targetInfo.Size(),
			}
			if targetInfo.Size() <= maxCachedFileBytes && totalCachedBytes+targetInfo.Size() <= maxTotalManifestCacheMem {
				if data, err := os.ReadFile(path); err == nil {
					mf.data = data
					mf.hasData = true
					totalCachedBytes += targetInfo.Size()
				}
			} else {
				mf.readOnly = true
				m.warnings = append(m.warnings, fmt.Sprintf("sandbox provisioned %q as read-only (exceeded memory cache threshold)", slashRel))
			}
			m.warnings = append(m.warnings, fmt.Sprintf("sandbox materialized external symlink %q -> %q as a regular file copy", slashRel, linkTarget))
			m.files = append(m.files, mf)
			m.knownRel[rel] = true
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxSandboxFileBytes {
			m.warnings = append(m.warnings, fmt.Sprintf("sandbox skipped large file %q (%d bytes > %d byte limit)", slashRel, info.Size(), maxSandboxFileBytes))
			return nil
		}
		if err := recordFileBudget(info.Size()); err != nil {
			return err
		}
		mf := manifestFile{
			srcPath: path,
			relPath: rel,
			mode:    info.Mode(),
			size:    info.Size(),
		}
		if info.Size() <= maxCachedFileBytes && totalCachedBytes+info.Size() <= maxTotalManifestCacheMem {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			mf.data = data
			mf.hasData = true
			totalCachedBytes += info.Size()
		} else {
			mf.readOnly = true
			m.warnings = append(m.warnings, fmt.Sprintf("sandbox provisioned %q as read-only (exceeded memory cache threshold)", slashRel))
		}
		m.files = append(m.files, mf)
		m.knownRel[rel] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(skippedDefaultDirs) > 0 {
		sort.Strings(skippedDefaultDirs)
		m.warnings = append([]string{
			fmt.Sprintf("sandbox skipped default-excluded directories [%s] (use --workspace-include to include any if tests require them)", strings.Join(skippedDefaultDirs, ", ")),
		}, m.warnings...)
	}
	return m, nil
}

func provisionWorkerDir(m *workspaceManifest, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, relDir := range m.dirs {
		if err := os.MkdirAll(filepath.Join(dstDir, relDir), 0o755); err != nil {
			return err
		}
	}
	return restoreWorkerFiles(m, dstDir, "")
}

func restoreWorkerFiles(m *workspaceManifest, dstDir, skipRelTarget string) error {
	if len(m.knownRel) > 0 {
		permErrHit := false
		_ = filepath.WalkDir(dstDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					permErrHit = true
				}
				return nil
			}
			rel, relErr := filepath.Rel(dstDir, path)
			if relErr != nil || rel == "." {
				return nil
			}
			if skipRelTarget != "" && rel == skipRelTarget {
				return nil
			}
			if !m.knownRel[rel] {
				if rmErr := removeAllWithChmodRetry(path); rmErr != nil && errors.Is(rmErr, fs.ErrPermission) {
					permErrHit = true
				}
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return nil
		})
		if permErrHit {
			_ = makeWritableTree(dstDir)
			_ = filepath.WalkDir(dstDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				rel, relErr := filepath.Rel(dstDir, path)
				if relErr != nil || rel == "." {
					return nil
				}
				if skipRelTarget != "" && rel == skipRelTarget {
					return nil
				}
				if !m.knownRel[rel] {
					_ = removeAllWithChmodRetry(path)
					if d.IsDir() {
						return filepath.SkipDir
					}
				}
				return nil
			})
		}
	}
	for _, relDir := range m.dirs {
		dirPath := filepath.Join(dstDir, relDir)
		if info, err := os.Lstat(dirPath); err == nil && info.IsDir() {
			if info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(dirPath, info.Mode().Perm()|0o700)
			}
			continue
		} else if err == nil {
			if rmErr := removeWithChmodRetry(dirPath); rmErr != nil {
				return rmErr
			}
		}
		if err := os.MkdirAll(dirPath, 0o755); err != nil {
			return err
		}
	}
	for _, f := range m.files {
		if skipRelTarget != "" && f.relPath == skipRelTarget {
			continue
		}
		dstPath := filepath.Join(dstDir, f.relPath)
		if f.isSymlink {
			if curTarget, err := os.Readlink(dstPath); err == nil && curTarget == f.linkTarget {
				continue
			}
			_ = removeAllWithChmodRetry(dstPath)
			if err := os.Symlink(f.linkTarget, dstPath); err != nil {
				return err
			}
			continue
		}
		if f.readOnly && !f.hasData {
			wantPerm := f.mode.Perm() &^ 0o222
			if info, err := os.Lstat(dstPath); err == nil {
				if info.Mode().IsRegular() && info.Size() == f.size {
					if info.Mode().Perm() != wantPerm {
						if chmodErr := os.Chmod(dstPath, wantPerm); chmodErr != nil {
							return chmodErr
						}
					}
					continue
				}
				_ = removeAllWithChmodRetry(dstPath)
			}
			data, err := os.ReadFile(f.srcPath)
			if err != nil {
				return err
			}
			if err := writeFileWithChmodRetry(dstPath, data, wantPerm); err != nil {
				return err
			}
			continue
		}
		data := f.data
		if !f.hasData {
			var err error
			data, err = os.ReadFile(f.srcPath)
			if err != nil {
				return err
			}
		}
		if info, err := os.Lstat(dstPath); err == nil {
			if info.Mode().IsRegular() && info.Size() == int64(len(data)) {
				if cur, readErr := os.ReadFile(dstPath); readErr == nil && bytes.Equal(cur, data) {
					if info.Mode().Perm() != f.mode.Perm() {
						if chmodErr := os.Chmod(dstPath, f.mode.Perm()); chmodErr != nil {
							return chmodErr
						}
					}
					continue
				}
			}
			if !info.Mode().IsRegular() {
				_ = removeAllWithChmodRetry(dstPath)
			}
		}
		if err := writeFileWithChmodRetry(dstPath, data, f.mode); err != nil {
			return err
		}
		if err := os.Chmod(dstPath, f.mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func makeWritableTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		info, lstatErr := os.Lstat(p)
		if lstatErr != nil {
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		perm := info.Mode().Perm()
		if info.IsDir() {
			if perm&0o700 != 0o700 {
				_ = os.Chmod(p, perm|0o700)
			}
		} else if perm&0o600 != 0o600 {
			_ = os.Chmod(p, perm|0o600)
		}
		return nil
	})
}

func removeAllWithChmodRetry(path string) error {
	err := os.RemoveAll(path)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		_ = makeWritableTree(filepath.Dir(path))
		_ = makeWritableTree(path)
		return os.RemoveAll(path)
	}
	return err
}

func removeWithChmodRetry(path string) error {
	err := os.Remove(path)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		_ = os.Chmod(filepath.Dir(path), 0o755)
		_ = os.Chmod(path, 0o600)
		return os.Remove(path)
	}
	return err
}

func writeFileWithChmodRetry(path string, data []byte, perm fs.FileMode) error {
	err := os.WriteFile(path, data, perm)
	if err != nil && errors.Is(err, fs.ErrPermission) {
		if info, statErr := os.Lstat(filepath.Dir(path)); statErr == nil && info.IsDir() {
			_ = os.Chmod(filepath.Dir(path), info.Mode().Perm()|0o700)
		}
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			_ = os.Chmod(path, info.Mode().Perm()|0o600)
		}
		return os.WriteFile(path, data, perm)
	}
	return err
}

func safeTestArg(testPath string) string {
	if strings.HasPrefix(testPath, "-") {
		return "./" + testPath
	}
	return testPath
}

func isFilesystemRoot(p string) bool {
	clean := filepath.Clean(p)
	vol := filepath.VolumeName(clean) + string(filepath.Separator)
	return clean == string(filepath.Separator) || clean == vol
}

func sameCanonicalPath(a, b string) bool {
	ca, errA := filepath.EvalSymlinks(a)
	if errA != nil {
		ca = filepath.Clean(a)
	}
	cb, errB := filepath.EvalSymlinks(b)
	if errB != nil {
		cb = filepath.Clean(b)
	}
	return ca == cb
}

func isHomeDir(p string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return sameCanonicalPath(p, home)
}

func gitToplevelDir(ctx context.Context, dir string) string {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probeCtx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return ""
	}
	return filepath.Clean(top)
}

func checkFilesInsideRoot(root, absTarget, absTest string) (string, string, bool) {
	relTarget, err1 := filepath.Rel(root, absTarget)
	relTest, err2 := filepath.Rel(root, absTest)
	if err1 == nil && err2 == nil && filepath.IsLocal(relTarget) && filepath.IsLocal(relTest) {
		return relTarget, relTest, true
	}
	// Retry after EvalSymlinks (handles /var -> /private/var on macOS).
	cRoot, eR := filepath.EvalSymlinks(root)
	cTarget, eT := filepath.EvalSymlinks(absTarget)
	cTest, eB := filepath.EvalSymlinks(absTest)
	if eR == nil && eT == nil && eB == nil {
		rT, err1 := filepath.Rel(cRoot, cTarget)
		rB, err2 := filepath.Rel(cRoot, cTest)
		if err1 == nil && err2 == nil && filepath.IsLocal(rT) && filepath.IsLocal(rB) {
			return rT, rB, true
		}
	}
	return "", "", false
}

func checkTargetAndTestsInsideRoot(root, absTarget string, absTests []string) (string, []string, bool) {
	relTarget, _, ok := checkFilesInsideRoot(root, absTarget, absTarget)
	if !ok {
		return "", nil, false
	}
	relTests := make([]string, len(absTests))
	for i, absTest := range absTests {
		_, relTest, testOK := checkFilesInsideRoot(root, absTarget, absTest)
		if !testOK {
			return "", nil, false
		}
		relTests[i] = relTest
	}
	return relTarget, relTests, true
}

func resolveSafeWorkspaceRootMultiTests(ctx context.Context, absWorkDir, absWorkspaceDir, absTarget string, absTests []string) (string, string, []string, error) {
	if absWorkspaceDir != "" {
		info, err := os.Stat(absWorkspaceDir)
		if err != nil {
			return "", "", nil, fmt.Errorf("invalid --workspace %q: %w", absWorkspaceDir, err)
		}
		if !info.IsDir() {
			return "", "", nil, fmt.Errorf("--workspace %q must be a directory", absWorkspaceDir)
		}
		if isFilesystemRoot(absWorkspaceDir) {
			return "", "", nil, fmt.Errorf("refusing filesystem root %q as workspace", absWorkspaceDir)
		}
		if relT, relBs, ok := checkTargetAndTestsInsideRoot(absWorkspaceDir, absTarget, absTests); ok {
			return absWorkspaceDir, relT, relBs, nil
		}
		return "", "", nil, fmt.Errorf("target %q and test(s) %v must reside within --workspace %q", absTarget, absTests, absWorkspaceDir)
	}

	if isFilesystemRoot(absWorkDir) {
		return "", "", nil, fmt.Errorf("refusing filesystem root %q as implicit workspace root; pass --workspace", absWorkDir)
	}

	if relT, relBs, ok := checkTargetAndTestsInsideRoot(absWorkDir, absTarget, absTests); ok {
		if isHomeDir(absWorkDir) {
			return "", "", nil, fmt.Errorf("refusing home directory %q as implicit workspace root; run inside a project directory or pass --workspace", absWorkDir)
		}
		return absWorkDir, relT, relBs, nil
	}

	// Fallback only to the enclosing git repository toplevel (never arbitrary ancestors such as $HOME or /).
	for _, probeDir := range []string{absWorkDir, filepath.Dir(absTarget)} {
		gitTop := gitToplevelDir(ctx, probeDir)
		if gitTop == "" || isFilesystemRoot(gitTop) || isHomeDir(gitTop) {
			continue
		}
		if relT, relBs, ok := checkTargetAndTestsInsideRoot(gitTop, absTarget, absTests); ok {
			return gitTop, relT, relBs, nil
		}
	}

	return "", "", nil, fmt.Errorf("target %q and test(s) %v must reside within workspace %q (or a shared git repository; pass --workspace to specify root explicitly)", absTarget, absTests, absWorkDir)
}

func resolveSafeMultiWorkspaceRoot(ctx context.Context, absWorkDir, absWorkspaceDir string, absPaths []string) (string, error) {
	allInside := func(root string) bool {
		for _, p := range absPaths {
			if _, _, ok := checkFilesInsideRoot(root, p, p); !ok {
				return false
			}
		}
		return true
	}

	if absWorkspaceDir != "" {
		info, err := os.Stat(absWorkspaceDir)
		if err != nil {
			return "", fmt.Errorf("invalid --workspace %q: %w", absWorkspaceDir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("--workspace %q must be a directory", absWorkspaceDir)
		}
		if isFilesystemRoot(absWorkspaceDir) {
			return "", fmt.Errorf("refusing filesystem root %q as workspace", absWorkspaceDir)
		}
		if allInside(absWorkspaceDir) {
			return absWorkspaceDir, nil
		}
		return "", fmt.Errorf("all target and test files must reside within --workspace %q", absWorkspaceDir)
	}

	if !isFilesystemRoot(absWorkDir) && !isHomeDir(absWorkDir) && allInside(absWorkDir) {
		return absWorkDir, nil
	}

	probeDirs := []string{absWorkDir}
	if len(absPaths) > 0 {
		probeDirs = append(probeDirs, filepath.Dir(absPaths[0]))
	}
	for _, probeDir := range probeDirs {
		gitTop := gitToplevelDir(ctx, probeDir)
		if gitTop == "" || isFilesystemRoot(gitTop) || isHomeDir(gitTop) {
			continue
		}
		if allInside(gitTop) {
			return gitTop, nil
		}
	}

	return "", fmt.Errorf("all target and test files must reside within workspace %q (or a shared git repository; pass --workspace to specify root explicitly)", absWorkDir)
}

func absOrJoin(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func serializeTestFilesForCache(relTests []string, contents [][]byte) []byte {
	type entry struct {
		path string
		data []byte
	}
	entries := make([]entry, len(relTests))
	for i, p := range relTests {
		var d []byte
		if i < len(contents) {
			d = contents[i]
		}
		entries[i] = entry{path: filepath.ToSlash(filepath.Clean(p)), data: d}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})
	var buf bytes.Buffer
	_, _ = fmt.Fprintf(&buf, "tests=%d\n", len(entries))
	for _, e := range entries {
		_, _ = fmt.Fprintf(&buf, "file=%d:%s\nlen=%d\n", len(e.path), e.path, len(e.data))
		buf.Write(e.data)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func buildCacheMeta(ctx context.Context, cfg Config, m *workspaceManifest, skipRelTarget string, coverageActive, probeVersions bool) []string {
	meta := []string{
		"tool_version=" + cfg.ToolVersion,
		"manifest=" + computeManifestHash(m, skipRelTarget),
		"timeout=" + cfg.Timeout.String(),
		"disable_filter=" + strconv.FormatBool(cfg.DisableTestFilter),
		"disable_coverage=" + strconv.FormatBool(cfg.DisableCoverage),
		"coverage_active=" + strconv.FormatBool(coverageActive),
		"bats_bin=" + cfg.BatsBin,
		"runtime=" + computeRuntimeSignature(ctx, cfg.Workspace, cfg.BatsBin, probeVersions),
	}
	for _, arg := range cfg.BatsArgs {
		meta = append(meta, "bats_arg="+arg)
	}
	for _, inc := range normalizeWorkspacePatterns(cfg.WorkspaceIncludes) {
		meta = append(meta, "ws_inc="+inc.raw)
	}
	for _, exc := range normalizeWorkspacePatterns(cfg.WorkspaceExcludes) {
		meta = append(meta, "ws_exc="+exc.raw)
	}
	return meta
}

func computeManifestHash(m *workspaceManifest, skipRelTarget string) string {
	h := sha256.New()
	if m == nil {
		return ""
	}
	skipSlash := ""
	if skipRelTarget != "" {
		skipSlash = filepath.ToSlash(skipRelTarget)
	}
	dirs := make([]string, len(m.dirs))
	for i, d := range m.dirs {
		dirs[i] = filepath.ToSlash(d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		_, _ = fmt.Fprintf(h, "d:%d:%s\n", len(d), d)
	}

	files := make([]manifestFile, len(m.files))
	copy(files, m.files)
	sort.Slice(files, func(i, j int) bool {
		return filepath.ToSlash(files[i].relPath) < filepath.ToSlash(files[j].relPath)
	})

	for _, f := range files {
		rel := filepath.ToSlash(f.relPath)
		if skipSlash != "" && rel == skipSlash {
			continue
		}
		if f.isSymlink {
			_, _ = fmt.Fprintf(h, "l:%d:%s->%d:%s\n", len(rel), rel, len(f.linkTarget), f.linkTarget)
			continue
		}
		_, _ = fmt.Fprintf(h, "f:%d:%s:mode=%o:size=%d:", len(rel), rel, f.mode.Perm(), f.size)
		if f.hasData {
			_, _ = h.Write(f.data)
		} else if f.srcPath != "" {
			if fh, err := os.Open(f.srcPath); err == nil {
				_, _ = io.Copy(h, fh)
				_ = fh.Close()
			}
		}
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func computeRuntimeSignature(ctx context.Context, ws *WorkspaceSession, batsBin string, probeVersions bool) string {
	h := sha256.New()
	envKeys := []string{"PATH", "HOME", "BASH_ENV", "ENV", "CDPATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "CI"}
	for _, k := range envKeys {
		if v, ok := os.LookupEnv(k); ok {
			_, _ = fmt.Fprintf(h, "env:%s=%d:%s\n", k, len(v), v)
		}
	}
	var batsEnv []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BATS_") {
			batsEnv = append(batsEnv, kv)
		}
	}
	sort.Strings(batsEnv)
	for _, kv := range batsEnv {
		_, _ = fmt.Fprintf(h, "benv:%d:%s\n", len(kv), kv)
	}

	if probeVersions {
		batsVer, bashVer := ws.probeToolVersions(ctx, batsBin)
		if batsVer != "" {
			_, _ = fmt.Fprintf(h, "bats_ver:%d:%s\n", len(batsVer), batsVer)
		}
		if bashVer != "" {
			_, _ = fmt.Fprintf(h, "bash_ver:%d:%s\n", len(bashVer), bashVer)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func runVersionProbes(ctx context.Context, batsBin string) (batsVer, bashVer string) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if batsOut, err := exec.CommandContext(probeCtx, batsBin, "--version").Output(); err == nil {
		batsVer = strings.TrimSpace(string(batsOut))
	}
	if bashOut, err := exec.CommandContext(probeCtx, "bash", "--version").Output(); err == nil {
		bashVer = strings.TrimSpace(string(bashOut))
	}
	return batsVer, bashVer
}
