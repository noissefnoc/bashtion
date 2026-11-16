//go:build unix

package bashtion

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/noissefnoc/bashtion/internal/cli"
	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/report"
)

// DefaultConfigFile is the default config filename looked up when present in the working directory.
const DefaultConfigFile = ".bashtion.toml"

// Options holds parsed and merged command-line options for bashtion.
type Options struct {
	Config             string
	Target             string
	Targets            []string
	Test               string
	Mappings           map[string]string
	BatsBin            string
	BatsArgs           []string
	Jobs               int
	List               bool
	Timeout            time.Duration
	Format             report.Format
	MinScore           float64
	GitDiff            string
	NoCache            bool
	NoCoverage         bool
	CacheFile          string
	Workspace          string
	WorkspaceInclude   []string
	WorkspaceExclude   []string
	DisabledCategories []mutator.Category
	Help               bool
	Version            bool
	Guide              bool
}

// TargetPatterns returns all configured target patterns (combining Target and Targets).
func (o *Options) TargetPatterns() []string {
	var out []string
	if o.Target != "" {
		out = append(out, o.Target)
	}
	out = append(out, o.Targets...)
	return out
}

// ParseOptions parses and validates CLI arguments without calling os.Exit.
func ParseOptions(args []string, inout *cli.ProcInout) (*Options, error) {
	if len(args) >= 1 && (args[0] == "guide" || args[0] == "agent-guide") {
		if len(args) == 1 {
			return &Options{
				Format: report.FormatText,
				Guide:  true,
			}, nil
		}
		if len(args) == 2 && (args[1] == "-h" || args[1] == "--help") {
			fmt.Fprintln(inout.Stderr, "Usage: bashtion guide\n\nPrint AI coding agent remediation guide to stdout and exit.")
			return &Options{
				Format: report.FormatText,
				Help:   true,
			}, nil
		}
		return nil, fmt.Errorf("unexpected argument(s) for %s: %v", args[0], args[1:])
	}

	flags := flag.NewFlagSet("bashtion", flag.ContinueOnError)
	flags.SetOutput(inout.Stderr)

	options := &Options{
		Format: report.FormatText,
	}
	var (
		disableCatsCSV string
		wsIncludeCSV   string
		wsExcludeCSV   string
		formatStr      string
		batsArgsStr    string
	)

	flags.StringVar(&options.Config, "config", "", "Path to config file (default: .bashtion.toml if present)")
	flags.StringVar(&options.Target, "target", "", "Path, directory, or glob pattern for target Bash script(s) (e.g. src/**/*.sh)")
	flags.StringVar(&options.Target, "t", "", "Alias for --target")
	flags.StringVar(&options.Test, "test", "", "Path to bats test file (.bats) (auto-discovered if omitted for existing scripts)")
	flags.StringVar(&options.BatsBin, "bats", "bats", "bats executable name or path")
	flags.StringVar(&batsArgsStr, "bats-args", "", "Pass-through arguments to bats (e.g. --filter <regex>)")
	flags.IntVar(&options.Jobs, "jobs", DefaultJobs(), "Number of parallel worker jobs")
	flags.IntVar(&options.Jobs, "j", DefaultJobs(), "Alias for --jobs")
	flags.BoolVar(&options.List, "list", false, "Only list generated mutants without running tests")
	flags.DurationVar(&options.Timeout, "timeout", 0, "Per-mutant timeout (default: max(5s, 5x baseline))")
	flags.StringVar(&formatStr, "format", "text", "Output report format: text, llm, json, markdown")
	flags.Float64Var(&options.MinScore, "min-score", 0, "Minimum required mutation score percentage (0-100)")
	flags.StringVar(&options.GitDiff, "git-diff", "", "Only mutate lines changed in git diff against <ref> (e.g. HEAD, main)")
	flags.BoolVar(&options.NoCache, "no-cache", false, "Disable incremental result cache (recommended for CI gates)")
	flags.BoolVar(&options.NoCoverage, "no-coverage", false, "Disable baseline line coverage instrumentation")
	flags.StringVar(&options.CacheFile, "cache-file", "", "Path to incremental cache file (default: ${XDG_CACHE_HOME:-~/.cache}/bashtion/<project-hash>.json)")
	flags.StringVar(&options.Workspace, "workspace", "", "Explicit workspace root directory copied into mutant sandboxes")
	flags.StringVar(&wsIncludeCSV, "workspace-include", "", "Comma-separated workspace paths/patterns to include despite default skips (e.g. .git,node_modules/bats-support)")
	flags.StringVar(&wsExcludeCSV, "workspace-exclude", "", "Comma-separated workspace paths/patterns to exclude from mutant sandboxes")
	flags.StringVar(&disableCatsCSV, "disable-category", "", "Comma-separated mutation categories to disable")
	flags.BoolVar(&options.Version, "version", false, "Print version information and exit")
	flags.BoolVar(&options.Version, "v", false, "Alias for --version")
	flags.BoolVar(&options.Guide, "guide", false, "Print AI coding agent remediation guide to stdout and exit")
	flags.BoolVar(&options.Guide, "help-agent", false, "Alias for --guide")

	flags.Usage = func() {
		fmt.Fprint(inout.Stderr, "Usage: bashtion [options]\n")
		fmt.Fprint(inout.Stderr, "       bashtion guide (print AI coding agent remediation guide)\n\n")
		fmt.Fprint(inout.Stderr, "OPTIONS\n")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			options.Help = true
			return options, nil
		}
		return nil, err
	}

	if flags.NArg() > 0 {
		return nil, fmt.Errorf("unexpected positional argument(s): %v (flags must precede positional arguments, or flags contain typos)", flags.Args())
	}

	if options.Version || options.Guide {
		return options, nil
	}

	parsedFormat, err := report.ParseFormat(formatStr)
	if err != nil {
		return nil, err
	}
	options.Format = parsedFormat

	setFlags := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) {
		name := f.Name
		switch name {
		case "t":
			name = "target"
		case "j":
			name = "jobs"
		case "v":
			name = "version"
		}
		setFlags[name] = true
	})

	// Load config file if --config is specified, or if .bashtion.toml exists in current directory,
	// or fallback to XDG config directory ($XDG_CONFIG_HOME/bashtion/config.toml or ~/.config/bashtion/config.toml)
	configPath := options.Config
	if configPath == "" {
		if _, err := os.Stat(DefaultConfigFile); err == nil {
			configPath = DefaultConfigFile
		} else if xdg := resolveXDGConfigFile(); xdg != "" {
			configPath = xdg
		}
	}
	if configPath != "" {
		fileCfg, err := LoadFileConfig(configPath)
		if err != nil {
			return nil, fmt.Errorf("loading config %s: %w", configPath, err)
		}
		applyFileConfigWithWarnings(options, fileCfg, setFlags, inout.Stderr)
	}

	if batsArgsStr != "" {
		parsedBatsArgs, err := SplitCommandArgs(batsArgsStr)
		if err != nil {
			return nil, fmt.Errorf("invalid --bats-args: %w", err)
		}
		options.BatsArgs = parsedBatsArgs
	}

	appendCSVTrim(&options.WorkspaceInclude, wsIncludeCSV)
	appendCSVTrim(&options.WorkspaceExclude, wsExcludeCSV)

	if disableCatsCSV != "" {
		for _, part := range strings.Split(disableCatsCSV, ",") {
			p := strings.TrimSpace(part)
			if p != "" {
				c := mutator.Category(p)
				if !mutator.IsValidCategory(c) {
					return nil, fmt.Errorf("invalid mutation category in --disable-category: %q", p)
				}
				options.DisabledCategories = append(options.DisabledCategories, c)
			}
		}
	}

	if options.Jobs <= 0 {
		return nil, errors.New("--jobs must be >= 1")
	}
	if options.Timeout < 0 {
		return nil, errors.New("--timeout must not be negative")
	}
	if math.IsNaN(options.MinScore) || options.MinScore < 0 || options.MinScore > 100 {
		return nil, errors.New("--min-score must be between 0 and 100")
	}

	if options.Target == "" && len(options.Targets) == 0 {
		return nil, errors.New("--target <script.sh> is required")
	}

	return options, nil
}

