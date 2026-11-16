//go:build unix

package report

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/noissefnoc/bashtion/internal/runner"
)

// Format specifies the output report format.
type Format string

const (
	FormatText     Format = "text"
	FormatLLM      Format = "llm"
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
)

// ParseFormat validates and normalizes a report format string.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "llm", "agent":
		return FormatLLM, nil
	case "json":
		return FormatJSON, nil
	case "markdown", "md", "github":
		return FormatMarkdown, nil
	default:
		return "", fmt.Errorf("unsupported format %q (valid: text, llm, json, markdown)", s)
	}
}

// Write renders the mutation testing summary to w in the requested Format.
func Write(w io.Writer, format Format, summary *runner.Summary, origSrc []byte, minScore float64) error {
	srcMap := map[string][]byte{}
	if summary != nil && summary.TargetFile != "" && len(origSrc) > 0 {
		srcMap[summary.TargetFile] = origSrc
	}
	return WriteMulti(w, format, summary, srcMap, origSrc, minScore)
}

// WriteMulti renders a single- or multi-target mutation testing summary using per-file original source bytes.
func WriteMulti(w io.Writer, format Format, summary *runner.Summary, origSrcByFile map[string][]byte, defaultSrc []byte, minScore float64) error {
	resolveSrc := func(filePath string) []byte {
		if b, ok := origSrcByFile[filePath]; ok && len(b) > 0 {
			return b
		}
		return defaultSrc
	}

	switch format {
	case FormatText:
		return writeText(w, summary, resolveSrc)
	case FormatLLM:
		return writeLLM(w, summary, resolveSrc, minScore)
	case FormatJSON:
		return writeJSON(w, summary, resolveSrc, minScore)
	case FormatMarkdown:
		return writeMarkdown(w, summary, resolveSrc, minScore)
	default:
		return fmt.Errorf("unknown report format: %s", format)
	}
}

func hintForResult(res runner.Result) string {
	if res.Uncovered {
		return res.Mutant.UncoveredHint()
	}
	return res.Mutant.Hint()
}

func writeText(w io.Writer, summary *runner.Summary, resolveSrc func(string) []byte) error {
	fmt.Fprintln(w, "\n========================================")
	fmt.Fprintln(w, "bashtion: Mutation Testing Summary")
	fmt.Fprintln(w, "========================================")
	fmt.Fprintf(w, "Baseline Time : %dms\n", summary.BaselineDuration.Milliseconds())
	fmt.Fprintf(w, "Workspace Prep: %dms\n", summary.WorkspaceSetupDuration.Milliseconds())
	fmt.Fprintf(w, "Mutant Eval   : %dms\n", summary.MutantEvalDuration.Milliseconds())
	fmt.Fprintf(w, "Total Time    : %dms (workers: %d)\n", summary.TotalDuration.Milliseconds(), summary.Jobs)
	fmt.Fprintf(w, "Total Mutants : %d\n", summary.Total)
	fmt.Fprintf(w, "Killed        : %d\n", summary.Killed)
	fmt.Fprintf(w, "Timeout       : %d\n", summary.Timeout)
	if summary.Errors > 0 {
		fmt.Fprintf(w, "Errors        : %d\n", summary.Errors)
	}
	if summary.Uncovered > 0 {
		fmt.Fprintf(w, "Survived      : %d (%d uncovered)\n", summary.Survived, summary.Uncovered)
	} else {
		fmt.Fprintf(w, "Survived      : %d\n", summary.Survived)
	}
	if summary.CacheHits > 0 {
		fmt.Fprintf(w, "Cache Hits    : %d\n", summary.CacheHits)
	}
	if summary.Total == 0 {
		fmt.Fprintln(w, "Mutation Score: N/A (0 mutants generated)")
	} else {
		fmt.Fprintf(w, "Mutation Score: %.1f%%\n", summary.Score())
	}

	if summary.Survived > 0 {
		fmt.Fprintln(w, "\n----------------------------------------")
		fmt.Fprintf(w, "Survived Mutants (%d)\n", summary.Survived)
		fmt.Fprintln(w, "----------------------------------------")
		for _, res := range summary.Results {
			if res.Status != runner.StatusSurvived {
				continue
			}
			m := res.Mutant
			uncovLabel := ""
			if res.Uncovered {
				uncovLabel = " [UNCOVERED]"
			}
			fmt.Fprintf(w, "\nMutant #%02d [%s]%s %s:%d:%d — %s\n",
				m.ID, m.Category, uncovLabel, m.FilePath, m.Line, m.Col, m.Description)
			fmt.Fprint(w, m.UnifiedDiff(resolveSrc(m.FilePath)))
		}
	}
	return nil
}

