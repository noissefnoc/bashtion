package mutator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// Category represents a class of mutation operators.
type Category string

const (
	CategoryLogicalOp   Category = "LOGICAL_OP"
	CategoryTestBinOp   Category = "TEST_BIN_OP"
	CategoryTestUnaryOp Category = "TEST_UNARY_OP"
	CategoryArithBinOp  Category = "ARITH_BIN_OP"
	CategoryArithCmpOp  Category = "ARITH_CMP_OP"
	CategoryReturnCode  Category = "RETURN_CODE"
	CategoryBoolCmd     Category = "BOOL_CMD"
	CategoryParamExp    Category = "PARAM_DEFAULT"
	CategoryPipelineNeg Category = "PIPELINE_NEG"
	CategoryParamRepl   Category = "PARAM_REPL"
	CategoryParamSlice  Category = "PARAM_SLICE"
	CategoryLoopCtrl    Category = "LOOP_CTRL"
	CategoryCaseCtrl    Category = "CASE_CTRL"
)

// AllCategories lists all valid mutation categories.
var AllCategories = []Category{
	CategoryLogicalOp,
	CategoryTestBinOp,
	CategoryTestUnaryOp,
	CategoryArithBinOp,
	CategoryArithCmpOp,
	CategoryReturnCode,
	CategoryBoolCmd,
	CategoryParamExp,
	CategoryPipelineNeg,
	CategoryParamRepl,
	CategoryParamSlice,
	CategoryLoopCtrl,
	CategoryCaseCtrl,
}

// IsValidCategory reports whether cat is a recognized mutation category.
func IsValidCategory(cat Category) bool {
	for _, c := range AllCategories {
		if c == cat {
			return true
		}
	}
	return false
}

// GenerateOptions configures optional filtering during mutant generation.
type GenerateOptions struct {
	DisabledCategories []Category
	// AllowedLines restricts mutant generation to the specified 1-based line numbers when non-nil (used by --git-diff).
	AllowedLines map[uint]bool
}

// Mutant represents a single concrete source mutation derived from the Bash AST.
type Mutant struct {
	ID           int
	FilePath     string
	FuncName     string
	Category     Category
	Description  string
	Line         uint
	CoverageLine uint
	Col          uint
	StartOffset  int
	EndOffset    int
	ScopeStart   int
	ScopeEnd     int
	Original     string
	Replacement  string
	// OriginalSrc references the shared, read-only target script buffer so each mutant does not
	// duplicate the full source in memory. Use Apply or ApplyToBuffer to materialize the mutated file.
	OriginalSrc []byte
}

