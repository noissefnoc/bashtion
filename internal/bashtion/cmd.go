//go:build unix

package bashtion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/noissefnoc/bashtion/internal/cache"
	"github.com/noissefnoc/bashtion/internal/cli"
	"github.com/noissefnoc/bashtion/internal/discovery"
	"github.com/noissefnoc/bashtion/internal/gitdiff"
	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/report"
	"github.com/noissefnoc/bashtion/internal/runner"
)

// Exit codes conforming to standard Unix CLI conventions.
const (
	ExitSuccess    = 0
	ExitFailure    = 1
	ExitUsageError = 2
	ExitInterrupt  = 130
)

// Version holds the application version, optionally injected at link time.
var Version = "dev"

// ChangedLinesResolver abstracts git diff line resolution for unit testability.
type ChangedLinesResolver func(ctx context.Context, workDir, ref, targetFile string) (map[uint]bool, error)

// Deps allows injecting external dependencies (such as CommandExecutor, ChangedLinesResolver, or WorkDir) during tests.
type Deps struct {
	WorkDir      string
	Executor     runner.CommandExecutor
	ChangedLines ChangedLinesResolver
}

// MainCommandByArgs parses CLI arguments and executes the main command, returning an exit status.
func MainCommandByArgs(args []string, inout *cli.ProcInout) int {
	opts, err := ParseOptions(args, inout)
	if err != nil {
		fmt.Fprintf(inout.Stderr, "error: %v\n", err)
		return ExitUsageError
	}

	if opts.Help {
		return ExitSuccess
	}

	if opts.Version {
		fmt.Fprintf(inout.Stdout, "bashtion version %s\n", effectiveToolVersion())
		return ExitSuccess
	}

	if opts.Guide {
		fmt.Fprint(inout.Stdout, AgentGuide)
		return ExitSuccess
	}

	if err := MainCommandByOptions(opts, inout); err != nil {
		fmt.Fprintf(inout.Stderr, "error: %v\n", err)
		if errors.Is(err, context.Canceled) {
			return ExitInterrupt
		}
		return ExitFailure
	}

	return ExitSuccess
}

// MainCommandByOptions executes the mutation testing command with parsed Options.
func MainCommandByOptions(opts *Options, inout *cli.ProcInout) error {
	return MainCommandWithDeps(opts, inout, Deps{})
}

