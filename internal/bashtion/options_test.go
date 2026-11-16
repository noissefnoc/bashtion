//go:build unix

package bashtion

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/noissefnoc/bashtion/internal/cli"
	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/report"
)

func TestParseOptions_Success(t *testing.T) {
	defaultJobs := DefaultJobs()

	testCases := []struct {
		Name     string
		Input    []string
		Expected *Options
	}{
		{
			Name:  "help flag",
			Input: []string{"-h"},
			Expected: &Options{
				BatsBin: "bats",
				Jobs:    defaultJobs,
				Format:  report.FormatText,
				Help:    true,
			},
		},
		{
			Name:  "version flag",
			Input: []string{"--version"},
			Expected: &Options{
				BatsBin: "bats",
				Jobs:    defaultJobs,
				Format:  report.FormatText,
				Version: true,
			},
		},
		{
			Name:  "short version flag",
			Input: []string{"-v"},
			Expected: &Options{
				BatsBin: "bats",
				Jobs:    defaultJobs,
				Format:  report.FormatText,
				Version: true,
			},
		},
		{
			Name:  "short flags for target and jobs",
			Input: []string{"-t", "examples/deploy_helper.sh", "--list", "-j", "4"},
			Expected: &Options{
				Target:  "examples/deploy_helper.sh",
				BatsBin: "bats",
				Jobs:    4,
				Format:  report.FormatText,
				List:    true,
			},
		},
		{
			Name:  "list mode requires only target",
			Input: []string{"--target", "examples/deploy_helper.sh", "--list"},
			Expected: &Options{
				Target:  "examples/deploy_helper.sh",
				BatsBin: "bats",
				Jobs:    defaultJobs,
				Format:  report.FormatText,
				List:    true,
			},
		},
		{
			Name: "full test run options with phase 3 flags",
			Input: []string{
				"--target", "a.sh",
				"--test", "a.bats",
				"--bats", "/usr/local/bin/bats",
				"--jobs", "4",
				"--timeout", "3s",
				"--format", "llm",
				"--min-score", "85.5",
				"--git-diff", "HEAD~1",
				"--disable-category", "BOOL_CMD,PARAM_DEFAULT",
			},
			Expected: &Options{
				Target:             "a.sh",
				Test:               "a.bats",
				BatsBin:            "/usr/local/bin/bats",
				Jobs:               4,
				Timeout:            3 * time.Second,
				Format:             report.FormatLLM,
				MinScore:           85.5,
				GitDiff:            "HEAD~1",
				DisabledCategories: []mutator.Category{mutator.CategoryBoolCmd, mutator.CategoryParamExp},
			},
		},
		{
			Name: "bats-args pass-through",
			Input: []string{
				"--target", "a.sh",
				"--test", "a.bats",
				"--bats-args", "--timing --filter-tags 'unit and not slow'",
			},
			Expected: &Options{
				Target:   "a.sh",
				Test:     "a.bats",
				BatsBin:  "bats",
				BatsArgs: []string{"--timing", "--filter-tags", "unit and not slow"},
				Jobs:     defaultJobs,
				Format:   report.FormatText,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			opts, err := ParseOptions(tc.Input, &cli.ProcInout{
				Stdin:  strings.NewReader(""),
				Stdout: stdout,
				Stderr: stderr,
			})
			if err != nil {
				t.Fatalf("failed to parse options: %v", err)
			}

			if !reflect.DeepEqual(opts, tc.Expected) {
				t.Error(cmp.Diff(opts, tc.Expected))
			}
		})
	}
}

func TestParseOptions_WithConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ".bashtion.toml")
	cfgContent := `# sample config
target = "examples/deploy_helper.sh"
test = "examples/deploy_helper.bats"
jobs = 2
timeout = "4s"
format = "llm"
min_score = 90.0
disabled_categories = ["BOOL_CMD"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := ParseOptions([]string{"--config", cfgPath, "--jobs", "6"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("ParseOptions with config failed: %v", err)
	}

	// CLI --jobs 6 should override config's jobs: 2
	if opts.Target != "examples/deploy_helper.sh" || opts.Test != "examples/deploy_helper.bats" || opts.Jobs != 6 || opts.Timeout != 4*time.Second {
		t.Errorf("unexpected merged options: %+v", opts)
	}
	if opts.Format != report.FormatLLM || opts.MinScore != 90 {
		t.Errorf("unexpected format/min_score: %+v", opts)
	}
	if len(opts.DisabledCategories) != 1 || opts.DisabledCategories[0] != mutator.CategoryBoolCmd {
		t.Errorf("unexpected disabled categories: %+v", opts.DisabledCategories)
	}
}

func TestParseOptions_XDGConfigFallback(t *testing.T) {
	tmpDir := t.TempDir()
	xdgDir := filepath.Join(tmpDir, "xdg_config", "bashtion")
	if err := os.MkdirAll(xdgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(xdgDir, "config.toml")
	cfgContent := `target = "xdg_target.sh"