// writeLLM outputs a compact, token-efficient, actionable feedback report designed for LLM Coding Agents.
func writeLLM(w io.Writer, summary *runner.Summary, resolveSrc func(string) []byte, minScore float64) error {
	score := summary.Score()
	status := "PASS"
	if summary.Errors > 0 {
		status = fmt.Sprintf("ERROR (%d mutant evaluation error(s))", summary.Errors)
	} else if summary.Total == 0 {
		if minScore > 0 && !summary.AllowZeroMutants {
			status = "FAIL (no mutants generated to satisfy min-score)"
		} else {
			status = "NO_MUTANTS"
		}
	} else if minScore > 0 && score < minScore {
		status = "FAIL (below min-score threshold)"
	} else if summary.Survived > 0 {
		status = "NEEDS_TESTS"
	}

	fmt.Fprintln(w, "# bashtion LLM Agent Feedback")
	fmt.Fprintf(w, "- Status: %s\n", status)
	fmt.Fprintf(w, "- Target Script: `%s`\n", summary.TargetFile)
	fmt.Fprintf(w, "- Bats Test Suite: `%s`\n", summary.TestFile)
	if summary.Total == 0 {
		fmt.Fprintln(w, "- Mutation Score: N/A (0 mutants generated)")
	} else {
		fmt.Fprintf(w, "- Mutation Score: %.1f%% (%d killed, %d timeout, %d survived / %d total)\n",
			score, summary.Killed, summary.Timeout, summary.Survived, summary.Total)
	}
	if summary.Uncovered > 0 {
		fmt.Fprintf(w, "- Uncovered Mutants: %d (lines not executed during baseline)\n", summary.Uncovered)
	}
	if minScore > 0 {
		fmt.Fprintf(w, "- Required Minimum Score: %.1f%%\n", minScore)
	}

	if summary.Total == 0 {
		fmt.Fprintln(w, "\nNo mutants were generated (target may contain only comments, all lines disabled, or no diff changes).")
		return nil
	}

	if summary.Survived == 0 {
		fmt.Fprintln(w, "\nAll mutants were killed. No additional bats tests are required.")
		return nil
	}

	fmt.Fprintf(w, "\n## Action Required: %d Survived Mutant(s)\n", summary.Survived)
	fmt.Fprintf(w, "Update `%s` (without breaking `%s`) so that the following mutations cause the test suite to fail:\n",
		summary.TestFile, summary.TargetFile)

	for _, res := range summary.Results {
		if res.Status != runner.StatusSurvived {
			continue
		}
		m := res.Mutant
		fnLabel := m.FuncName
		if fnLabel == "" {
			fnLabel = "<top-level>"
		}
		uncovTag := ""
		if res.Uncovered {
			uncovTag = ", status: `UNCOVERED`"
		}
		fmt.Fprintf(w, "\n### Mutant #%02d: `%s:%d:%d` (func: `%s`, category: `%s`%s)\n",
			m.ID, m.FilePath, m.Line, m.Col, fnLabel, m.Category, uncovTag)
		fmt.Fprintf(w, "- Mutation: %s (%s -> %s)\n", m.Description, markdownInlineCode(m.Original), markdownInlineCode(m.Replacement))
		fmt.Fprintf(w, "- Hint: %s\n", hintForResult(res))
		diffText := m.UnifiedDiff(resolveSrc(m.FilePath))
		openFence, closeFence := codeFence(diffText, "diff")
		fmt.Fprintln(w, openFence)
		fmt.Fprint(w, diffText)
		fmt.Fprintln(w, closeFence)
	}

	fmt.Fprintln(w, "\n## Re-verification Command")
	if strings.Contains(summary.TargetFile, ", ") {
		csvTargets := strings.ReplaceAll(summary.TargetFile, ", ", ",")
		fmt.Fprintf(w, "```bash\nbashtion --target %s --format llm\n```\n", safeShellArg(csvTargets))
	} else if summary.TestFile != "" {
		fmt.Fprintf(w, "```bash\nbashtion --target %s --test %s --format llm\n```\n",
			safeShellArg(summary.TargetFile), safeShellArg(summary.TestFile))
	} else {
		fmt.Fprintf(w, "```bash\nbashtion --target %s --format llm\n```\n",
			safeShellArg(summary.TargetFile))
	}
	return nil
}

