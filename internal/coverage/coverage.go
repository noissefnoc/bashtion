package coverage

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

const (
	traceMarker          = "__BASHTION_COV__:"
	targetMarker         = "__BASHTION_TARGET__:"
	fieldSep             = "__BASHTION_SEP__"
	endMarker            = "__BASHTION_END__"
	setupMarker          = "__SETUP__"
	maxFilterRegexLength = 2048
)

// Profile holds line-level execution coverage and per-line test mapping collected via BASH_XTRACEFD + PS4.
// This package has zero dependencies on bashtion internals so it can also be extracted as a standalone tool.
type Profile struct {
	// LineTests maps 1-based line numbers in the target script to the @test descriptions that executed that line.
	LineTests map[uint][]string
	// SetupLines records 1-based line numbers executed outside individual @test blocks (e.g. during setup() or file load).
	SetupLines map[uint]bool
}

// NewProfile initializes an empty coverage Profile.
func NewProfile() *Profile {
	return &Profile{
		LineTests:  make(map[uint][]string),
		SetupLines: make(map[uint]bool),
	}
}

// HasAnyCoverage returns true if at least one line in the target script was recorded during trace collection.
func (p *Profile) HasAnyCoverage() bool {
	if p == nil {
		return false
	}
	return len(p.LineTests) > 0 || len(p.SetupLines) > 0
}

// IsCovered reports whether the given 1-based line number was executed at least once during the baseline run.
func (p *Profile) IsCovered(line uint) bool {
	if p == nil {
		return true
	}
	if p.SetupLines[line] {
		return true
	}
	return len(p.LineTests[line]) > 0
}

// TestsForLine returns the slice of @test descriptions that executed the given 1-based line.
// If the line was executed during setup/global load, nil is returned to indicate all tests may depend on it.
func (p *Profile) TestsForLine(line uint) []string {
	if p == nil || p.SetupLines[line] {
		return nil
	}
	return p.LineTests[line]
}

// FilterRegexForLine builds a `bats --filter` regular expression for the tests covering the given line.
// Returns "" if the line is covered by setup(), covered by all tests (totalTests > 0 && len == totalTests), or uncovered.
func (p *Profile) FilterRegexForLine(line uint, totalTests int) string {
	tests := p.TestsForLine(line)
	if len(tests) == 0 {
		return ""
	}
	if totalTests > 0 && len(tests) >= totalTests {
		return ""
	}
	escaped := make([]string, 0, len(tests))
	for _, t := range tests {
		escaped = append(escaped, regexp.QuoteMeta(t))
	}
	re := "^(" + strings.Join(escaped, "|") + ")$"
	if len(re) > maxFilterRegexLength {
		return ""
	}
	return re
}

// Preamble returns a single-line Bash snippet that configures line coverage tracing:
//   - On Bash 4.1+ / 5.x: dynamically allocates an unused FD ({__bashtion_cov_fd}) once per shell process
//     to avoid colliding with bats-core FDs (e.g. FD 3) or leaking FDs on repeated `source` calls,
//     and sets BASH_XTRACEFD + PS4 with `set -x` without `eval` or environment variable export leakage.
//   - On Bash 3.2 (macOS default /bin/bash fallback): uses `set -o functrace` + `trap ... DEBUG` appending directly to the trace file.
func Preamble(traceFilePath string) string {
	escapedPath := strings.ReplaceAll(traceFilePath, `'`, `'\''`)
	return fmt.Sprintf(
		`__bashtion_cov_file='%s'; if (( BASH_VERSINFO[0] >= 4 )); then if [[ -z "${__bashtion_cov_fd:-}" ]]; then exec {__bashtion_cov_fd}>>"${__bashtion_cov_file}"; fi; printf '+%s%%s%s\n' "${BASH_SOURCE[0]:-${0:-}}" >&"${__bashtion_cov_fd}"; BASH_XTRACEFD="${__bashtion_cov_fd}"; PS4='+%s${BATS_TEST_DESCRIPTION:-%s}%s${BASH_SOURCE[0]:-}%s${LINENO}%s '; set -x; else printf '+%s%%s%s\n' "${BASH_SOURCE[0]:-${0:-}}" >> "${__bashtion_cov_file}"; set -o functrace; trap 'printf "+%s%%s%s%%s%s%%s%s \n" "${BATS_TEST_DESCRIPTION:-%s}" "${BASH_SOURCE[0]:-}" "${LINENO}" >> "${__bashtion_cov_file}"' DEBUG; fi`,
		escapedPath,
		targetMarker, endMarker,
		traceMarker, setupMarker, fieldSep, fieldSep, endMarker,
		targetMarker, endMarker,
		traceMarker, fieldSep, fieldSep, endMarker, setupMarker,
	)
}

