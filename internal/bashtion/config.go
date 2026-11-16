//go:build unix

package bashtion

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/report"
)

// FileConfig represents configuration loaded from .bashtion.toml.
type FileConfig struct {
	Target             string             `toml:"target"`
	Targets            []string           `toml:"targets"`
	Test               string             `toml:"test"`
	Mappings           map[string]string  `toml:"mappings"`
	BatsBin            string             `toml:"bats"`
	BatsArgs           []string           `toml:"bats_args"`
	Jobs               int                `toml:"jobs"`
	Timeout            time.Duration      `toml:"timeout"`
	Format             report.Format      `toml:"format"`
	MinScore           float64            `toml:"min_score"`
	GitDiff            string             `toml:"git_diff"`
	NoCache            bool               `toml:"no_cache"`
	NoCoverage         bool               `toml:"no_coverage"`
	CacheFile          string             `toml:"cache_file"`
	Workspace          string             `toml:"workspace"`
	WorkspaceInclude   []string           `toml:"workspace_include"`
	WorkspaceExclude   []string           `toml:"workspace_exclude"`
	DisabledCategories []mutator.Category `toml:"disabled_categories"`
}

// LoadFileConfig reads a TOML configuration file (.bashtion.toml).
func LoadFileConfig(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFileConfig(string(data))
}

type tomlRawConfig struct {
	Target             string            `toml:"target"`
	Targets            []string          `toml:"targets"`
	Test               string            `toml:"test"`
	Mappings           map[string]string `toml:"mappings"`
	BatsBin            string            `toml:"bats"`
	BatsArgs           any               `toml:"bats_args"`
	Jobs               *int              `toml:"jobs"`
	Timeout            string            `toml:"timeout"`
	Format             string            `toml:"format"`
	MinScore           *float64          `toml:"min_score"`
	GitDiff            string            `toml:"git_diff"`
	NoCache            bool              `toml:"no_cache"`
	NoCoverage         bool              `toml:"no_coverage"`
	CacheFile          string            `toml:"cache_file"`
	Workspace          string            `toml:"workspace"`
	WorkspaceInclude   []string          `toml:"workspace_include"`
	WorkspaceExclude   []string          `toml:"workspace_exclude"`
	DisabledCategories []string          `toml:"disabled_categories"`
}

// ParseFileConfig parses TOML content into FileConfig.
func ParseFileConfig(content string) (*FileConfig, error) {
	var raw tomlRawConfig
	dec := toml.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid toml configuration: %w", err)
	}

	cfg := &FileConfig{
		Target:           raw.Target,
		Targets:          raw.Targets,
		Test:             raw.Test,
		Mappings:         raw.Mappings,
		BatsBin:          raw.BatsBin,
		GitDiff:          raw.GitDiff,
		NoCache:          raw.NoCache,
		NoCoverage:       raw.NoCoverage,
		CacheFile:        raw.CacheFile,
		Workspace:        raw.Workspace,
		WorkspaceInclude: raw.WorkspaceInclude,
		WorkspaceExclude: raw.WorkspaceExclude,
	}

	if raw.Jobs != nil {
		if *raw.Jobs < 1 {
			return nil, fmt.Errorf("invalid jobs in config: must be >= 1, got %d", *raw.Jobs)
		}
		cfg.Jobs = *raw.Jobs
	}

	if raw.MinScore != nil {
		if math.IsNaN(*raw.MinScore) || *raw.MinScore < 0 || *raw.MinScore > 100 {
			return nil, fmt.Errorf("invalid min_score in config: must be between 0 and 100, got %v", *raw.MinScore)
		}
		cfg.MinScore = *raw.MinScore
	}

	if raw.BatsArgs != nil {
		switch v := raw.BatsArgs.(type) {
		case string:
			args, err := SplitCommandArgs(v)
			if err != nil {
				return nil, fmt.Errorf("invalid bats_args: %w", err)
			}
			cfg.BatsArgs = args
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					cfg.BatsArgs = append(cfg.BatsArgs, s)
				} else {
					return nil, fmt.Errorf("invalid element in bats_args: %v", item)
				}
			}
		case []string:
			cfg.BatsArgs = append([]string{}, v...)
		default:
			return nil, fmt.Errorf("bats_args must be string or list of strings")
		}
	}

	if raw.Timeout != "" {
		d, err := time.ParseDuration(raw.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout %q: %w", raw.Timeout, err)
		}
		if d < 0 {
			return nil, fmt.Errorf("invalid timeout in config: must not be negative (%s)", raw.Timeout)
		}
		cfg.Timeout = d
	}

	if raw.Format != "" {
		f, err := report.ParseFormat(raw.Format)
		if err != nil {
			return nil, err
		}
		cfg.Format = f
	}

	for _, catStr := range raw.DisabledCategories {
		c := mutator.Category(strings.TrimSpace(catStr))
		if !mutator.IsValidCategory(c) {
			return nil, fmt.Errorf("invalid mutation category in disabled_categories: %q", catStr)
		}
		cfg.DisabledCategories = append(cfg.DisabledCategories, c)
	}

	return cfg, nil
}
