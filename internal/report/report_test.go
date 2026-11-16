//go:build unix

package report_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/noissefnoc/bashtion/internal/mutator"
	"github.com/noissefnoc/bashtion/internal/report"
	"github.com/noissefnoc/bashtion/internal/runner"
)

func sampleSummaryAndSrc(t *testing.T) (*runner.Summary, []byte) {
	t.Helper()
	src := []byte(`#!/usr/bin/env bash
validate_port() {
    if [[ "$1" -ge 1024 ]]; then
        return 0
    fi
    return 1
}
`)
	mutants, err := mutator.Generate("deploy.sh", src)
	if err != nil {
		t.Fatal(err)
	}
	var results []runner.Result
	for i, m := range mutants {
		st := runner.StatusKilled
		if i == 0 {
			st = runner.StatusSurvived
		}
		results = append(results, runner.Result{
			Mutant:   m,
			Status:   st,
			Duration: 50 * time.Millisecond,
		})
	}
	return &runner.Summary{
		TargetFile:       "deploy.sh",
		TestFile:         "deploy.bats",
		BaselineDuration: 40 * time.Millisecond,
		TotalDuration:    150 * time.Millisecond,
		Jobs:             2,
		Total:            len(mutants),
		Killed:           len(mutants) - 1,
		Survived:         1,
		Results:          results,
	}, src
}