// DisablesXtrace reports whether src contains commands that explicitly disable xtrace (such as `set +x` or `set +o xtrace`),
// which would make unrecorded lines in xtrace output unreliable as proof of dead/uncovered code.
var disableXtraceRe = regexp.MustCompile(`(?:^|[;\s(|&])(?:set\s+\+[a-zA-Z]*x|set\s+\+o\s+xtrace|shopt\s+-u[a-zA-Z]*o\s+xtrace)(?:$|[;\s)|&])`)

func DisablesXtrace(src []byte) bool {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	if file, err := parser.Parse(bytes.NewReader(src), ""); err == nil {
		found := false
		syntax.Walk(file, func(node syntax.Node) bool {
			if found {
				return false
			}
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			cmd := call.Args[0].Lit()
			switch cmd {
			case "set":
				for i := 1; i < len(call.Args); i++ {
					arg := call.Args[i].Lit()
					if strings.HasPrefix(arg, "+") && !strings.HasPrefix(arg, "++") {
						if strings.ContainsRune(arg[1:], 'x') {
							found = true
							return false
						}
						if arg == "+o" && i+1 < len(call.Args) && call.Args[i+1].Lit() == "xtrace" {
							found = true
							return false
						}
					}
				}
			case "shopt":
				hasU := false
				hasO := false
				hasXtrace := false
				for i := 1; i < len(call.Args); i++ {
					arg := call.Args[i].Lit()
					if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
						if strings.ContainsRune(arg[1:], 'u') {
							hasU = true
						}
						if strings.ContainsRune(arg[1:], 'o') {
							hasO = true
						}
					} else if arg == "xtrace" {
						hasXtrace = true
					}
				}
				if hasU && hasO && hasXtrace {
					found = true
					return false
				}
			}
			return true
		})
		return found
	}

	// Fallback for non-pure Bash files (such as .bats suites)
	for _, rawLine := range bytes.Split(src, []byte{'\n'}) {
		cleanLine := stripShellCommentBytes(rawLine)
		if disableXtraceRe.Match(cleanLine) {
			return true
		}
	}
	return false
}

func stripShellCommentBytes(line []byte) []byte {
	inSingle := false
	inDouble := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '\\' && !inSingle {
			i++
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
		} else if c == '"' && !inSingle {
			inDouble = !inDouble
		} else if c == '#' && !inSingle && !inDouble {
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == ';' || line[i-1] == '(' || line[i-1] == '&' || line[i-1] == '|' {
				return line[:i]
			}
		}
	}
	return line
}

// InstrumentScript injects the single-line coverage preamble into src and returns the instrumented script
// along with a lineOffset (1 because a single line is inserted).
// It preserves any leading shebang line and contiguous header comments (#...) so tools that extract doc comments
// from script headers (like pyenv-help) are not disrupted.
func InstrumentScript(src []byte, traceFilePath string) (instrumented []byte, lineOffset int) {
	preamble := Preamble(traceFilePath)
	insertIdx := findPreambleInsertionIndex(src)

	var buf bytes.Buffer
	buf.Grow(len(src) + len(preamble) + 2)
	buf.Write(src[:insertIdx])
	buf.WriteString(preamble)
	buf.WriteByte('\n')
	buf.Write(src[insertIdx:])
	return buf.Bytes(), 1
}

func findPreambleInsertionIndex(src []byte) int {
	if len(src) == 0 {
		return 0
	}

	offset := 0
	lines := bytes.SplitAfter(src, []byte{'\n'})
	if len(lines) == 0 {
		return 0
	}

	// 1. Skip shebang if present
	if bytes.HasPrefix(lines[0], []byte("#!")) {
		offset += len(lines[0])
		lines = lines[1:]
	}

	// 2. Skip contiguous header comments (#) and blank lines
	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			offset += len(line)
		} else {
			break
		}
	}

	return offset
}

