package mutator_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/noissefnoc/bashtion/internal/mutator"
)

func TestGenerate_IgnoresCommentsAndStrings_AndExactUnifiedDiff(t *testing.T) {
	script := `#!/usr/bin/env bash
# comment with && and -eq and return 0
msg="string with && and -eq and return 0"
if [[ "$a" -eq 1 ]] && [ "$b" = "ok" ]; then
    return 0
fi
`
	mutants, err := mutator.Generate("sample.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(mutants) != 4 {
		t.Fatalf("expected 4 mutants (-eq, &&, =, return 0), got %d", len(mutants))
	}

	for _, m := range mutants {
		if m.Line <= 3 {
			t.Errorf("unexpected mutation in comment or string literal on line %d: %+v", m.Line, m)
		}
	}

	// Verify exact UnifiedDiff for mutant 0 (&& -> || on line 4)
	wantDiff := `--- a/sample.sh
+++ b/sample.sh
@@ -2,5 +2,5 @@
 # comment with && and -eq and return 0
 msg="string with && and -eq and return 0"
-if [[ "$a" -eq 1 ]] && [ "$b" = "ok" ]; then
+if [[ "$a" -eq 1 ]] || [ "$b" = "ok" ]; then
     return 0
 fi
`
	gotDiff := mutants[0].UnifiedDiff([]byte(script))
	if gotDiff != wantDiff {
		t.Errorf("UnifiedDiff mismatch:\n%s", cmp.Diff(wantDiff, gotDiff))
	}

	// Verify fallback UnifiedDiff when offsets are out of bounds
	invalidMutant := mutator.Mutant{
		Original:    "-eq",
		Replacement: "-ne",
		StartOffset: -1,
		EndOffset:   2,
	}
	if got := invalidMutant.UnifiedDiff([]byte(script)); got != "- -eq\n+ -ne" {
		t.Errorf("unexpected fallback diff: %q", got)
	}
}