func TestWrite_LLMFormat(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	var buf bytes.Buffer
	if err := report.Write(&buf, report.FormatLLM, sum, src, 90.0); err != nil {
		t.Fatalf("Write LLM failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"# bashtion LLM Agent Feedback",
		"Status: FAIL (below min-score threshold)",
		"func: `validate_port`",
		"Hint: Boundary value test case is missing",
		"bashtion --target deploy.sh --test deploy.bats --format llm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected LLM output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestWrite_JSONFormat(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	var buf bytes.Buffer
	if err := report.Write(&buf, report.FormatJSON, sum, src, 80.0); err != nil {
		t.Fatalf("Write JSON failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if parsed["target_file"] != "deploy.sh" {
		t.Errorf("unexpected target_file: %v", parsed["target_file"])
	}
	survived, ok := parsed["survived_mutants"].([]any)
	if !ok || len(survived) != 1 {
		t.Errorf("expected 1 survived mutant in JSON, got: %v", parsed["survived_mutants"])
	}
}

func TestWrite_MarkdownFormat(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	var buf bytes.Buffer
	if err := report.Write(&buf, report.FormatMarkdown, sum, src, 50.0); err != nil {
		t.Fatalf("Write Markdown failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "## 🛡️ bashtion Mutation Testing Report") || !strings.Contains(out, "<details>") {
		t.Errorf("unexpected markdown output:\n%s", out)
	}
}

func TestWrite_TextFormatAndParseFormat(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	var buf bytes.Buffer
	if err := report.Write(&buf, report.FormatText, sum, src, 0); err != nil {
		t.Fatalf("Write Text failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"bashtion: Mutation Testing Summary",
		"Workspace Prep:",
		"Mutant Eval   :",
		"Survived Mutants (1)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected Text output to contain %q, got:\n%s", want, out)
		}
	}

	for _, valid := range []string{"", "text", "llm", "agent", "json", "markdown", "md", "github"} {
		if _, err := report.ParseFormat(valid); err != nil {
			t.Errorf("ParseFormat(%q) unexpected error: %v", valid, err)
		}
	}
	if _, err := report.ParseFormat("invalid"); err == nil {
		t.Error("expected error for invalid format, got nil")
	}
}

func TestWrite_UncoveredAndCacheHits(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	sum.Uncovered = 1
	sum.CacheHits = 2
	sum.Results[0].Uncovered = true

	var textBuf bytes.Buffer
	if err := report.Write(&textBuf, report.FormatText, sum, src, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textBuf.String(), "[UNCOVERED]") || !strings.Contains(textBuf.String(), "Cache Hits    : 2") {
		t.Errorf("expected [UNCOVERED] and Cache Hits in text report:\n%s", textBuf.String())
	}

	var llmBuf bytes.Buffer
	if err := report.Write(&llmBuf, report.FormatLLM, sum, src, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llmBuf.String(), "status: `UNCOVERED`") || !strings.Contains(llmBuf.String(), "never executed during the baseline") {
		t.Errorf("expected uncovered status and hint in LLM report:\n%s", llmBuf.String())
	}
}

func TestWrite_LLMFormat_ShellEscape(t *testing.T) {
	sum, src := sampleSummaryAndSrc(t)
	sum.TargetFile = "x;touch PWNED;$(id).sh"
	sum.TestFile = "test with spaces and 'quotes'.bats"

	var buf bytes.Buffer
	if err := report.Write(&buf, report.FormatLLM, sum, src, 0); err != nil {
		t.Fatalf("Write LLM failed: %v", err)
	}
	out := buf.String()

	// Should safely escape via POSIX single quotes
	expectedCmd := "bashtion --target 'x;touch PWNED;$(id).sh' --test 'test with spaces and '\\''quotes'\\''.bats' --format llm"
	if !strings.Contains(out, expectedCmd) {
		t.Errorf("expected safely quoted re-verification command:\n  want: %s\n  got:\n%s", expectedCmd, out)
	}

	// Multi-target test with special chars
	sum.TargetFile = "a;b.sh, c d.sh"
	sum.TestFile = ""
	buf.Reset()
	if err := report.Write(&buf, report.FormatLLM, sum, src, 0); err != nil {
		t.Fatalf("Write LLM multi-target failed: %v", err)
	}
	multiOut := buf.String()
	expectedMulti := "bashtion --target 'a;b.sh,c d.sh' --format llm"
	if !strings.Contains(multiOut, expectedMulti) {
		t.Errorf("expected safely quoted multi-target command:\n  want: %s\n  got:\n%s", expectedMulti, multiOut)
	}
}

func TestWrite_ZeroMutants(t *testing.T) {
	zeroSum := &runner.Summary{
		TargetFile: "empty.sh",
		TestFile:   "empty.bats",
		Total:      0,
		Results:    []runner.Result{},
	}
	src := []byte("# only comments\n")

	// 1. Text format
	var textBuf bytes.Buffer
	if err := report.Write(&textBuf, report.FormatText, zeroSum, src, 80.0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textBuf.String(), "Mutation Score: N/A (0 mutants generated)") {
		t.Errorf("expected N/A for 0 mutants in text, got:\n%s", textBuf.String())
	}

	// 2. LLM format
	var llmBuf bytes.Buffer
	if err := report.Write(&llmBuf, report.FormatLLM, zeroSum, src, 80.0); err != nil {
		t.Fatal(err)
	}
	llmOut := llmBuf.String()
	if !strings.Contains(llmOut, "Status: FAIL (no mutants generated to satisfy min-score)") ||
		!strings.Contains(llmOut, "- Mutation Score: N/A (0 mutants generated)") ||
		!strings.Contains(llmOut, "No mutants were generated") {
		t.Errorf("unexpected LLM output for 0 mutants with min-score:\n%s", llmOut)
	}

	// 3. Markdown format
	var mdBuf bytes.Buffer
	if err := report.Write(&mdBuf, report.FormatMarkdown, zeroSum, src, 80.0); err != nil {
		t.Fatal(err)
	}
	mdOut := mdBuf.String()
	if !strings.Contains(mdOut, "**N/A**") || !strings.Contains(mdOut, "❌ Failed minimum score threshold: no mutants generated") {
		t.Errorf("unexpected Markdown output for 0 mutants with min-score:\n%s", mdOut)
	}

	// 4. JSON format
	var jsonBuf bytes.Buffer
	if err := report.Write(&jsonBuf, report.FormatJSON, zeroSum, src, 80.0); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(jsonBuf.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["mutation_score"] != 0.0 || parsed["passed_threshold"] != false {
		t.Errorf("expected score 0.0 and passed_threshold false, got: score=%v, passed=%v", parsed["mutation_score"], parsed["passed_threshold"])
	}
}

func TestWrite_Markdown_HTMLEscapeAndDynamicCodeFence(t *testing.T) {
	specialSum := &runner.Summary{
		TargetFile: "special<script>.sh",
		TestFile:   "special.bats",
		Total:      1,
		Survived:   1,
		Results: []runner.Result{
			{
				Status: runner.StatusSurvived,
				Mutant: mutator.Mutant{
					ID:          1,
					FilePath:    "special<script>.sh",
					Category:    mutator.CategoryTestBinOp,
					Description: "replace < with > & tag",
					Original:    "echo '```'",
					Replacement: "echo '````'",
					Line:        1,
					Col:         1,
				},
			},
		},
	}
	// Source contains 3 backticks: ```
	srcWithBackticks := []byte("echo '```'\n")

	var mdBuf bytes.Buffer
	if err := report.Write(&mdBuf, report.FormatMarkdown, specialSum, srcWithBackticks, 0); err != nil {
		t.Fatal(err)
	}
	out := mdBuf.String()

	// 1. Should HTML-escape <script> and & in <summary>
	if !strings.Contains(out, "special&lt;script&gt;.sh") || !strings.Contains(out, "&lt; with &gt; &amp; tag") {
		t.Errorf("expected HTML-escaped summary text in markdown output:\n%s", out)
	}

	// 2. Should dynamically expand fence to 5 backticks ````` because content contains 4 backticks ````
	if !strings.Contains(out, "`````diff\n") {
		t.Errorf("expected dynamic code fence `````diff, got:\n%s", out)
	}

	// 3. Test LLM format dynamic fence
	var llmBuf bytes.Buffer
	if err := report.Write(&llmBuf, report.FormatLLM, specialSum, srcWithBackticks, 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llmBuf.String(), "`````diff\n") {
		t.Errorf("expected dynamic code fence in LLM output, got:\n%s", llmBuf.String())
	}
}

func TestWrite_AllowZeroMutantsAndMarkdownTableSafety(t *testing.T) {
	allowZero := &runner.Summary{
		TargetFile:       "docs_only.sh",
		TestFile:         "docs.bats",
		Total:            0,
		AllowZeroMutants: true,
		Results:          []runner.Result{},
	}
	src := []byte("# empty\n")

	var llmBuf, mdBuf, jsonBuf bytes.Buffer
	if err := report.Write(&llmBuf, report.FormatLLM, allowZero, src, 80.0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llmBuf.String(), "Status: NO_MUTANTS") {
		t.Errorf("expected Status: NO_MUTANTS when AllowZeroMutants=true, got:\n%s", llmBuf.String())
	}

	if err := report.Write(&mdBuf, report.FormatMarkdown, allowZero, src, 80.0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mdBuf.String(), "✅ Passed minimum score threshold") {
		t.Errorf("expected passed message when AllowZeroMutants=true, got:\n%s", mdBuf.String())
	}

	if err := report.Write(&jsonBuf, report.FormatJSON, allowZero, src, 80.0); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(jsonBuf.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["passed_threshold"] != true {
		t.Errorf("expected passed_threshold=true when AllowZeroMutants=true, got %v", parsed["passed_threshold"])
	}

	// Verify Markdown table cell sanitization and error/uncovered reporting.
	pipeSum := &runner.Summary{
		TargetFile: "a|b\nc.sh",
		TestFile:   "t`1|2.bats",
		Total:      2,
		Killed:     1,
		Errors:     1,
		Uncovered:  0,
	}
	mdBuf.Reset()
	if err := report.Write(&mdBuf, report.FormatMarkdown, pipeSum, src, 80.0); err != nil {
		t.Fatal(err)
	}
	pipeOut := mdBuf.String()
	if !strings.Contains(pipeOut, "<code>a&#124;b c.sh</code>") || !strings.Contains(pipeOut, "<code>t`1&#124;2.bats</code>") {
		t.Errorf("expected sanitized Markdown table cells, got:\n%s", pipeOut)
	}
	if !strings.Contains(pipeOut, "Mutant evaluation errors: 1") {
		t.Errorf("expected error count in Markdown report, got:\n%s", pipeOut)
	}
}
