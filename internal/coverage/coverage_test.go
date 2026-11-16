package coverage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noissefnoc/bashtion/internal/coverage"
)

func TestParseTrace_ExtractsLinesAndHandlesColonsInTestTitle(t *testing.T) {
	traceData := `+__BASHTION_COV__:__SETUP__:examples/deploy_helper.sh:2: source_init=1
++__BASHTION_COV__:validate_port: accepts valid port:examples/deploy_helper.sh:6: local port=8080
++__BASHTION_COV__:validate_port: accepts valid port:examples/deploy_helper.sh:7: [[ -z 8080 ]]
++__BASHTION_COV__:validate_port: accepts valid port:examples/deploy_helper.sh:11: [[ 8080 -ge 1024 ]]
++__BASHTION_COV__:validate_port: rejects empty port:examples/deploy_helper.sh:6: local port=
++__BASHTION_COV__:validate_port: rejects empty port:examples/deploy_helper.sh:7: [[ -z '' ]]
++__BASHTION_COV__:validate_port: rejects empty port:examples/deploy_helper.sh:8: return 1
++__BASHTION_COV__:other_test:other_script.sh:6: echo ignored
`
	prof, err := coverage.ParseTrace(strings.NewReader(traceData), "examples/deploy_helper.sh", 1)
	if err != nil {
		t.Fatalf("ParseTrace failed: %v", err)
	}

	if !prof.HasAnyCoverage() {
		t.Fatalf("expected profile to have coverage")
	}

	// Line 2 - offset 1 = Line 1 (covered in setup)
	if !prof.IsCovered(1) {
		t.Errorf("expected line 1 to be covered via setup")
	}
	if got := prof.TestsForLine(1); got != nil {
		t.Errorf("expected nil (all tests) for setup line 1, got %v", got)
	}

	// Line 6 - offset 1 = Line 5 (covered by both validate_port tests)
	if !prof.IsCovered(5) {
		t.Errorf("expected line 5 to be covered")
	}
	testsLine5 := prof.TestsForLine(5)
	if len(testsLine5) != 2 {
		t.Fatalf("expected 2 tests for line 5, got %v", testsLine5)
	}

	// Line 11 - offset 1 = Line 10 (covered only by "validate_port: accepts valid port")
	if !prof.IsCovered(10) {
		t.Errorf("expected line 10 to be covered")
	}
	regex10 := prof.FilterRegexForLine(10, 3)
	wantRegex := `^(validate_port: accepts valid port)$`
	if regex10 != wantRegex {
		t.Errorf("FilterRegexForLine(10) = %q, want %q", regex10, wantRegex)
	}

	// Uncovered line 30
	if prof.IsCovered(30) {
		t.Errorf("expected line 30 to be uncovered")
	}
	if r := prof.FilterRegexForLine(30, 3); r != "" {
		t.Errorf("expected empty filter regex for uncovered line, got %q", r)
	}

	// Weird path containing colons (e.g. "src/a:1.sh") using the current __BASHTION_SEP__ separator
	colonPathTrace := "+__BASHTION_COV__:test: colon path__BASHTION_SEP__src/a:1.sh__BASHTION_SEP__5__BASHTION_END__ echo ok\n"
	colonProf, err := coverage.ParseTrace(strings.NewReader(colonPathTrace), "src/a:1.sh", 1)
	if err != nil {
		t.Fatalf("ParseTrace colon path failed: %v", err)
	}
	if !colonProf.IsCovered(4) {
		t.Errorf("expected line 4 of src/a:1.sh to be covered")
	}
}