func applyFileConfig(opts *Options, fileCfg *FileConfig, setFlags map[string]bool) {
	applyFileConfigWithWarnings(opts, fileCfg, setFlags, io.Discard)
}

func applyFileConfigWithWarnings(opts *Options, fileCfg *FileConfig, setFlags map[string]bool, stderr io.Writer) {
	if !setFlags["target"] {
		if fileCfg.Target != "" {
			opts.Target = fileCfg.Target
		}
		if len(fileCfg.Targets) > 0 {
			opts.Targets = append(opts.Targets, fileCfg.Targets...)
		}
	}
	if !setFlags["test"] && fileCfg.Test != "" {
		opts.Test = fileCfg.Test
	}
	if len(fileCfg.Mappings) > 0 {
		if opts.Mappings == nil {
			opts.Mappings = make(map[string]string, len(fileCfg.Mappings))
		}
		for k, v := range fileCfg.Mappings {
			opts.Mappings[k] = v
		}
	}
	if !setFlags["bats"] && fileCfg.BatsBin != "" {
		opts.BatsBin = fileCfg.BatsBin
	}
	if !setFlags["bats-args"] && len(fileCfg.BatsArgs) > 0 {
		opts.BatsArgs = append([]string{}, fileCfg.BatsArgs...)
	}
	if !setFlags["jobs"] && fileCfg.Jobs > 0 {
		opts.Jobs = fileCfg.Jobs
	}
	if !setFlags["timeout"] && fileCfg.Timeout > 0 {
		opts.Timeout = fileCfg.Timeout
	}
	if !setFlags["format"] && fileCfg.Format != "" {
		opts.Format = fileCfg.Format
	}
	if !setFlags["min-score"] && fileCfg.MinScore > 0 {
		opts.MinScore = fileCfg.MinScore
	}
	if !setFlags["git-diff"] && fileCfg.GitDiff != "" {
		opts.GitDiff = fileCfg.GitDiff
	}
	if !setFlags["no-cache"] && fileCfg.NoCache {
		opts.NoCache = fileCfg.NoCache
	}
	if !setFlags["no-coverage"] && fileCfg.NoCoverage {
		opts.NoCoverage = fileCfg.NoCoverage
	}
	if !setFlags["cache-file"] && fileCfg.CacheFile != "" {
		if filepath.IsLocal(filepath.Clean(fileCfg.CacheFile)) {
			opts.CacheFile = fileCfg.CacheFile
		} else if stderr != nil {
			fmt.Fprintf(stderr, "warning: ignoring non-local cache_file %q in config (pass --cache-file explicitly to override)\n", fileCfg.CacheFile)
		}
	}
	if !setFlags["workspace"] && fileCfg.Workspace != "" {
		opts.Workspace = fileCfg.Workspace
	}
	if !setFlags["workspace-include"] && len(fileCfg.WorkspaceInclude) > 0 {
		opts.WorkspaceInclude = append(opts.WorkspaceInclude, fileCfg.WorkspaceInclude...)
	}
	if !setFlags["workspace-exclude"] && len(fileCfg.WorkspaceExclude) > 0 {
		opts.WorkspaceExclude = append(opts.WorkspaceExclude, fileCfg.WorkspaceExclude...)
	}
	if !setFlags["disable-category"] && len(fileCfg.DisabledCategories) > 0 {
		opts.DisabledCategories = append(opts.DisabledCategories, fileCfg.DisabledCategories...)
	}
}

func appendCSVTrim(dst *[]string, csv string) {
	if csv == "" {
		return
	}
	for _, part := range strings.Split(csv, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*dst = append(*dst, p)
		}
	}
}

func resolveXDGConfigFile() string {
	xdgHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgHome != "" {
		p := filepath.Join(xdgHome, "bashtion", "config.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		p := filepath.Join(home, ".config", "bashtion", "config.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