// MainCommandWithDeps executes the mutation testing command with explicit dependencies.
func MainCommandWithDeps(opts *Options, inout *cli.ProcInout, deps Deps) error {
	if opts.Version {
		fmt.Fprintf(inout.Stdout, "bashtion version %s\n", effectiveToolVersion())
		return nil
	}
	if opts.Guide {
		fmt.Fprint(inout.Stdout, AgentGuide)
		return nil
	}

	pairs, err := discovery.ResolvePairs(deps.WorkDir, opts.TargetPatterns(), opts.TestPatterns(), opts.Mappings, opts.List)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	format := opts.Format
	if format == "" {
		format = report.FormatText
	}

	type preparedTarget struct {
		pair    discovery.TargetPair
		origSrc []byte
		mutants []mutator.Mutant
	}

	prepared := make([]preparedTarget, 0, len(pairs))
	origSrcByFile := make(map[string][]byte, len(pairs))
	var defaultOrigSrc []byte
	nextMutantID := 1

	for _, pair := range pairs {
		readPath := pair.TargetFile
		if deps.WorkDir != "" && !filepath.IsAbs(readPath) {
			readPath = filepath.Join(deps.WorkDir, readPath)
		}

		origSrc, err := os.ReadFile(readPath)
		if err != nil {
			return fmt.Errorf("reading target file: %w", err)
		}
		origSrcByFile[pair.TargetFile] = origSrc
		if defaultOrigSrc == nil {
			defaultOrigSrc = origSrc
		}

		var allowedLines map[uint]bool
		if opts.GitDiff != "" {
			resolver := deps.ChangedLines
			if resolver == nil {
				resolver = gitdiff.ChangedLines
			}
			allowedLines, err = resolver(ctx, deps.WorkDir, opts.GitDiff, pair.TargetFile)
			if err != nil {
				return fmt.Errorf("resolving git diff lines: %w", err)
			}
		}

		mutants, err := mutator.GenerateWithOptions(pair.TargetFile, origSrc, mutator.GenerateOptions{
			DisabledCategories: opts.DisabledCategories,
			AllowedLines:       allowedLines,
		})
		if err != nil {
			return fmt.Errorf("generating mutants: %w", err)
		}

		if len(pairs) > 1 {
			for i := range mutants {
				mutants[i].ID = nextMutantID
				nextMutantID++
			}
		}

		if opts.List {
			fmt.Fprintf(inout.Stdout, "Target: %s (generated %d AST mutants)\n", pair.TargetFile, len(mutants))
			for _, m := range mutants {
				fmt.Fprintf(inout.Stdout, "  #%02d [%s] key=%s %s:%d:%d  %s (%q -> %q)\n",
					m.ID, m.Category, m.Key(), m.FilePath, m.Line, m.Col, m.Description, m.Original, m.Replacement)
			}
			continue
		}

		prepared = append(prepared, preparedTarget{
			pair:    pair,
			origSrc: origSrc,
			mutants: mutants,
		})
	}

	if opts.List {
		return nil
	}

	// Setup incremental cache (enabled by default unless --no-cache is set)
	var cacheStore *cache.Store
	var cachePath string
	if !opts.NoCache {
		cachePath = resolveCacheFilePath(deps.WorkDir, opts.CacheFile, deps.Executor == nil)
		if cachePath != "" {
			var loadErr error
			cacheStore, loadErr = cache.Load(cachePath)
			if loadErr != nil {
				fmt.Fprintf(inout.Stderr, "warning: failed to read cache file %q (%v); pass --cache-file <writable-path>, set XDG_CACHE_HOME, or pass --no-cache to disable caching\n", cachePath, loadErr)
			}
		}
	}

	overallStart := time.Now()
	var targetLabels []string
	var testLabels []string
	seenTests := make(map[string]bool)
	seenWarnings := make(map[string]bool)
	combined := &runner.Summary{
		CacheEnabled: !opts.NoCache,
		CacheFile:    cachePath,
	}

	wsExcludes := append([]string(nil), opts.WorkspaceExclude...)
	if opts.JSONOut != "" {
		if relOut := relativeInsideWorkDir(deps.WorkDir, opts.JSONOut); relOut != "" {
			wsExcludes = append(wsExcludes, relOut)
		}
	}

	totalAllMutants := 0
	targetFiles := make([]string, 0, len(prepared))
	var testFiles []string
	for _, pt := range prepared {
		totalAllMutants += len(pt.mutants)
		targetFiles = append(targetFiles, pt.pair.TargetFile)
		testFiles = append(testFiles, pt.pair.TestFiles...)
	}
	overallCompleted := 0

	var wsSession *runner.WorkspaceSession
	if len(prepared) > 1 {
		var wsErr error
		wsSession, wsErr = runner.NewWorkspaceSession(ctx, runner.WorkspaceConfig{
			WorkDir:           deps.WorkDir,
			WorkspaceDir:      opts.Workspace,
			TargetFiles:       targetFiles,
			TestFiles:         testFiles,
			WorkspaceIncludes: opts.WorkspaceInclude,
			WorkspaceExcludes: wsExcludes,
		})
		if wsErr != nil {
			return fmt.Errorf("failed to initialize shared workspace: %w", wsErr)
		}
		defer func() {
			_ = wsSession.Close()
		}()
	}

	useColor := cli.SupportsColor(inout.Stderr)
	emitProgress := shouldEmitProgress(format, opts.Progress, opts.Quiet, inout.Stderr)

	for pairIdx, pt := range prepared {
		targetLabels = append(targetLabels, pt.pair.TargetFile)
		for _, tf := range pt.pair.TestFiles {
			if !seenTests[tf] {
				seenTests[tf] = true
				testLabels = append(testLabels, tf)
			}
		}

		var onProgress func(completed, total int, res runner.Result)
		if emitProgress {
			fmt.Fprintf(inout.Stderr, "Target: %s (generated %d AST mutants)\n", pt.pair.TargetFile, len(pt.mutants))
			fmt.Fprintf(inout.Stderr, "Test:   %s (jobs: %d)\n", strings.Join(pt.pair.TestFiles, ", "), opts.Jobs)
			if pairIdx == 0 {
				if opts.NoCache {
					fmt.Fprintf(inout.Stderr, "Cache:  disabled (--no-cache)\n")
				} else if cachePath != "" {
					fmt.Fprintf(inout.Stderr, "Cache:  %s\n", cachePath)
				}
			}
			fmt.Fprintln(inout.Stderr)
			onProgress = func(completed, total int, res runner.Result) {
				m := res.Mutant
				statusLabel := string(res.Status)
				if res.Status == runner.StatusUncovered || res.Uncovered {
					statusLabel = "UNCOVERED"
				}
				if res.Cached {
					statusLabel += "*"
				}
				displayStatus := formatProgressStatus(statusLabel, res, useColor)
				displayCompleted := completed
				displayTotal := total
				if len(prepared) > 1 {
					displayCompleted = overallCompleted + completed
					displayTotal = totalAllMutants
				}
				fmt.Fprintf(inout.Stderr, "[%02d/%02d] %s (%4dms) #%02d %s:%d:%d [%s] %s\n",
					displayCompleted, displayTotal,
					displayStatus,
					res.Duration.Milliseconds(),
					m.ID,
					m.FilePath, m.Line, m.Col,
					m.Category,
					m.Description,
				)
			}
		}

		cfg := runner.Config{
			WorkDir:           deps.WorkDir,
			WorkspaceDir:      opts.Workspace,
			WorkspaceIncludes: opts.WorkspaceInclude,
			WorkspaceExcludes: wsExcludes,
			Workspace:         wsSession,
			TargetFile:        pt.pair.TargetFile,
			TestFile:          strings.Join(pt.pair.TestFiles, ", "),
			TestFiles:         pt.pair.TestFiles,
			BatsBin:           opts.BatsBin,
			BatsArgs:          opts.BatsArgs,
			Jobs:              opts.Jobs,
			Timeout:           opts.Timeout,
			DisableCoverage:   opts.NoCoverage,
			NoCache:           opts.NoCache,
			CacheStore:        cacheStore,
			ToolVersion:       effectiveToolVersion(),
			Executor:          deps.Executor,
			OnProgress:        onProgress,
		}

		sub, err := runner.Run(ctx, cfg, pt.mutants)
		if err != nil {
			return fmt.Errorf("mutation run failed: %w", err)
		}
		for _, w := range sub.Warnings {
			if !seenWarnings[w] {
				seenWarnings[w] = true
				fmt.Fprintf(inout.Stderr, "warning: %s\n", w)
			}
		}

		combined.BaselineDuration += sub.BaselineDuration
		combined.WorkspaceSetupDuration += sub.WorkspaceSetupDuration
		combined.MutantEvalDuration += sub.MutantEvalDuration
		if sub.Jobs > combined.Jobs {
			combined.Jobs = sub.Jobs
		}
		if sub.CoverageEnabled {
			combined.CoverageEnabled = true
		}
		combined.Total += sub.Total
		combined.Killed += sub.Killed
		combined.Survived += sub.Survived
		combined.Uncovered += sub.Uncovered
		combined.CacheHits += sub.CacheHits
		combined.Timeout += sub.Timeout
		combined.Errors += sub.Errors

		combined.Results = append(combined.Results, sub.Results...)
		overallCompleted += len(pt.mutants)
	}

	// Best-effort cache save: failures to persist cache (e.g. read-only filesystem or sandbox)
	// emit an actionable warning on stderr without failing the mutation run or affecting exit codes.
	if !opts.NoCache && cacheStore != nil && cachePath != "" {
		if saveErr := cacheStore.Save(cachePath); saveErr != nil {
			fmt.Fprintf(inout.Stderr, "warning: failed to write cache file %q (%v); pass --cache-file <writable-path>, set XDG_CACHE_HOME, or pass --no-cache to disable caching\n", cachePath, saveErr)
		}
	}

	if combined.Jobs == 0 {
		combined.Jobs = opts.Jobs
	}
	combined.TargetFile = strings.Join(targetLabels, ", ")
	combined.TestFile = strings.Join(testLabels, ", ")
	combined.TestFiles = testLabels
	combined.AllowZeroMutants = opts.GitDiff != ""
	combined.TotalDuration = time.Since(overallStart)

	if err := report.WriteMulti(inout.Stdout, format, combined, origSrcByFile, defaultOrigSrc, opts.MinScore); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	if opts.JSONOut != "" {
		jsonOutPath := opts.JSONOut
		if !filepath.IsAbs(jsonOutPath) && deps.WorkDir != "" {
			jsonOutPath = filepath.Join(deps.WorkDir, jsonOutPath)
		}
		var jsonBuf bytes.Buffer
		if err := report.WriteMulti(&jsonBuf, report.FormatJSON, combined, origSrcByFile, defaultOrigSrc, opts.MinScore); err != nil {
			return fmt.Errorf("rendering --json-out report: %w", err)
		}
		if err := writeFileAtomic(jsonOutPath, jsonBuf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("writing --json-out %q: %w", opts.JSONOut, err)
		}
	}

	if opts.MinScore > 0 {
		if combined.Total == 0 {
			if !combined.AllowZeroMutants {
				return fmt.Errorf("no mutants were generated to satisfy required min-score %.1f%%", opts.MinScore)
			}
		} else if combined.Score() < opts.MinScore {
			return fmt.Errorf("mutation score %.1f%% is below required threshold %.1f%%", combined.Score(), opts.MinScore)
		}
	}

	return nil
}

func effectiveToolVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var rev, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		if rev != "" {
			if modified == "true" {
				return "dev-" + rev + "-dirty"
			}
			return "dev-" + rev
		}
	}
	return Version
}

func resolveCacheFilePath(workDir, explicitPath string, isRealRun bool) string {
	if explicitPath != "" {
		if !filepath.IsAbs(explicitPath) && workDir != "" {
			return filepath.Join(workDir, explicitPath)
		}
		return explicitPath
	}
	if !isRealRun {
		return ""
	}
	if xdgPath := cache.ResolveXDGCacheFile(workDir); xdgPath != "" {
		return xdgPath
	}
	if workDir != "" {
		return filepath.Join(workDir, cache.DefaultFileName)
	}
	return cache.DefaultFileName
}

func shouldEmitProgress(format report.Format, mode ProgressMode, quiet bool, stderr io.Writer) bool {
	if quiet || mode == ProgressNever {
		return false
	}
	if mode == ProgressAlways {
		return true
	}
	// ProgressAuto (default): emit progress for text/json/markdown; keep llm compact unless stderr is interactive.
	if format == report.FormatLLM {
		return cli.IsInteractive(stderr)
	}
	return true
}

func relativeInsideWorkDir(workDir, p string) string {
	base := workDir
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return ""
		}
		base = wd
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return ""
	}
	absP := p
	if !filepath.IsAbs(absP) {
		absP = filepath.Join(absBase, absP)
	}
	rel, err := filepath.Rel(absBase, absP)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bashtion-out-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func formatProgressStatus(label string, res runner.Result, useColor bool) string {
	padded := fmt.Sprintf("%-10s", label)
	if !useColor {
		return padded
	}
	var color string
	switch {
	case res.Status == runner.StatusUncovered, res.Uncovered, res.Status == runner.StatusSurvived, res.Status == runner.StatusError:
		color = "\x1b[31m"
	case res.Status == runner.StatusKilled:
		color = "\x1b[32m"
	case res.Status == runner.StatusTimeout:
		color = "\x1b[33m"
	default:
		return padded
	}
	return color + padded + "\x1b[0m"
}