func TestInstrumentScript_AndRealBashExecution(t *testing.T) {
	tmpDir := t.TempDir()
	traceFile := filepath.Join(tmpDir, "trace.log")
	scriptPath := filepath.Join(tmpDir, "sample.sh")

	origScript := `#!/usr/bin/env bash
greet() {
    if [[ "$1" == "world" ]]; then
        echo "hello world"
        return 0
    fi
    echo "unknown"
    return 1
}
`
	instrumented, offset := coverage.InstrumentScript([]byte(origScript), traceFile)
	if offset != 1 {
		t.Fatalf("expected lineOffset 1, got %d", offset)
	}
	if err := os.WriteFile(scriptPath, instrumented, 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate bats-core environment with BATS_TEST_DESCRIPTION and verify stdout is not polluted by xtrace
	cmd := exec.Command("bash", "-c", `export BATS_TEST_DESCRIPTION="greet: world test"; source "$1"; out=$(greet "world"); [[ "$out" == "hello world" ]]`, "--", scriptPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash execution failed: %v\noutput: %s", err, string(out))
	}

	traceBytes, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("reading trace file: %v", err)
	}

	prof, err := coverage.ParseTrace(strings.NewReader(string(traceBytes)), scriptPath, offset)
	if err != nil {
		t.Fatalf("ParseTrace failed: %v", err)
	}

	// Lines 3, 4, 5 in origScript should be covered, while lines 7, 8 should be uncovered
	for _, coveredLine := range []uint{3, 4, 5} {
		if !prof.IsCovered(coveredLine) {
			t.Errorf("expected line %d to be covered, trace:\n%s", coveredLine, string(traceBytes))
		}
	}
	for _, uncoveredLine := range []uint{7, 8} {
		if prof.IsCovered(uncoveredLine) {
			t.Errorf("expected line %d to be uncovered, trace:\n%s", uncoveredLine, string(traceBytes))
		}
	}
}

func TestInstrumentScript_SpecialCharsInTracePathDoNotExpand(t *testing.T) {
	tmpDir := t.TempDir()
	// Include $, backticks, quotes, and spaces in trace filename to ensure no eval/shell injection occurs
	pwnFile := filepath.Join(tmpDir, "pwned")
	weirdTrace := filepath.Join(tmpDir, "trace '$(touch pwned)' `touch pwned` $HOME.log")
	scriptPath := filepath.Join(tmpDir, "sample.sh")

	origScript := "#!/usr/bin/env bash\necho ok\n"
	instrumented, offset := coverage.InstrumentScript([]byte(origScript), weirdTrace)
	if err := os.WriteFile(scriptPath, instrumented, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "-c", `source "$1"`, "--", scriptPath)
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash execution with special trace path failed: %v\noutput: %s", err, string(out))
	}
	if _, err := os.Stat(pwnFile); err == nil {
		t.Fatal("command injection occurred via traceFilePath")
	}

	traceBytes, err := os.ReadFile(weirdTrace)
	if err != nil {
		t.Fatalf("expected weird trace file to be created and written: %v", err)
	}
	prof, err := coverage.ParseTrace(strings.NewReader(string(traceBytes)), scriptPath, offset)
	if err != nil || !prof.IsCovered(2) {
		t.Errorf("expected line 2 to be covered, err=%v trace=%s", err, string(traceBytes))
	}
}

func TestDisablesXtrace(t *testing.T) {
	if coverage.DisablesXtrace([]byte("#!/usr/bin/env bash\n# set +x in comment\necho hello\n")) {
		t.Error("expected false when set +x is only in a comment")
	}
	heredocScript := "#!/usr/bin/env bash\ncat <<'EOF'\nset +x\nset +o xtrace\nEOF\necho ok\n"
	if coverage.DisablesXtrace([]byte(heredocScript)) {
		t.Error("expected false when set +x only appears inside a heredoc")
	}
	for _, script := range []string{
		"#!/usr/bin/env bash\nset +x\necho secret\n",
		"#!/usr/bin/env bash\nset +ex; echo secret\n",
		"#!/usr/bin/env bash\nset +o xtrace\n",
		"#!/usr/bin/env bash\nshopt -uo xtrace\n",
		"#!/usr/bin/env bash\nshopt -u -o xtrace\n",
		"#!/usr/bin/env bash\necho \"# not a comment\"; set +x\n",
		"#!/usr/bin/env bash\nx=${1#prefix}; set +x\n",
	} {
		if !coverage.DisablesXtrace([]byte(script)) {
			t.Errorf("expected DisablesXtrace=true for:\n%s", script)
		}
	}
}