test = "xdg_test.bats"
bats_args = ["-T", "--timing"]
jobs = 3
`
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg_config"))

	opts, err := ParseOptions([]string{}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("ParseOptions with XDG config failed: %v", err)
	}
	if opts.Target != "xdg_target.sh" || opts.Test != "xdg_test.bats" || opts.Jobs != 3 {
		t.Errorf("unexpected options from XDG config: %+v", opts)
	}
	if !reflect.DeepEqual(opts.BatsArgs, []string{"-T", "--timing"}) {
		t.Errorf("unexpected bats_args from XDG config: %v", opts.BatsArgs)
	}
}

func TestParseFileConfig_TargetsMappingsAndCacheFlags(t *testing.T) {
	cfgText := `
targets = ["src/a.sh", "src/#special.sh"]
no_cache = true
no_coverage = true
cache_file = ".custom-cache.json"

[mappings]
"src/a:1.sh" = "test/custom_a.bats" # inline comment
`
	cfg, err := ParseFileConfig(cfgText)
	if err != nil {
		t.Fatalf("ParseFileConfig failed: %v", err)
	}
	if len(cfg.Targets) != 2 || cfg.Targets[0] != "src/a.sh" || cfg.Targets[1] != "src/#special.sh" {
		t.Errorf("unexpected Targets: %+v", cfg.Targets)
	}
	if cfg.Mappings["src/a:1.sh"] != "test/custom_a.bats" {
		t.Errorf("unexpected Mappings: %+v", cfg.Mappings)
	}
	if !cfg.NoCache || !cfg.NoCoverage || cfg.CacheFile != ".custom-cache.json" {
		t.Errorf("unexpected cache/coverage config: NoCache=%v NoCoverage=%v CacheFile=%q", cfg.NoCache, cfg.NoCoverage, cfg.CacheFile)
	}

	// Verify non-local cache_file in config is ignored by applyFileConfig
	opts := &Options{}
	applyFileConfig(opts, &FileConfig{CacheFile: "../../../etc/passwd"}, map[string]bool{})
	if opts.CacheFile != "" {
		t.Errorf("expected escaping cache_file to be ignored, got %q", opts.CacheFile)
	}
}

func TestParseFileConfig_DisallowsUnknownFieldsAndValidatesCategories(t *testing.T) {
	// Typo in field name (min-score instead of min_score) must return error
	typoToml := `
target = "src/a.sh"
min-score = 80.0
`
	if _, err := ParseFileConfig(typoToml); err == nil {
		t.Error("expected error for unknown TOML key 'min-score', got nil")
	}

	// Unknown mutation category must return error
	badCatToml := `