// ParseTrace reads xtrace output produced with Preamble and extracts line coverage for targetFile.
// lineOffset is subtracted from recorded LINENO values (e.g. 1 when InstrumentScript inserted 1 preamble line).
func ParseTrace(r io.Reader, targetFile string, lineOffset int) (*Profile, error) {
	prof := NewProfile()
	cleanTarget := filepath.Clean(targetFile)
	targetBase := filepath.Base(cleanTarget)

	seenPerLine := make(map[uint]map[string]bool)
	verifiedSources := make(map[string]bool)
	br := bufio.NewReaderSize(r, 64*1024)
	const maxTraceLineInspectBytes = 64 * 1024

	for {
		line, readErr := readTraceLinePrefix(br, maxTraceLineInspectBytes)
		if len(line) > 0 {
			if strings.HasPrefix(line, "+"+targetMarker) {
				rest := line[1+len(targetMarker):]
				if endIdx := strings.Index(rest, endMarker); endIdx >= 0 {
					if regSource := rest[:endIdx]; regSource != "" {
						cleanReg := filepath.Clean(regSource)
						if filepath.Base(cleanReg) == targetBase {
							verifiedSources[cleanReg] = true
						}
					}
				}
			} else if idx := strings.Index(line, traceMarker); idx >= 0 {
				rest := line[idx+len(traceMarker):]
				// Format of rest: <test_desc>:<bash_source>:<lineno>__BASHTION_END__ <command>
				// (or legacy `: ` separator if __BASHTION_END__ is omitted)
				var header string
				if endIdx := strings.Index(rest, endMarker); endIdx >= 0 {
					header = rest[:endIdx]
				} else if colonSpace := strings.LastIndex(rest, ": "); colonSpace >= 0 {
					header = rest[:colonSpace]
				}
				if header != "" {
					if testDesc, bashSource, lineNumStr, ok := parseHeaderFields(header); ok {
						if rawLineNum, err := strconv.Atoi(lineNumStr); err == nil {
							adjustedLine := rawLineNum - lineOffset
							if adjustedLine > 0 && matchesTargetFile(bashSource, cleanTarget, targetBase, verifiedSources) {
								uLine := uint(adjustedLine)
								if testDesc == "" || testDesc == setupMarker {
									prof.SetupLines[uLine] = true
								} else {
									if seenPerLine[uLine] == nil {
										seenPerLine[uLine] = make(map[string]bool)
									}
									if !seenPerLine[uLine][testDesc] {
										seenPerLine[uLine][testDesc] = true
										prof.LineTests[uLine] = append(prof.LineTests[uLine], testDesc)
									}
								}
							}
						}
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
	}

	return prof, nil
}

// readTraceLinePrefix reads up to maxInspect bytes of the next line and drains any remaining
// bytes through '\n' so arbitrarily long xtrace commands (>1MB) never abort coverage parsing.
func readTraceLinePrefix(br *bufio.Reader, maxInspect int) (string, error) {
	var buf []byte
	for {
		frag, err := br.ReadSlice('\n')
		if len(buf) < maxInspect {
			remaining := maxInspect - len(buf)
			if len(frag) <= remaining {
				buf = append(buf, frag...)
			} else {
				buf = append(buf, frag[:remaining]...)
			}
		}
		if err != bufio.ErrBufferFull {
			line := strings.TrimRight(string(buf), "\r\n")
			return line, err
		}
	}
}

func parseHeaderFields(header string) (testDesc, bashSource, lineNumStr string, ok bool) {
	if strings.Contains(header, fieldSep) {
		parts := strings.SplitN(header, fieldSep, 3)
		if len(parts) != 3 {
			return "", "", "", false
		}
		return parts[0], parts[1], parts[2], true
	}
	// Legacy colon-delimited fallback: parse from the right because <test_desc> may contain colons
	lastColon := strings.LastIndexByte(header, ':')
	if lastColon <= 0 {
		return "", "", "", false
	}
	lineNumStr = header[lastColon+1:]
	beforeLine := header[:lastColon]
	srcColon := strings.LastIndexByte(beforeLine, ':')
	if srcColon < 0 {
		return "", "", "", false
	}
	return beforeLine[:srcColon], beforeLine[srcColon+1:], lineNumStr, true
}

// matchesTargetFile avoids basename-only suffix collisions (e.g. `cd lib && source ./common.sh`
// matching target `src/common.sh`, or `lib/common.sh` matching root target `common.sh`) by requiring
// either an exact clean match, a target registration emitted by the instrumented script, or a
// directory-qualified suffix match.
func matchesTargetFile(recordedSource, cleanTarget, targetBase string, verifiedSources map[string]bool) bool {
	if recordedSource == "" {
		return false
	}
	cleanRec := filepath.Clean(recordedSource)
	if cleanRec == cleanTarget {
		return true
	}
	if verifiedSources[cleanRec] {
		return true
	}
	if filepath.Base(cleanRec) != targetBase {
		return false
	}
	cleanRecSlash := filepath.ToSlash(cleanRec)
	cleanTargetSlash := filepath.ToSlash(cleanTarget)
	if strings.Contains(cleanTargetSlash, "/") {
		return strings.HasSuffix(cleanRecSlash, "/"+cleanTargetSlash)
	}
	return false
}