// Key returns a deterministic, run-independent identifier (16-hex-char SHA-256 prefix)
// derived from the mutant's normalized file path, line, column, category, original/replacement
// text, and description. Unlike sequential ID, Key remains stable across runs with different
// target sets, --git-diff filters, or --disable-category options.
func (m Mutant) Key() string {
	h := sha256.New()
	cleanPath := filepath.ToSlash(filepath.Clean(m.FilePath))
	_, _ = fmt.Fprintf(h, "file=%d:%s\nline=%d\ncol=%d\ncat=%d:%s\norig=%d:%s\nrep=%d:%s\ndesc=%d:%s\n",
		len(cleanPath), cleanPath,
		m.Line, m.Col,
		len(m.Category), m.Category,
		len(m.Original), m.Original,
		len(m.Replacement), m.Replacement,
		len(m.Description), m.Description,
	)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// UncoveredHint returns an actionable suggestion when the mutated line was never executed during baseline coverage.
func (m Mutant) UncoveredHint() string {
	return fmt.Sprintf(
		"Line %d in %s was never executed during the baseline test run (uncovered code path). Add a .bats @test case that exercises this branch and asserts its outcome.",
		m.Line, m.funcOrScript(),
	)
}

// Hint returns an actionable suggestion for an LLM Agent or developer explaining how to kill this mutant.
func (m Mutant) Hint() string {
	switch m.Category {
	case CategoryReturnCode:
		return fmt.Sprintf(
			"Exit status assertion is missing or incomplete. In the .bats test covering %s (around line %d), assert `[ \"$status\" -eq %s ]` after `run`.",
			m.funcOrScript(), m.Line, m.Original,
		)
	case CategoryTestBinOp, CategoryArithCmpOp:
		if isBoundaryMutation(m.Original, m.Replacement) {
			return fmt.Sprintf(
				"Boundary value test case is missing in %s (line %d). Add a .bats test exercising the exact boundary input where `%s` and `%s` produce different results.",
				m.funcOrScript(), m.Line, m.Original, m.Replacement,
			)
		}
		return fmt.Sprintf(
			"Comparison condition `%s` in %s (line %d) was inverted to `%s` without failing any test. Add or tighten assertions for both true and false branches.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryLogicalOp:
		return fmt.Sprintf(
			"Logical operator `%s` in %s (line %d) was replaced with `%s`. Add a test case where only one side of the condition is true to verify short-circuit/branch behavior.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryTestUnaryOp:
		return fmt.Sprintf(
			"Unary check `%s` in %s (line %d) was mutated to `%s`. Add test cases covering both empty/non-empty (or existing/missing file) inputs.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryArithBinOp:
		return fmt.Sprintf(
			"Arithmetic operator `%s` in %s (line %d) was mutated to `%s`. Assert the computed numeric output or side effect in your .bats test.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryParamExp:
		return fmt.Sprintf(
			"Parameter expansion `%s` in %s (line %d) was mutated to `%s`. Add a test calling %s with an empty or omitted argument to verify default/fallback expansion.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement, m.funcOrScript(),
		)
	case CategoryBoolCmd:
		return fmt.Sprintf(
			"Boolean command `%s` in %s (line %d) was flipped to `%s`. Verify the branch side effect or exit code in your .bats test.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryPipelineNeg:
		return fmt.Sprintf(
			"Pipeline/command negation `%s` in %s (line %d) was inverted to `%s`. Add a test case verifying both success and failure outcomes of the command.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryParamRepl:
		return fmt.Sprintf(
			"Parameter replacement/transformation `%s` in %s (line %d) was mutated to `%s`. Add a test input with multiple occurrences or mixed-case characters to verify the exact transformation.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryParamSlice:
		return fmt.Sprintf(
			"Slice boundary `%s` in %s (line %d) was shifted to `%s`. Assert the exact sliced elements or substring length in your .bats test.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryLoopCtrl:
		return fmt.Sprintf(
			"Loop control statement `%s` in %s (line %d) was replaced with `%s`. Add a test with multiple loop iterations to verify whether the loop terminates early or continues.",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	case CategoryCaseCtrl:
		return fmt.Sprintf(
			"Case branch operator `%s` in %s (line %d) was mutated to `%s`. Verify that matching this case branch does not unintentionally fall through to subsequent branches (or vice versa).",
			m.Original, m.funcOrScript(), m.Line, m.Replacement,
		)
	default:
		return "Add or strengthen a .bats test assertion that exercises line " + fmt.Sprint(m.Line) + "."
	}
}

func (m Mutant) funcOrScript() string {
	if m.FuncName != "" {
		return fmt.Sprintf("function `%s()`", m.FuncName)
	}
	return fmt.Sprintf("`%s`", m.FilePath)
}

func isBoundaryMutation(orig, rep string) bool {
	switch orig {
	case "-lt":
		return rep == "-le"
	case "-le":
		return rep == "-lt"
	case "-gt":
		return rep == "-ge"
	case "-ge":
		return rep == "-gt"
	case "<":
		return rep == "<="
	case "<=":
		return rep == "<"
	case ">":
		return rep == ">="
	case ">=":
		return rep == ">"
	default:
		return false
	}
}

// Apply materializes the mutated script bytes using m.OriginalSrc (or fallbackSrc if empty).
func (m Mutant) Apply(fallbackSrc []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := m.ApplyToBuffer(&buf, fallbackSrc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ApplyToBuffer writes the materialized mutated script bytes into dst, resetting dst first.
func (m Mutant) ApplyToBuffer(dst *bytes.Buffer, fallbackSrc []byte) error {
	src := fallbackSrc
	if len(src) == 0 {
		src = m.OriginalSrc
	}
	if len(src) == 0 {
		return fmt.Errorf("missing base source for mutant %d", m.ID)
	}
	if m.StartOffset < 0 || m.EndOffset > len(src) || m.StartOffset > m.EndOffset {
		return fmt.Errorf("mutant %d offsets [%d:%d] out of range for source length %d", m.ID, m.StartOffset, m.EndOffset, len(src))
	}
	dst.Reset()
	dst.Grow(len(src) - (m.EndOffset - m.StartOffset) + len(m.Replacement))
	dst.Write(src[:m.StartOffset])
	dst.WriteString(m.Replacement)
	dst.Write(src[m.EndOffset:])
	return nil
}

// UnifiedDiff generates a concise contextual unified diff for a mutant by scanning only surrounding lines.
func (m Mutant) UnifiedDiff(originalSrc []byte) string {
	if m.StartOffset < 0 || m.EndOffset > len(originalSrc) || m.StartOffset >= m.EndOffset {
		return fmt.Sprintf("- %s\n+ %s", m.Original, m.Replacement)
	}

	// Locate start and end of the mutated line in originalSrc
	lineStart := m.StartOffset
	for lineStart > 0 && originalSrc[lineStart-1] != '\n' {
		lineStart--
	}
	lineEnd := m.EndOffset
	for lineEnd < len(originalSrc) && originalSrc[lineEnd] != '\n' {
		lineEnd++
	}

	// Collect up to 2 preceding lines
	var beforeLines []string
	curStart := lineStart
	for i := 0; i < 2 && curStart > 0; i++ {
		prevEnd := curStart - 1
		prevStart := prevEnd
		for prevStart > 0 && originalSrc[prevStart-1] != '\n' {
			prevStart--
		}
		beforeLines = append(beforeLines, string(originalSrc[prevStart:prevEnd]))
		curStart = prevStart
	}

	// Collect up to 2 trailing lines (excluding a trailing empty line after final newline)
	var afterLines []string
	curEnd := lineEnd
	for i := 0; i < 2 && curEnd < len(originalSrc); i++ {
		nextStart := curEnd + 1
		if nextStart >= len(originalSrc) {
			break
		}
		nextEnd := nextStart
		for nextEnd < len(originalSrc) && originalSrc[nextEnd] != '\n' {
			nextEnd++
		}
		afterLines = append(afterLines, string(originalSrc[nextStart:nextEnd]))
		curEnd = nextEnd
	}

	origLines := strings.Split(string(originalSrc[lineStart:lineEnd]), "\n")
	mutLines := strings.Split(string(originalSrc[lineStart:m.StartOffset])+m.Replacement+string(originalSrc[m.EndOffset:lineEnd]), "\n")

	startLineNum := int(m.Line) - len(beforeLines)
	if startLineNum < 1 {
		startLineNum = 1
	}
	origCount := len(beforeLines) + len(origLines) + len(afterLines)
	mutCount := len(beforeLines) + len(mutLines) + len(afterLines)

	var b strings.Builder
	b.Grow(256)
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n",
		m.FilePath, m.FilePath,
		startLineNum, origCount,
		startLineNum, mutCount,
	)
	for i := len(beforeLines) - 1; i >= 0; i-- {
		b.WriteByte(' ')
		b.WriteString(beforeLines[i])
		b.WriteByte('\n')
	}
	for _, ol := range origLines {
		b.WriteByte('-')
		b.WriteString(ol)
		b.WriteByte('\n')
	}
	for _, ml := range mutLines {
		b.WriteByte('+')
		b.WriteString(ml)
		b.WriteByte('\n')
	}
	for _, l := range afterLines {
		b.WriteByte(' ')
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// Generate parses a Bash 5.x script and returns all valid single-site mutants.
func Generate(filePath string, src []byte) ([]Mutant, error) {
	return GenerateWithOptions(filePath, src, GenerateOptions{})
}

type funcSpan struct {
	name        string
	startLine   uint
	endLine     uint
	startOffset int
	endOffset   int
}

type clauseSpan struct {
	startLine   uint
	startOffset int
	endOffset   int
}

// GenerateWithOptions parses a Bash 5.x script and returns all valid single-site mutants
// respecting comment pragmas (# bashtion: disable / # bashtion: disable-next-line) and options.
func GenerateWithOptions(filePath string, src []byte, opts GenerateOptions) ([]Mutant, error) {
	parser := syntax.NewParser(
		syntax.KeepComments(true),
		syntax.Variant(syntax.LangBash),
	)
	file, err := parser.Parse(bytes.NewReader(src), filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", filePath, err)
	}

	disabledLines, fileDisabled := collectDisabledLinesFromAST(file)
	if fileDisabled {
		return nil, nil
	}

	sharedSrc := bytes.Clone(src)

	var funcs []funcSpan
	var clauses []clauseSpan

	resolveFunc := func(line uint, offset int) (string, int, int) {
		for i := len(funcs) - 1; i >= 0; i-- {
			fn := funcs[i]
			if line >= fn.startLine && line <= fn.endLine && offset >= fn.startOffset && offset <= fn.endOffset {
				return fn.name, fn.startOffset, fn.endOffset
			}
		}
		return "", 0, len(src)
	}

	resolveClauseStartLine := func(line uint, offset int) uint {
		for i := len(clauses) - 1; i >= 0; i-- {
			cl := clauses[i]
			if offset >= cl.startOffset && offset <= cl.endOffset {
				if cl.startLine != line {
					return cl.startLine
				}
				return 0
			}
		}
		return 0
	}

	disabledCat := make(map[Category]bool, len(opts.DisabledCategories))
	for _, c := range opts.DisabledCategories {
		disabledCat[c] = true
	}

	// Reusable syntax checker and buffer for scoped O(1) syntax verification
	checkParser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	var scopeBuf bytes.Buffer
	var reader bytes.Reader

	var candidates []Mutant

	addCandidateWithCovLine := func(cat Category, desc string, pos syntax.Pos, covLine uint, start, end int, replacements ...string) {
		if disabledCat[cat] {
			return
		}
		line := pos.Line()
		if disabledLines[line] {
			return
		}
		if opts.AllowedLines != nil && !opts.AllowedLines[line] {
			return
		}
		if start < 0 || end > len(src) || start >= end {
			return
		}
		orig := string(src[start:end])
		fnName, scopeStart, scopeEnd := resolveFunc(line, start)
		if covLine == 0 || covLine == line {
			covLine = resolveClauseStartLine(line, start)
		}

		for _, rep := range replacements {
			if rep == orig {
				continue
			}

			// Scoped syntax check: validate only the enclosing function (or file when top-level).
			// If a function-scoped slice fails to parse on its own (e.g. trailing heredoc body after `}`),
			// fall back to full-file syntax validation before discarding the candidate.
			scopeBuf.Reset()
			scopeBuf.Grow((scopeEnd - scopeStart) - len(orig) + len(rep))
			scopeBuf.Write(src[scopeStart:start])
			scopeBuf.WriteString(rep)
			scopeBuf.Write(src[end:scopeEnd])

			reader.Reset(scopeBuf.Bytes())
			if _, err := checkParser.Parse(&reader, filePath); err != nil {
				if scopeStart == 0 && scopeEnd == len(src) {
					continue
				}
				scopeBuf.Reset()
				scopeBuf.Grow(len(src) - len(orig) + len(rep))
				scopeBuf.Write(src[:start])
				scopeBuf.WriteString(rep)
				scopeBuf.Write(src[end:])
				reader.Reset(scopeBuf.Bytes())
				if _, fullErr := checkParser.Parse(&reader, filePath); fullErr != nil {
					continue
				}
			}

			candidates = append(candidates, Mutant{
				ID:           len(candidates) + 1,
				FilePath:     filePath,
				FuncName:     fnName,
				Category:     cat,
				Description:  desc,
				Line:         line,
				CoverageLine: covLine,
				Col:          pos.Col(),
				StartOffset:  start,
				EndOffset:    end,
				ScopeStart:   scopeStart,
				ScopeEnd:     scopeEnd,
				Original:     orig,
				Replacement:  rep,
				OriginalSrc:  sharedSrc,
			})
		}
	}

	addCandidate := func(cat Category, desc string, pos syntax.Pos, start, end int, replacements ...string) {
		addCandidateWithCovLine(cat, desc, pos, 0, start, end, replacements...)
	}

	// Track negated UnaryTest children so we don't turn `[[ ! -f x ]]` into `[[ ! ! -f x ]]`
	negatedTestNodes := make(map[syntax.Node]bool)
	// Track condition statements of if/while/until so unnegated pipeline/command conditions can be negated
	condStmts := make(map[*syntax.Stmt]bool)

	syntax.Walk(file, func(node syntax.Node) bool {
		if node == nil {
			return false
		}

		switch n := node.(type) {
		case *syntax.FuncDecl:
			if fn := n; fn.Name != nil {
				funcs = append(funcs, funcSpan{
					name:        fn.Name.Value,
					startLine:   fn.Pos().Line(),
					endLine:     fn.End().Line(),
					startOffset: int(fn.Pos().Offset()),
					endOffset:   int(fn.End().Offset()),
				})
			}

		case *syntax.TestClause, *syntax.ArithmCmd, *syntax.ArithmExp, *syntax.DeclClause:
			if n.Pos().Line() != n.End().Line() {
				clauses = append(clauses, clauseSpan{
					startLine:   n.Pos().Line(),
					startOffset: int(n.Pos().Offset()),
					endOffset:   int(n.End().Offset()),
				})
			}

		case *syntax.IfClause:
			for cur := n; cur != nil; cur = cur.Else {
				if len(cur.Cond) > 0 {
					condStmts[cur.Cond[len(cur.Cond)-1]] = true
				}
			}

		case *syntax.WhileClause:
			if len(n.Cond) > 0 {
				condStmts[n.Cond[len(n.Cond)-1]] = true
			}

		case *syntax.Stmt:
			mutateStmtNegation(n, src, condStmts[n], addCandidate)

		case *syntax.CaseClause:
			mutateCaseClause(n, src, addCandidateWithCovLine)

		case *syntax.BinaryCmd:
			// Logical command operators: && <-> ||
			start := int(n.OpPos.Offset())
			opStr := n.Op.String()
			end := start + len(opStr)
			covLine := uint(0)
			if n.OpPos.Line() != n.Pos().Line() {
				covLine = n.Pos().Line()
			}
			switch n.Op {
			case syntax.AndStmt:
				addCandidateWithCovLine(CategoryLogicalOp, "replace && with ||", n.OpPos, covLine, start, end, "||")
			case syntax.OrStmt:
				addCandidateWithCovLine(CategoryLogicalOp, "replace || with &&", n.OpPos, covLine, start, end, "&&")
			}

		case *syntax.BinaryTest:
			// Conditional expressions inside [[ ... ]]
			if n.Op == syntax.TsReMatch {
				if !negatedTestNodes[n] {
					exprStart := int(n.Pos().Offset())
					exprEnd := int(n.End().Offset())
					if exprStart >= 0 && exprEnd <= len(src) && exprStart < exprEnd {
						origExpr := string(src[exprStart:exprEnd])
						addCandidate(
							CategoryTestBinOp,
							"negate =~ regex match in [[ ]]",
							n.Pos(),
							exprStart,
							exprEnd,
							"! "+origExpr,
						)
					}
				}
				return true
			}
			start := int(n.OpPos.Offset())
			end := scanTokenEnd(src, start, int(n.Y.Pos().Offset()))
			origOp := string(src[start:end])
			reps := testBinaryReplacements(origOp, false)
			for _, rep := range reps {
				addCandidate(
					CategoryTestBinOp,
					fmt.Sprintf("replace %s with %s in [[ ]]", origOp, rep),
					n.OpPos,
					start,
					end,
					rep,
				)
			}

		case *syntax.UnaryTest:
			// Unary expressions inside [[ ... ]]: -z <-> -n, -f -> ! -f, ! expr -> expr
			if n.Op == syntax.TsNot {
				negatedTestNodes[n.X] = true
				start := int(n.Pos().Offset())
				end := int(n.X.Pos().Offset())
				if start >= 0 && end > start && end <= len(src) {
					cat := CategoryTestUnaryOp
					if _, isBin := n.X.(*syntax.BinaryTest); isBin {
						cat = CategoryTestBinOp
					}
					addCandidate(
						cat,
						"remove ! negation in [[ ]]",
						n.Pos(),
						start,
						end,
						"",
					)
				}
				return true
			}
			start := int(n.OpPos.Offset())
			end := scanTokenEnd(src, start, int(n.X.Pos().Offset()))
			origOp := string(src[start:end])
			reps := testUnaryReplacements(origOp, !negatedTestNodes[n])
			for _, rep := range reps {
				addCandidate(
					CategoryTestUnaryOp,
					fmt.Sprintf("replace %s with %s in [[ ]]", origOp, rep),
					n.OpPos,
					start,
					end,
					rep,
				)
			}

		case *syntax.BinaryArithm:
			// Arithmetic expressions inside (( ... )) and $(( ... ))
			start := int(n.OpPos.Offset())
			opStr := n.Op.String()
			end := start + len(opStr)
			cat, reps := arithBinaryReplacements(n.Op)
			for _, rep := range reps {
				addCandidate(
					cat,
					fmt.Sprintf("replace %s with %s in (( ))", opStr, rep),
					n.OpPos,
					start,
					end,
					rep,
				)
			}

		case *syntax.ParamExp:
			// Parameter expansions: default/alt, prefix/suffix, replace, case, slice
			mutateParamExp(n, src, addCandidate)

		case *syntax.CallExpr:
			if n.Pos().Line() != n.End().Line() {
				clauses = append(clauses, clauseSpan{
					startLine:   n.Pos().Line(),
					startOffset: int(n.Pos().Offset()),
					endOffset:   int(n.End().Offset()),
				})
			}
			mutateCallExpr(n, addCandidate)
		}

		return true
	})

	return candidates, nil
}

func mutateStmtNegation(stmt *syntax.Stmt, src []byte, isCondStmt bool, addCandidate func(Category, string, syntax.Pos, int, int, ...string)) {
	if stmt.Cmd == nil {
		return
	}
	if stmt.Negated {
		bangStart := int(stmt.Position.Offset())
		cmdStart := int(stmt.Cmd.Pos().Offset())
		if bangStart >= 0 && cmdStart <= len(src) && bangStart < cmdStart && src[bangStart] == '!' {
			end := bangStart + 1
			for end < cmdStart && (src[end] == ' ' || src[end] == '\t') {
				end++
			}
			addCandidate(
				CategoryPipelineNeg,
				"remove pipeline negation !",
				stmt.Position,
				bangStart,
				end,
				"",
			)
		}
		return
	}

	if !isCondStmt {
		return
	}

	// Only add `! ` to condition statements that invoke regular commands or pipelines
	// (avoid duplicating mutations for [[ ]], [ ], (( )), true, false)
	if !isNegatableCondCmd(stmt.Cmd) {
		return
	}
	cmdPos := stmt.Cmd.Pos()
	start := int(cmdPos.Offset())
	end := scanTokenEnd(src, start, int(stmt.Cmd.End().Offset()))
	if start >= 0 && end <= len(src) && start < end {
		origTok := string(src[start:end])
		addCandidate(
			CategoryPipelineNeg,
			"negate condition command with !",
			cmdPos,
			start,
			end,
			"! "+origTok,
		)
	}
}

func isNegatableCondCmd(cmd syntax.Command) bool {
	switch c := cmd.(type) {
	case *syntax.BinaryCmd:
		return c.Op == syntax.Pipe || c.Op == syntax.PipeAll
	case *syntax.CallExpr:
		if len(c.Args) == 0 {
			return false
		}
		lit := c.Args[0].Lit()
		switch lit {
		case "", "[", "test", "true", "false", "return", "exit", "break", "continue":
			return false
		default:
			return true
		}
	default:
		return false
	}
}

func mutateCaseClause(clause *syntax.CaseClause, src []byte, addCandidateWithCovLine func(Category, string, syntax.Pos, uint, int, int, ...string)) {
	for i, item := range clause.Items {
		if !item.OpPos.IsValid() {
			continue
		}
		opStr := item.Op.String()
		start := int(item.OpPos.Offset())
		end := start + len(opStr)
		if start < 0 || end > len(src) || string(src[start:end]) != opStr {
			continue
		}
		var covLine uint
		if len(item.Stmts) > 0 {
			stmtLine := item.Stmts[len(item.Stmts)-1].Pos().Line()
			if stmtLine != item.OpPos.Line() {
				covLine = stmtLine
			}
		}
		switch item.Op {
		case syntax.Break: // ;;
			// Mutate ;; to ;& (fallthrough) on non-final, non-empty branches unless control unconditionally exits the branch
			if i < len(clause.Items)-1 && len(item.Stmts) > 0 && !isTerminatingCaseBranch(item.Stmts) {
				addCandidateWithCovLine(
					CategoryCaseCtrl,
					"replace ;; with ;& in case branch",
					item.OpPos,
					covLine,
					start,
					end,
					";&",
				)
			}
		case syntax.Fallthrough, syntax.Resume: // ;& or ;;&
			addCandidateWithCovLine(
				CategoryCaseCtrl,
				fmt.Sprintf("replace %s with ;; in case branch", opStr),
				item.OpPos,
				covLine,
				start,
				end,
				";;",
			)
		}
	}
}

func isTerminatingCaseBranch(stmts []*syntax.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	last := stmts[len(stmts)-1]
	if last.Background || last.Coprocess {
		return false
	}
	call, ok := last.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	switch call.Args[0].Lit() {
	case "return", "exit", "break", "continue":
		return true
	default:
		return false
	}
}

func mutateParamExp(n *syntax.ParamExp, src []byte, addCandidate func(Category, string, syntax.Pos, int, int, ...string)) {
	if n.Param == nil {
		return
	}

	if n.Repl != nil {
		mutateParamReplace(n, src, addCandidate)
	}
	if n.Slice != nil {
		mutateParamSlice(n, src, addCandidate)
	}
	if n.Exp == nil {
		return
	}

	opStart := paramExpOpStart(n, src)
	opStr := n.Exp.Op.String()
	opEnd := opStart + len(opStr)
	if opStart < 0 || opEnd > len(src) || string(src[opStart:opEnd]) != opStr {
		return
	}

	cat := CategoryParamExp
	var reps []string
	switch n.Exp.Op {
	case syntax.AlternateUnsetOrNull: // :+
		reps = []string{":-"}
	case syntax.DefaultUnsetOrNull: // :-
		reps = []string{":+"}
	case syntax.AlternateUnset: // +
		reps = []string{"-"}
	case syntax.DefaultUnset: // -
		reps = []string{"+"}
	case syntax.RemSmallPrefix: // #
		if paramExpPatternHasGlob(n, src) {
			reps = []string{"##"}
		}
	case syntax.RemLargePrefix: // ##
		if paramExpPatternHasGlob(n, src) {
			reps = []string{"#"}
		}
	case syntax.RemSmallSuffix: // %
		if paramExpPatternHasGlob(n, src) {
			reps = []string{"%%"}
		}
	case syntax.RemLargeSuffix: // %%
		if paramExpPatternHasGlob(n, src) {
			reps = []string{"%"}
		}
	case syntax.UpperAll: // ^^
		cat = CategoryParamRepl
		reps = []string{",,"}
	case syntax.LowerAll: // ,,
		cat = CategoryParamRepl
		reps = []string{"^^"}
	case syntax.UpperFirst: // ^
		cat = CategoryParamRepl
		reps = []string{","}
	case syntax.LowerFirst: // ,
		cat = CategoryParamRepl
		reps = []string{"^"}
	}

	opPos := syntax.NewPos(uint(opStart), n.Param.End().Line(), n.Param.End().Col())
	for _, rep := range reps {
		addCandidate(
			cat,
			fmt.Sprintf("replace %s with %s in ${%s...}", opStr, rep, n.Param.Value),
			opPos,
			opStart,
			opEnd,
			rep,
		)
	}
}

func paramExpOpStart(n *syntax.ParamExp, src []byte) int {
	pos := int(n.Param.End().Offset())
	if n.Index != nil {
		idxEnd := int(n.Index.End().Offset())
		for idxEnd < len(src) && src[idxEnd] != ']' {
			idxEnd++
		}
		if idxEnd < len(src) && src[idxEnd] == ']' {
			pos = idxEnd + 1
		}
	}
	return pos
}

func paramExpPatternHasGlob(n *syntax.ParamExp, _ []byte) bool {
	if n == nil || n.Exp == nil || n.Exp.Word == nil {
		return false
	}
	for _, part := range n.Exp.Word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if litHasUnescapedGlob(p.Value) {
				return true
			}
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ExtGlob:
			return true
		case *syntax.SglQuoted, *syntax.DblQuoted:
			// Quoted patterns in ${var#"*"} or ${var#'*'} match literally in Bash,
			// so # and ## (or % and %%) are equivalent on purely quoted patterns.
			continue
		}
	}
	return false
}

func litHasUnescapedGlob(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '*' || s[i] == '?' || s[i] == '[' {
			return true
		}
	}
	return false
}

func mutateParamReplace(n *syntax.ParamExp, src []byte, addCandidate func(Category, string, syntax.Pos, int, int, ...string)) {
	slashStart := paramExpOpStart(n, src)
	if slashStart < 0 || slashStart >= len(src) || src[slashStart] != '/' {
		return
	}
	opPos := syntax.NewPos(uint(slashStart), n.Param.End().Line(), n.Param.End().Col())
	if n.Repl.All {
		if slashStart+2 <= len(src) && string(src[slashStart:slashStart+2]) == "//" {
			addCandidate(
				CategoryParamRepl,
				fmt.Sprintf("replace // with / in ${%s//...}", n.Param.Value),
				opPos,
				slashStart,
				slashStart+2,
				"/",
			)
		}
	} else {
		addCandidate(
			CategoryParamRepl,
			fmt.Sprintf("replace / with // in ${%s/...}", n.Param.Value),
			opPos,
			slashStart,
			slashStart+1,
			"//",
		)
	}
}

func mutateParamSlice(n *syntax.ParamExp, src []byte, addCandidate func(Category, string, syntax.Pos, int, int, ...string)) {
	mutateArithLiteral := func(expr syntax.ArithmExpr, label string, isLength bool) {
		w, ok := expr.(*syntax.Word)
		if !ok || len(w.Parts) != 1 {
			return
		}
		lit, ok := w.Parts[0].(*syntax.Lit)
		if !ok {
			return
		}
		val, err := strconv.Atoi(strings.TrimSpace(lit.Value))
		if err != nil || val < 0 {
			return
		}
		start := int(lit.Pos().Offset())
		end := int(lit.End().Offset())
		var reps []string
		if isLength {
			if val >= 1 {
				reps = append(reps, strconv.Itoa(val+1))
				if val > 1 {
					reps = append(reps, strconv.Itoa(val-1))
				}
			}
		} else {
			reps = append(reps, strconv.Itoa(val+1))
			if val > 0 {
				reps = append(reps, strconv.Itoa(val-1))
			}
		}
		for _, rep := range reps {
			addCandidate(
				CategoryParamSlice,
				fmt.Sprintf("replace slice %s %s with %s in ${%s:...}", label, lit.Value, rep, n.Param.Value),
				lit.Pos(),
				start,
				end,
				rep,
			)
		}
	}

	if n.Slice.Offset != nil {
		mutateArithLiteral(n.Slice.Offset, "offset", false)
	}
	if n.Slice.Length != nil {
		mutateArithLiteral(n.Slice.Length, "length", true)
	}
}

func mutateCallExpr(call *syntax.CallExpr, addCandidate func(Category, string, syntax.Pos, int, int, ...string)) {
	if len(call.Args) == 0 {
		return
	}
	cmdLit := call.Args[0].Lit()
	cmdPos := call.Args[0].Pos()
	cmdStart := int(cmdPos.Offset())
	cmdEnd := int(call.Args[0].End().Offset())

	switch cmdLit {
	case "return", "exit":
		if len(call.Args) >= 2 {
			arg := call.Args[1]
			argLit := arg.Lit()
			start := int(arg.Pos().Offset())
			end := int(arg.End().Offset())
			switch argLit {
			case "0":
				addCandidate(CategoryReturnCode, fmt.Sprintf("replace %s 0 with %s 1", cmdLit, cmdLit), arg.Pos(), start, end, "1")
			case "1":
				addCandidate(CategoryReturnCode, fmt.Sprintf("replace %s 1 with %s 0", cmdLit, cmdLit), arg.Pos(), start, end, "0")
			}
		}

	case "true":
		if len(call.Args) == 1 {
			addCandidate(CategoryBoolCmd, "replace true with false", cmdPos, cmdStart, cmdEnd, "false")
		}

	case "false":
		if len(call.Args) == 1 {
			addCandidate(CategoryBoolCmd, "replace false with true", cmdPos, cmdStart, cmdEnd, "true")
		}

	case "break":
		if len(call.Args) == 1 {
			addCandidate(CategoryLoopCtrl, "replace break with continue", cmdPos, cmdStart, cmdEnd, "continue")
		}

	case "continue":
		if len(call.Args) == 1 {
			addCandidate(CategoryLoopCtrl, "replace continue with break", cmdPos, cmdStart, cmdEnd, "break")
		}

	case "[", "test":
		// Mutate binary and unary test operators inside POSIX [ ... ] or test ...
		for i := 1; i < len(call.Args); i++ {
			arg := call.Args[i]
			lit := arg.Lit()
			if lit == "" || lit == "]" {
				continue
			}
			// Require a following operand (not end-of-args or closing ']') so 1-arg string checks like `[ -n ]` are not mutated
			hasOperand := i+1 < len(call.Args) && call.Args[i+1].Lit() != "]"
			if !hasOperand {
				continue
			}
			start := int(arg.Pos().Offset())
			end := int(arg.End().Offset())

			if lit == "!" {
				nextStart := int(call.Args[i+1].Pos().Offset())
				if nextStart > start {
					addCandidate(
						CategoryTestUnaryOp,
						fmt.Sprintf("remove ! negation in %s", cmdLit),
						arg.Pos(),
						start,
						nextStart,
						"",
					)
				}
				continue
			}

			if reps := testBinaryReplacements(lit, true); len(reps) > 0 {
				for _, rep := range reps {
					addCandidate(
						CategoryTestBinOp,
						fmt.Sprintf("replace %s with %s in %s", lit, rep, cmdLit),
						arg.Pos(),
						start,
						end,
						rep,
					)
				}
				// Skip the right-hand operand of the binary operator so literal operands like `[ "$x" = -n ]` are not mutated
				i++
				continue
			}

			// Do not treat an operand as a unary operator when followed by a binary test operator and RHS operand (e.g. `[ -f = "$x" ]`)
			nextIsBinaryWithRHS := i+2 < len(call.Args) && call.Args[i+2].Lit() != "]" && isBinaryTestOperator(call.Args[i+1].Lit())
			if nextIsBinaryWithRHS {
				continue
			}

			allowNegate := i == 1 || call.Args[i-1].Lit() != "!"
			if reps := testUnaryReplacements(lit, allowNegate); len(reps) > 0 {
				for _, rep := range reps {
					addCandidate(
						CategoryTestUnaryOp,
						fmt.Sprintf("replace %s with %s in %s", lit, rep, cmdLit),
						arg.Pos(),
						start,
						end,
						rep,
					)
				}
				// Skip the operand of the unary operator
				i++
			}
		}
	}
}

func isBinaryTestOperator(op string) bool {
	if len(testBinaryReplacements(op, true)) > 0 {
		return true
	}
	switch op {
	case "-ef", "-nt", "-ot", "<", ">":
		return true
	default:
		return false
	}
}

func testBinaryReplacements(op string, posix bool) []string {
	switch op {
	case "==", "=":
		return []string{"!="}
	case "!=":
		if posix {
			return []string{"="}
		}
		return []string{"=="}
	case "-eq":
		return []string{"-ne"}
	case "-ne":
		return []string{"-eq"}
	case "-lt":
		return []string{"-le", "-ge"}
	case "-le":
		return []string{"-lt", "-gt"}
	case "-gt":
		return []string{"-ge", "-le"}
	case "-ge":
		return []string{"-gt", "-lt"}
	default:
		return nil
	}
}

func testUnaryReplacements(op string, allowNegate bool) []string {
	switch op {
	case "-z":
		return []string{"-n"}
	case "-n":
		return []string{"-z"}
	case "-e", "-f", "-d", "-s", "-r", "-w", "-x", "-L":
		if allowNegate {
			return []string{"! " + op}
		}
		return nil
	default:
		return nil
	}
}

func arithBinaryReplacements(op syntax.BinAritOperator) (Category, []string) {
	switch op {
	case syntax.Add:
		return CategoryArithBinOp, []string{"-"}
	case syntax.Sub:
		return CategoryArithBinOp, []string{"+"}
	case syntax.Mul:
		return CategoryArithBinOp, []string{"/"}
	case syntax.Quo:
		return CategoryArithBinOp, []string{"*"}
	case syntax.Eql:
		return CategoryArithCmpOp, []string{"!="}
	case syntax.Neq:
		return CategoryArithCmpOp, []string{"=="}
	case syntax.Lss:
		return CategoryArithCmpOp, []string{"<=", ">="}
	case syntax.Leq:
		return CategoryArithCmpOp, []string{"<", ">"}
	case syntax.Gtr:
		return CategoryArithCmpOp, []string{">=", "<="}
	case syntax.Geq:
		return CategoryArithCmpOp, []string{">", "<"}
	case syntax.AndArit:
		return CategoryLogicalOp, []string{"||"}
	case syntax.OrArit:
		return CategoryLogicalOp, []string{"&&"}
	default:
		return "", nil
	}
}

var pragmaRe = regexp.MustCompile(`^(?:bashtion|mutate):\s*(disable-file|disable-next-line|disable-line|disable)(?:\s.*)?$`)

// collectDisabledLinesFromAST extracts `# bashtion:` and `# mutate:` directives directly from
// AST Comment nodes so `# bashtion:` text inside heredocs or multi-line strings is never misidentified.
func collectDisabledLinesFromAST(file *syntax.File) (map[uint]bool, bool) {
	var disabled map[uint]bool
	fileDisabled := false

	syntax.Walk(file, func(node syntax.Node) bool {
		c, ok := node.(*syntax.Comment)
		if !ok {
			return true
		}
		lineNum := c.Hash.Line()
		rawComment := strings.TrimSpace(c.Text)
		cleanComment := strings.TrimSpace(strings.TrimPrefix(rawComment, "#"))

		m := pragmaRe.FindStringSubmatch(cleanComment)
		if len(m) < 2 {
			return true
		}
		action := m[1]

		if disabled == nil {
			disabled = make(map[uint]bool)
		}
		switch action {
		case "disable-file":
			fileDisabled = true
		case "disable-next-line":
			disabled[lineNum+1] = true
		case "disable", "disable-line":
			disabled[lineNum] = true
		}
		return true
	})

	return disabled, fileDisabled
}

func scanTokenEnd(src []byte, start, limit int) int {
	if limit > len(src) {
		limit = len(src)
	}
	i := start
	for i < limit {
		b := src[i]
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			break
		}
		i++
	}
	return i
}