func TestInstrumentScript_PreservesLeadingHeaderComments(t *testing.T) {
	script := `#!/usr/bin/env bash
# Summary: List hooks
# Usage: pyenv hooks <command>
#
# Detailed help text here.

set -euo pipefail
echo "running hooks"
`
	instrumented, offset := coverage.InstrumentScript([]byte(script), "/tmp/trace.log")
	if offset != 1 {
		t.Fatalf("expected offset 1, got %d", offset)
	}

	lines := strings.Split(string(instrumented), "\n")
	// Expected layout:
	// Line 0: #!/usr/bin/env bash
	// Line 1: # Summary: List hooks
	// Line 2: # Usage: pyenv hooks <command>
	// Line 3: #
	// Line 4: # Detailed help text here.
	// Line 5: (empty)
	// Line 6: preamble
	// Line 7: set -euo pipefail
	if !strings.HasPrefix(lines[0], "#!/usr/bin/env bash") {
		t.Errorf("line 0 should be shebang, got %q", lines[0])
	}
	if lines[1] != "# Summary: List hooks" {
		t.Errorf("line 1 should be summary comment, got %q", lines[1])
	}
	if lines[2] != "# Usage: pyenv hooks <command>" {
		t.Errorf("line 2 should be usage comment, got %q", lines[2])
	}
	if !strings.Contains(lines[6], "__bashtion_cov_file") {
		t.Errorf("line 6 should be preamble, got %q", lines[6])
	}
	if lines[7] != "set -euo pipefail" {
		t.Errorf("line 7 should be first code line, got %q", lines[7])
	}
}

func TestParseTrace_OversizedXtraceLineDoesNotAbort(t *testing.T) {
	// Generate a >1.5MB xtrace line sandwiched between two normal trace entries.
	hugeArg := strings.Repeat("x", 1500*1024)
	trace := "+__BASHTION_COV__:t1__BASHTION_SEP__/app/script.sh__BASHTION_SEP__3__BASHTION_END__ echo before\n" +
		"+__BASHTION_COV__:t1__BASHTION_SEP__/app/script.sh__BASHTION_SEP__5__BASHTION_END__ echo " + hugeArg + "\n" +
		"+__BASHTION_COV__:t1__BASHTION_SEP__/app/script.sh__BASHTION_SEP__8__BASHTION_END__ echo after\n"

	prof, err := coverage.ParseTrace(strings.NewReader(trace), "/app/script.sh", 0)
	if err != nil {
		t.Fatalf("ParseTrace failed on >1MB xtrace line: %v", err)
	}
	for _, wantLine := range []uint{3, 5, 8} {
		if !prof.IsCovered(wantLine) {
			t.Errorf("expected line %d to be recorded despite >1MB line", wantLine)
		}
	}
}

func TestParseTrace_AvoidsSameBasenameSuffixCollisions(t *testing.T) {
	// Case 1: target is "src/common.sh"; sibling "lib/common.sh", "./common.sh" (from cd lib && source ./common.sh),
	// and "other_src/common.sh" must not match, and targetMarker inside an xtrace line body must not forge registration.
	trace := strings.Join([]string{
		"+__BASHTION_COV__:t1__BASHTION_SEP__lib/common.sh__BASHTION_SEP__2__BASHTION_END__ echo lib",
		"+__BASHTION_COV__:t1__BASHTION_SEP__./common.sh__BASHTION_SEP__3__BASHTION_END__ printf '+__BASHTION_TARGET__:./common.sh__BASHTION_END__'",
		"+__BASHTION_COV__:t1__BASHTION_SEP__other_src/common.sh__BASHTION_SEP__4__BASHTION_END__ echo other_src",
		"+__BASHTION_COV__:t1__BASHTION_SEP__/tmp/worker-0/src/common.sh__BASHTION_SEP__6__BASHTION_END__ echo real_target",
	}, "\n") + "\n"

	prof, err := coverage.ParseTrace(strings.NewReader(trace), "src/common.sh", 0)
	if err != nil {
		t.Fatalf("ParseTrace failed: %v", err)
	}
	for _, unexpected := range []uint{2, 3, 4} {
		if prof.IsCovered(unexpected) {
			t.Errorf("line %d should not be marked covered from colliding filename", unexpected)
		}
	}
	if !prof.IsCovered(6) {
		t.Errorf("expected line 6 from /tmp/worker-0/src/common.sh to be covered")
	}

	// Case 2: root-level target "common.sh"; uninstrumented "lib/common.sh" must not match,
	// while an instrumented "./common.sh" registration does match.
	rootTrace := strings.Join([]string{
		"+__BASHTION_COV__:t1__BASHTION_SEP__lib/common.sh__BASHTION_SEP__2__BASHTION_END__ echo lib",
		"+__BASHTION_TARGET__:./common.sh__BASHTION_END__",
		"+__BASHTION_COV__:t1__BASHTION_SEP__./common.sh__BASHTION_SEP__5__BASHTION_END__ echo root_target",
	}, "\n") + "\n"

	rootProf, err := coverage.ParseTrace(strings.NewReader(rootTrace), "common.sh", 0)
	if err != nil {
		t.Fatalf("ParseTrace root failed: %v", err)
	}
	if rootProf.IsCovered(2) {
		t.Errorf("line 2 from lib/common.sh should not be marked covered for root common.sh")
	}
	if !rootProf.IsCovered(5) {
		t.Errorf("expected line 5 from registered ./common.sh to be covered")
	}
}