// safeShellArg safely formats an argument for POSIX shells.
// If the argument contains only characters known to be safe in shells without quoting,
// it is returned as-is; otherwise, it is wrapped in POSIX single quotes with internal
// single quotes escaped as '\”.
func safeShellArg(s string) string {
	if s == "" {
		return "''"
	}
	isSafe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '/' || c == '-' || c == '@' || c == '%' || c == '+' || c == '=' || c == ':':
		default:
			isSafe = false
		}
		if !isSafe {
			break
		}
	}
	if isSafe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type jsonReport struct {
	TargetFile       string       `json:"target_file"`
	TestFile         string       `json:"test_file"`
	BaselineTimeMs   int64        `json:"baseline_time_ms"`
	TotalTimeMs      int64        `json:"total_time_ms"`
	Workers          int          `json:"workers"`
	TotalMutants     int          `json:"total_mutants"`
	Killed           int          `json:"killed"`
	Timeout          int          `json:"timeout"`
	Survived         int          `json:"survived"`
	Errors           int          `json:"errors,omitempty"`
	Uncovered        int          `json:"uncovered,omitempty"`
	CacheHits        int          `json:"cache_hits,omitempty"`
	MutationScore    float64      `json:"mutation_score"`
	MinScore         float64      `json:"min_score,omitempty"`
	PassedThreshold  bool         `json:"passed_threshold"`
	SurvivedMutants  []jsonMutant `json:"survived_mutants"`
	AllMutantResults []jsonMutant `json:"all_mutant_results"`
}