target = "src/a.sh"
disabled_categories = ["NON_EXISTENT_CATEGORY"]
`
	if _, err := ParseFileConfig(badCatToml); err == nil {
		t.Error("expected error for unknown mutation category in config, got nil")
	}
}

func TestParseOptions_Error(t *testing.T) {
	testCases := []struct {
		Name  string
		Input []string
	}{
		{
			Name:  "empty args missing target",
			Input: []string{},
		},
		{
			Name:  "invalid jobs count",
			Input: []string{"--target", "a.sh", "--list", "--jobs", "0"},
		},
		{
			Name:  "invalid format",
			Input: []string{"--target", "a.sh", "--list", "--format", "xml"},
		},
		{
			Name:  "invalid min-score",
			Input: []string{"--target", "a.sh", "--list", "--min-score", "120"},
		},
		{
			Name:  "min-score NaN",
			Input: []string{"--target", "a.sh", "--list", "--min-score", "NaN"},
		},
		{
			Name:  "negative timeout",
			Input: []string{"--target", "a.sh", "--list", "--timeout", "-5s"},
		},
		{
			Name:  "stray positional args",
			Input: []string{"--target", "a.sh", "oops", "--min-score", "100"},
		},
		{
			Name:  "unknown category in --disable-category",
			Input: []string{"--target", "a.sh", "--list", "--disable-category", "INVALID_CAT"},
		},
		{
			Name:  "unknown flag",
			Input: []string{"--unknown-flag"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}

			_, err := ParseOptions(tc.Input, &cli.ProcInout{
				Stdin:  strings.NewReader(""),
				Stdout: stdout,
				Stderr: stderr,
			})
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestFlagUsage(t *testing.T) {
	stdin := strings.NewReader("")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	_, err := ParseOptions([]string{"-h"}, &cli.ProcInout{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("failed to parse options: %v", err)
	}

	if !strings.HasPrefix(stderr.String(), "Usage: bashtion [options]\n") {
		t.Errorf("unexpected usage header:\n%s", stderr.String())
	}
}

func TestParseOptions_AndFileConfig_WorkspaceFlags(t *testing.T) {
	cfg, err := ParseFileConfig(`
target = "src/a.sh"
test = "test/a.bats"
workspace = "."
workspace_include = [".git", "node_modules/bats-support"]
workspace_exclude = ["dist", "fixtures/huge.bin"]
`)
	if err != nil {
		t.Fatalf("ParseFileConfig failed: %v", err)
	}
	if cfg.Workspace != "." || len(cfg.WorkspaceInclude) != 2 || len(cfg.WorkspaceExclude) != 2 {
		t.Fatalf("unexpected workspace config: %+v", cfg)
	}

	opts, err := ParseOptions([]string{
		"--target", "src/a.sh",
		"--list",
		"--workspace", "repo-root",
		"--workspace-include", ".git, node_modules/bats-assert",
		"--workspace-exclude", "tmpdata",
	}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.Workspace != "repo-root" {
		t.Errorf("want Workspace=repo-root, got %q", opts.Workspace)
	}
	if len(opts.WorkspaceInclude) != 2 || opts.WorkspaceInclude[0] != ".git" || opts.WorkspaceInclude[1] != "node_modules/bats-assert" {
		t.Errorf("unexpected WorkspaceInclude: %+v", opts.WorkspaceInclude)
	}
	if len(opts.WorkspaceExclude) != 1 || opts.WorkspaceExclude[0] != "tmpdata" {
		t.Errorf("unexpected WorkspaceExclude: %+v", opts.WorkspaceExclude)
	}
}

func TestParseFileConfig_ValidatesNumericAndDurationBounds(t *testing.T) {
	invalidConfigs := []struct {
		name string
		toml string
	}{
		{"zero jobs", "jobs = 0\n"},
		{"negative jobs", "jobs = -2\n"},
		{"negative min_score", "min_score = -1.0\n"},
		{"min_score over 100", "min_score = 101.5\n"},
		{"min_score nan", "min_score = nan\n"},
		{"negative timeout", `timeout = "-5s"` + "\n"},
	}
	for _, tc := range invalidConfigs {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseFileConfig(tc.toml); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestParseOptions_NonLocalCacheFileWarningAndGuideHelp(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ".bashtion.toml")
	if err := os.WriteFile(cfgPath, []byte("target = \"a.sh\"\ncache_file = \"/tmp/evil.json\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	opts, err := ParseOptions([]string{"--config", cfgPath}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.CacheFile != "" {
		t.Errorf("expected non-local cache_file to be ignored, got %q", opts.CacheFile)
	}
	if !strings.Contains(stderr.String(), "warning: ignoring non-local cache_file") {
		t.Errorf("expected stderr warning for non-local cache_file, got:\n%s", stderr.String())
	}

	helpStderr := &bytes.Buffer{}
	helpOpts, err := ParseOptions([]string{"guide", "--help"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: helpStderr,
	})
	if err != nil || !helpOpts.Help {
		t.Fatalf("expected guide --help to return Help=true without error, got opts=%+v err=%v", helpOpts, err)
	}
	if _, err := ParseOptions([]string{"guide", "unexpected"}, &cli.ProcInout{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}); err == nil {
		t.Errorf("expected error for unexpected positional argument after guide")
	}
}