func TestInstrumentScript_SameBasenameInSiblingDir(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	libDir := filepath.Join(tmpDir, "lib")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Line 2: executed on source
	// Lines 4-6: inside uncalled src_unused, must remain uncovered even when lib/common.sh executes lines 4-6!
	srcScript := "#!/usr/bin/env bash\nsrc_loaded=1\nsrc_unused() {\n  local x=1\n  local y=2\n  echo \"$x $y\"\n}\n"
	libScript := "#!/usr/bin/env bash\n# pad\n# pad\nlib_a=1\nlib_b=2\nlib_c=3\n"

	traceA := filepath.Join(tmpDir, "traceA.log")
	instA, offsetA := coverage.InstrumentScript([]byte(srcScript), traceA)
	if err := os.WriteFile(filepath.Join(srcDir, "common.sh"), instA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "common.sh"), []byte(libScript), 0o755); err != nil {
		t.Fatal(err)
	}

	cmdA := exec.Command("bash", "-c", `source src/common.sh; cd lib && source ./common.sh`, "--")
	cmdA.Dir = tmpDir
	if out, err := cmdA.CombinedOutput(); err != nil {
		t.Fatalf("bash execution A failed: %v\n%s", err, string(out))
	}
	tbA, err := os.ReadFile(traceA)
	if err != nil {
		t.Fatal(err)
	}
	profA, err := coverage.ParseTrace(strings.NewReader(string(tbA)), "src/common.sh", offsetA)
	if err != nil {
		t.Fatal(err)
	}
	if !profA.IsCovered(2) {
		t.Errorf("expected line 2 of src/common.sh to be covered, trace:\n%s", string(tbA))
	}
	for _, uncovered := range []uint{4, 5, 6} {
		if profA.IsCovered(uncovered) {
			t.Errorf("expected line %d of src/common.sh to remain uncovered despite lib/common.sh, trace:\n%s", uncovered, string(tbA))
		}
	}

	// Case B: `cd src && source ./common.sh` must still attribute coverage to target "src/common.sh"
	traceB := filepath.Join(tmpDir, "traceB.log")
	instB, offsetB := coverage.InstrumentScript([]byte(srcScript), traceB)
	if err := os.WriteFile(filepath.Join(srcDir, "common.sh"), instB, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdB := exec.Command("bash", "-c", `cd src && source ./common.sh && src_unused`, "--")
	cmdB.Dir = tmpDir
	if out, err := cmdB.CombinedOutput(); err != nil {
		t.Fatalf("bash execution B failed: %v\n%s", err, string(out))
	}
	tbB, err := os.ReadFile(traceB)
	if err != nil {
		t.Fatal(err)
	}
	profB, err := coverage.ParseTrace(strings.NewReader(string(tbB)), "src/common.sh", offsetB)
	if err != nil {
		t.Fatal(err)
	}
	for _, covered := range []uint{2, 4, 5, 6} {
		if !profB.IsCovered(covered) {
			t.Errorf("expected line %d of src/common.sh to be covered via cd src && source ./common.sh, trace:\n%s", covered, string(tbB))
		}
	}
}