type jsonMutant struct {
	ID          int    `json:"id"`
	Status      string `json:"status"`
	Uncovered   bool   `json:"uncovered,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
	FilePath    string `json:"file_path"`
	FuncName    string `json:"func_name,omitempty"`
	Line        uint   `json:"line"`
	Col         uint   `json:"col"`
	Category    string `json:"category"`
	Description string `json:"description"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	DurationMs  int64  `json:"duration_ms"`
	Hint        string `json:"hint,omitempty"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
}

func writeJSON(w io.Writer, summary *runner.Summary, resolveSrc func(string) []byte, minScore float64) error {
	score := summary.Score()
	rep := jsonReport{
		TargetFile:       summary.TargetFile,
		TestFile:         summary.TestFile,
		BaselineTimeMs:   summary.BaselineDuration.Milliseconds(),
		TotalTimeMs:      summary.TotalDuration.Milliseconds(),
		Workers:          summary.Jobs,
		TotalMutants:     summary.Total,
		Killed:           summary.Killed,
		Timeout:          summary.Timeout,
		Survived:         summary.Survived,
		Errors:           summary.Errors,
		Uncovered:        summary.Uncovered,
		CacheHits:        summary.CacheHits,
		MutationScore:    score,
		MinScore:         minScore,
		PassedThreshold:  summary.Errors == 0 && (minScore <= 0 || (summary.Total == 0 && summary.AllowZeroMutants) || (summary.Total > 0 && score >= minScore)),
		SurvivedMutants:  make([]jsonMutant, 0, summary.Survived),
		AllMutantResults: make([]jsonMutant, 0, len(summary.Results)),
	}

	for _, res := range summary.Results {
		m := res.Mutant
		jm := jsonMutant{
			ID:          m.ID,
			Status:      string(res.Status),
			Uncovered:   res.Uncovered,
			Cached:      res.Cached,
			FilePath:    m.FilePath,
			FuncName:    m.FuncName,
			Line:        m.Line,
			Col:         m.Col,
			Category:    string(m.Category),
			Description: m.Description,
			Original:    m.Original,
			Replacement: m.Replacement,
			DurationMs:  res.Duration.Milliseconds(),
		}
		if res.Status == runner.StatusSurvived {
			jm.Hint = hintForResult(res)
			jm.UnifiedDiff = m.UnifiedDiff(resolveSrc(m.FilePath))
			rep.SurvivedMutants = append(rep.SurvivedMutants, jm)
		}
		rep.AllMutantResults = append(rep.AllMutantResults, jm)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

func writeMarkdown(w io.Writer, summary *runner.Summary, resolveSrc func(string) []byte, minScore float64) error {
	score := summary.Score()
	scoreStr := fmt.Sprintf("**%.1f%%**", score)
	if summary.Total == 0 {
		scoreStr = "**N/A**"
	}
	fmt.Fprintln(w, "## 🛡️ bashtion Mutation Testing Report")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Target | Test Suite | Score | Killed | Survived | Timeout | Total | Time |")
	fmt.Fprintln(w, "| :--- | :--- | ---: | ---: | ---: | ---: | ---: | ---: |")
	fmt.Fprintf(w, "| %s | %s | %s | %d | %d | %d | %d | %dms |\n",
		markdownTableCellCode(summary.TargetFile), markdownTableCellCode(summary.TestFile), scoreStr,
		summary.Killed, summary.Survived, summary.Timeout, summary.Total,
		summary.TotalDuration.Milliseconds(),
	)

	if summary.Errors > 0 {
		fmt.Fprintf(w, "\n❌ Mutant evaluation errors: %d\n", summary.Errors)
	}
	if summary.Uncovered > 0 {
		fmt.Fprintf(w, "\n- Uncovered mutants (skipped by baseline coverage): %d\n", summary.Uncovered)
	}

	if minScore > 0 {
		if summary.Errors == 0 && summary.Total > 0 && score >= minScore {
			fmt.Fprintf(w, "\n✅ Passed minimum score threshold (%.1f%% >= %.1f%%)\n", score, minScore)
		} else if summary.Errors == 0 && summary.Total == 0 && summary.AllowZeroMutants {
			fmt.Fprintf(w, "\n✅ Passed minimum score threshold (no mutants generated in diff scope; allowed for %.1f%%)\n", minScore)
		} else if summary.Total == 0 {
			fmt.Fprintf(w, "\n❌ Failed minimum score threshold: no mutants generated to satisfy %.1f%%\n", minScore)
		} else if summary.Errors > 0 {
			fmt.Fprintf(w, "\n❌ Failed threshold check due to %d evaluation error(s)\n", summary.Errors)
		} else {
			fmt.Fprintf(w, "\n❌ Failed minimum score threshold (%.1f%% < %.1f%%)\n", score, minScore)
		}
	}

	if summary.Survived > 0 {
		fmt.Fprintf(w, "\n### Survived Mutants (%d)\n\n", summary.Survived)
		for _, res := range summary.Results {
			if res.Status != runner.StatusSurvived {
				continue
			}
			m := res.Mutant
			uncovLabel := ""
			if res.Uncovered {
				uncovLabel = " [UNCOVERED]"
			}
			fmt.Fprintf(w, "<details>\n<summary><b>#%02d [%s]%s</b> <code>%s:%d:%d</code> — %s</summary>\n\n",
				m.ID,
				html.EscapeString(string(m.Category)),
				html.EscapeString(uncovLabel),
				html.EscapeString(m.FilePath),
				m.Line,
				m.Col,
				html.EscapeString(m.Description))
			fmt.Fprintf(w, "> **Hint**: %s\n\n", hintForResult(res))
			diffText := m.UnifiedDiff(resolveSrc(m.FilePath))
			openFence, closeFence := codeFence(diffText, "diff")
			fmt.Fprintln(w, openFence)
			fmt.Fprint(w, diffText)
			fmt.Fprintln(w, closeFence)
			fmt.Fprintln(w, "</details>")
		}
	}
	return nil
}

// codeFence generates opening and closing Markdown code fence strings with sufficient backticks
// to avoid colliding with any backtick sequences within content.
func codeFence(content, lang string) (open, close string) {
	maxBackticks := 2
	cur := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '`' {
			cur++
			if cur > maxBackticks {
				maxBackticks = cur
			}
		} else {
			cur = 0
		}
	}
	fence := strings.Repeat("`", maxBackticks+1)
	return fence + lang, fence
}

func markdownInlineCode(s string) string {
	clean := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	maxRun := 0
	cur := 0
	for i := 0; i < len(clean); i++ {
		if clean[i] == '`' {
			cur++
			if cur > maxRun {
				maxRun = cur
			}
		} else {
			cur = 0
		}
	}
	delim := strings.Repeat("`", maxRun+1)
	if strings.HasPrefix(clean, "`") || strings.HasSuffix(clean, "`") {
		return delim + " " + clean + " " + delim
	}
	return delim + clean + delim
}

func markdownTableCellCode(s string) string {
	clean := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(s)
	if !strings.ContainsAny(clean, "|`<>&") {
		return "`" + clean + "`"
	}
	escaped := html.EscapeString(clean)
	escaped = strings.ReplaceAll(escaped, "|", "&#124;")
	return "<code>" + escaped + "</code>"
}