func TestGenerate_AllOperatorsAndHints(t *testing.T) {
	script := `#!/usr/bin/env bash
all_ops() {
    local a="${1:-def}"
    local b="${2:+alt}"
    local c="${3-unset}"
    local d="${4+set}"
    local e="${a#pre}"
    local f="${a##pre}"
    local g="${a%suf}"
    local h="${a%%suf}"

    if [[ -z "$a" ]] || [[ -n "$b" ]] || [[ -f "/tmp/x" ]] || [[ ! -d "/tmp/y" ]]; then
        true
    else
        false
    fi

    if [[ "$a" == "x" ]] && [[ "$a" != "y" ]]; then
        return 1
    fi

    if [[ "$1" -lt 10 ]] || [[ "$1" -le 20 ]] || [[ "$1" -gt 30 ]] || [[ "$1" -ge 40 ]] || [[ "$1" -ne 50 ]]; then
        return 0
    fi

    if [ -z "$a" ] && [ -n "$b" ] && [ -e "/tmp/z" ] && [ "$1" -eq 1 ]; then
        return 0
    fi

    if (( (1 + 2 - 3 * 4 / 2) == 0 )) && (( a != 1 )) && (( a < 2 )) && (( a <= 3 )) && (( a > 4 )) && (( a >= 5 )) && (( a && b || c )); then
        exit 0
    fi
    exit 1
}
`
	mutants, err := mutator.Generate("all.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	categories := make(map[mutator.Category]int)
	for _, m := range mutants {
		categories[m.Category]++
		if m.FuncName != "all_ops" {
			t.Errorf("expected FuncName 'all_ops', got %q for mutant %+v", m.FuncName, m)
		}
		hint := m.Hint()
		if hint == "" || !strings.Contains(hint, "all_ops") {
			t.Errorf("expected non-empty hint mentioning all_ops, got %q", hint)
		}
	}

	expectedCats := []mutator.Category{
		mutator.CategoryParamExp,
		mutator.CategoryTestUnaryOp,
		mutator.CategoryTestBinOp,
		mutator.CategoryLogicalOp,
		mutator.CategoryArithBinOp,
		mutator.CategoryArithCmpOp,
		mutator.CategoryReturnCode,
		mutator.CategoryBoolCmd,
	}
	for _, cat := range expectedCats {
		if categories[cat] == 0 {
			t.Errorf("expected mutants for category %s, got 0", cat)
		}
	}

	// Verify Hint fallback for top-level / unknown category
	topMutant := mutator.Mutant{FilePath: "top.sh", Line: 5, Category: "CUSTOM"}
	if h := topMutant.Hint(); !strings.Contains(h, "line 5") {
		t.Errorf("unexpected default hint: %q", h)
	}
}

func TestGenerate_ExtendedBash5OperatorsAndHints(t *testing.T) {
	script := `#!/usr/bin/env bash
extended_ops() {
    local s="${1/foo/bar}"
    local s2="${1//foo/bar}"
    local up="${s^^}"
    local low="${s,,}"
    local up1="${s^}"
    local low1="${s,}"
    local arr=("a" "b" "c" "d")
    local sub=("${arr[@]:0:2}")
    local sub2="${s:2:3}"

    if ! grep -q "err" /dev/null; then
        return 0
    fi

    if grep -q "ok" /dev/null | cat; then
        return 0
    fi

    if [[ "$s" =~ ^[a-z]+$ ]]; then
        return 0
    fi

    for item in "${arr[@]}"; do
        if [[ "$item" == "b" ]]; then
            continue
        fi
        if [[ "$item" == "c" ]]; then
            break
        fi
    done

    case "$s" in
        alpha)
            echo "a"
            ;;
        beta)
            echo "b"
            ;&
        gamma)
            echo "g"
            ;;&
        *)
            echo "default"
            ;;
    esac
}
`
	mutants, err := mutator.Generate("extended.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	categories := make(map[mutator.Category]int)
	hasRegexNeg := false
	for _, m := range mutants {
		categories[m.Category]++
		if m.Category == mutator.CategoryTestBinOp && strings.Contains(m.Description, "=~") {
			hasRegexNeg = true
		}
		hint := m.Hint()
		if hint == "" || !strings.Contains(hint, "extended_ops") {
			t.Errorf("expected non-empty hint mentioning extended_ops for %s, got %q", m.Category, hint)
		}
	}

	expectedNewCats := []mutator.Category{
		mutator.CategoryPipelineNeg,
		mutator.CategoryParamRepl,
		mutator.CategoryParamSlice,
		mutator.CategoryLoopCtrl,
		mutator.CategoryCaseCtrl,
	}
	for _, cat := range expectedNewCats {
		if categories[cat] == 0 {
			t.Errorf("expected mutants for new category %s, got 0", cat)
		}
	}
	if !hasRegexNeg {
		t.Errorf("expected =~ regex negation mutant in TEST_BIN_OP")
	}
}

func TestGenerate_DisableCommentsAndOptions(t *testing.T) {
	script := `#!/usr/bin/env bash
if [[ "$a" -eq 1 ]]; then # bashtion: disable
    return 0
fi
# bashtion: disable-next-line
if [[ "$b" -eq 2 ]]; then
    return 1
fi
`
	mutants, err := mutator.Generate("pragma.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if len(mutants) != 2 {
		t.Fatalf("expected 2 mutants after disable pragmas, got %d: %+v", len(mutants), mutants)
	}
	for _, m := range mutants {
		if m.Line == 2 || m.Line == 6 {
			t.Errorf("disabled line %d was mutated: %+v", m.Line, m)
		}
	}

	// Test AllowedLines filter and DisabledCategories filter
	filtered, err := mutator.GenerateWithOptions("pragma.sh", []byte(script), mutator.GenerateOptions{
		AllowedLines: map[uint]bool{3: true},
	})
	if err != nil || len(filtered) != 1 || filtered[0].Line != 3 {
		t.Errorf("AllowedLines filter failed: %+v (err=%v)", filtered, err)
	}

	catFiltered, err := mutator.GenerateWithOptions("pragma.sh", []byte(script), mutator.GenerateOptions{
		DisabledCategories: []mutator.Category{mutator.CategoryReturnCode},
	})
	if err != nil || len(catFiltered) != 0 {
		t.Errorf("DisabledCategories filter failed: %+v (err=%v)", catFiltered, err)
	}

	// Test disable-file pragma
	disabledFileScript := "#!/usr/bin/env bash\n# bashtion: disable-file\nif [[ $a -eq 1 ]]; then return 0; fi\n"
	none, err := mutator.Generate("disabled.sh", []byte(disabledFileScript))
	if err != nil || len(none) != 0 {
		t.Errorf("expected 0 mutants for disable-file, got %d (err=%v)", len(none), err)
	}

	// Pragma inside string literal, multi-line string, or heredoc must NOT disable file or line,
	// and `[ "$x" = -n ]` or 1-arg `[ -n ]` must not mutate `-n` as a unary operator
	quotedPragmaScript := `#!/usr/bin/env bash
echo "# bashtion: disable-file"
multiline="hello
# bashtion: disable-file
world"
cat <<'EOF'
# bashtion: disable-file
EOF
if [ -n ]; then true; fi
if [ "$x" = -n ]; then return 0; fi
`
	quotedMutants, err := mutator.Generate("quoted.sh", []byte(quotedPragmaScript))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(quotedMutants) != 3 {
		t.Errorf("expected 3 mutants (true -> false, = -> !=, and return 0 -> 1), got %d: %+v", len(quotedMutants), quotedMutants)
	}

	// Loose comment containing 'bashtion:' or 'disabled' must NOT disable line or file
	looseCommentScript := `#!/usr/bin/env bash
# Please refer to bashtion: documentation for details
if [[ "$a" -eq 1 ]]; then # bashtion: disabled_checks_future
    return 0
fi
`
	looseMutants, err := mutator.Generate("loose.sh", []byte(looseCommentScript))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(looseMutants) != 2 {
		t.Errorf("expected 2 mutants (loose comments should not disable lines), got %d: %+v", len(looseMutants), looseMutants)
	}
}

func TestGenerate_CoverageLineForCaseAndMultilineClauses_AndCondStmts(t *testing.T) {
	script := `#!/usr/bin/env bash
check_multi() {
    if [[ "$1" == "a" &&
          "$2" == "b" ]]; then
        return 0
    fi
    if setup_env; grep -q "ok" /dev/null; then
        return 0
    fi
    case "$1" in
        alpha)
            echo "a"
            ;;
        *)
            echo "other"
            ;;
    esac
}
`
	mutants, err := mutator.Generate("multi.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	var foundMultilineTest, foundCaseCtrl bool
	for _, m := range mutants {
		// Second condition in multi-line [[ ... ]] is on line 4, with CoverageLine=3 (start of [[)
		if m.Category == mutator.CategoryTestBinOp && m.Line == 4 {
			foundMultilineTest = true
			if m.CoverageLine != 3 {
				t.Errorf("expected CoverageLine=3 for multi-line [[ ]] operator on line 4, got %d", m.CoverageLine)
			}
		}
		// Standalone ;; on line 13 has statement `echo "a"` on line 12 -> CoverageLine=12
		if m.Category == mutator.CategoryCaseCtrl && m.Line == 13 {
			foundCaseCtrl = true
			if m.CoverageLine != 12 {
				t.Errorf("expected CoverageLine=12 for case ;; on line 13, got %d", m.CoverageLine)
			}
		}
		// In `if setup_env; grep -q "ok" /dev/null; then`, only `grep` (last cond stmt) should be negated, not `setup_env`
		if m.Category == mutator.CategoryPipelineNeg && strings.Contains(m.Replacement, "setup_env") {
			t.Errorf("non-final condition statement setup_env should not be negated: %+v", m)
		}
	}
	if !foundMultilineTest {
		t.Errorf("expected mutant on line 4 of multi-line [[ ]]")
	}
	if !foundCaseCtrl {
		t.Errorf("expected CASE_CTRL mutant on line 13")
	}
}

func TestGenerate_ParamExpSuppressesEquivalentLiteralMutations(t *testing.T) {
	// Literal patterns without wildcards (including quoted/escaped wildcards) must NOT generate # ↔ ## or % ↔ %% mutants.
	// Unquoted glob patterns (*, ?, [) and unquoted parameter expansions ($pat) must generate mutants.
	script := `#!/usr/bin/env bash
test_fn() {
    local a="${1#prefix}"
    local b="${1##prefix}"
    local c="${1%suffix}"
    local d="${1%%suffix}"
    local e="${1#*prefix}"
    local f="${1##*prefix}"
    local g="${1%suffix?}"
    local h="${1%%suffix[0-9]}"
    local q1="${1#"*prefix"}"
    local q2="${1#'?suffix'}"
    local esc="${1#\*literal}"
    local dyn="${1#$pat}"
}
`
	mutants, err := mutator.Generate("test.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	for _, m := range mutants {
		if m.Category == mutator.CategoryParamExp {
			// Lines 3..6 and 11..13 (quoted/escaped wildcards) should have NO mutants
			if (m.Line >= 3 && m.Line <= 6) || (m.Line >= 11 && m.Line <= 13) {
				t.Errorf("expected no equivalent mutant for literal/quoted/escaped pattern on line %d, got: %+v", m.Line, m)
			}
		}
	}

	// Lines 7..10 and 14 (unquoted $pat) must have mutants
	globLinesWithMutants := make(map[uint]bool)
	for _, m := range mutants {
		if m.Category == mutator.CategoryParamExp {
			globLinesWithMutants[m.Line] = true
		}
	}
	for _, line := range []uint{7, 8, 9, 10, 14} {
		if !globLinesWithMutants[line] {
			t.Errorf("expected mutant on line %d with glob/dynamic pattern, but none found", line)
		}
	}
}

func TestGenerate_ScopedSyntaxFallbackForTrailingHeredocFunction(t *testing.T) {
	script := `#!/usr/bin/env bash
heredoc_fn() { cat <<EOF && return 0; }
hello
EOF
`
	mutants, err := mutator.Generate("heredoc.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(mutants) < 2 {
		t.Fatalf("expected mutants for && and return 0 inside trailing-heredoc function, got %d: %+v", len(mutants), mutants)
	}
}

func TestGenerate_SharedOriginalSrcAndApply(t *testing.T) {
	script := []byte("#!/usr/bin/env bash\nif [[ \"$a\" -eq 1 ]]; then\n    return 0\nfi\n")
	mutants, err := mutator.Generate("target.sh", script)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(mutants) < 2 {
		t.Fatalf("expected >= 2 mutants, got %d", len(mutants))
	}

	// Mutating caller's input slice afterwards must not affect mutants' shared OriginalSrc
	origCopy := bytes.Clone(script)
	script[0] = 'X'

	if &mutants[0].OriginalSrc[0] != &mutants[1].OriginalSrc[0] {
		t.Errorf("expected mutants to share a single read-only OriginalSrc backing array")
	}

	applied, err := mutants[0].Apply(nil)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !bytes.Contains(applied, []byte("-ne")) {
		t.Errorf("expected applied bytes to contain mutated operator -ne, got:\n%s", applied)
	}
	if bytes.Equal(applied, origCopy) {
		t.Errorf("expected applied bytes to differ from original script")
	}

	invalid := mutator.Mutant{ID: 99, StartOffset: -1, EndOffset: 5}
	if _, err := invalid.Apply(origCopy); err == nil {
		t.Errorf("expected error for out-of-range offsets")
	}
}

func TestGenerate_TestNegationRemovalPOSIXBinaryAndTerminatingCase(t *testing.T) {
	script := `#!/usr/bin/env bash
check_ops() {
    if [ -f = "$1" ]; then
        echo ok
    fi
    if [ "$1" != "$2" ]; then
        echo ne
    fi
    if [[ ! -f "$1" ]] && [[ ! "$1" =~ ^foo ]] && [ ! -d "$1" ]; then
        echo neg
    fi
    case "$1" in
        a)
            echo "a"
            return 0
            ;;
        b)
            echo "b"
            ;;
        c)
            echo "c"
            ;;
    esac
    printf "%s\n" \
        "${1:-default}"
}
`
	mutants, err := mutator.Generate("ops.sh", []byte(script))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	var hasPOSIXEq, hasRemDoubleNot, hasRemRegexNot, hasRemPOSIXNot, hasFallthroughB, hasMultilineCallCov bool
	for _, m := range mutants {
		switch {
		case m.Line == 3 && m.Original == "-f":
			t.Errorf("LHS operand -f in `[ -f = \"$1\" ]` must not be mutated as unary op: %+v", m)
		case m.Line == 6 && m.Category == mutator.CategoryTestBinOp && m.Original == "!=":
			if m.Replacement != "=" {
				t.Errorf("POSIX [ != ] expected replacement '=', got %q", m.Replacement)
			}
			hasPOSIXEq = true
		case m.Line == 9 && m.Category == mutator.CategoryTestUnaryOp && m.Replacement == "" && strings.Contains(m.Description, "remove ! negation in [[ ]]"):
			hasRemDoubleNot = true
		case m.Line == 9 && m.Category == mutator.CategoryTestBinOp && m.Replacement == "" && strings.Contains(m.Description, "remove ! negation in [[ ]]"):
			hasRemRegexNot = true
		case m.Line == 9 && m.Category == mutator.CategoryTestUnaryOp && m.Replacement == "" && strings.Contains(m.Description, "remove ! negation in ["):
			hasRemPOSIXNot = true
		case m.Category == mutator.CategoryCaseCtrl && m.Line == 16:
			t.Errorf("case branch ending with return 0 must not generate ;; -> ;& mutant: %+v", m)
		case m.Category == mutator.CategoryCaseCtrl && m.Line == 19 && m.Replacement == ";&":
			hasFallthroughB = true
		case m.Category == mutator.CategoryParamExp && m.Line == 25:
			if m.CoverageLine != 24 {
				t.Errorf("expected CoverageLine=24 for multiline printf arg on line 25, got %d", m.CoverageLine)
			}
			hasMultilineCallCov = true
		}
	}

	if !hasPOSIXEq || !hasRemDoubleNot || !hasRemRegexNot || !hasRemPOSIXNot || !hasFallthroughB || !hasMultilineCallCov {
		t.Errorf("missing expected mutants: posixEq=%v remDoubleNot=%v remRegexNot=%v remPOSIXNot=%v fallthroughB=%v multilineCallCov=%v",
			hasPOSIXEq, hasRemDoubleNot, hasRemRegexNot, hasRemPOSIXNot, hasFallthroughB, hasMultilineCallCov)
	}
}

func TestMutantKey_DeterministicNormalizedAndUnique(t *testing.T) {
	src := []byte("#!/usr/bin/env bash\nif [[ \"$1\" -ge 10 ]]; then\n  return 0\nfi\n")
	m1, err := mutator.Generate("src/check.sh", src)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := mutator.Generate("./src/check.sh", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(m1) < 3 || len(m1) != len(m2) {
		t.Fatalf("unexpected mutant counts: %d vs %d", len(m1), len(m2))
	}

	seen := make(map[string]bool, len(m1))
	for i := range m1 {
		k1 := m1[i].Key()
		k2 := m2[i].Key()
		if len(k1) != 16 {
			t.Errorf("expected 16-char hex key, got %q", k1)
		}
		if k1 != k2 {
			t.Errorf("expected normalized path to yield identical key: %q vs %q", k1, k2)
		}
		if seen[k1] {
			t.Errorf("duplicate key %q for mutant %+v", k1, m1[i])
		}
		seen[k1] = true
	}

	// Sequential ID differences must not change Key().
	altered := m1[0]
	altered.ID = 999
	if altered.Key() != m1[0].Key() {
		t.Errorf("Key() must be independent of sequential ID: %q vs %q", altered.Key(), m1[0].Key())
	}
}
