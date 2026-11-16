package gitdiff_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/noissefnoc/bashtion/internal/gitdiff"
)

func TestParseUnifiedDiffLines(t *testing.T) {
	// Place a pure deletion hunk BEFORE an addition hunk so `continue` vs `break` on count==0 is tested!
	rawDiff := `diff --git a/examples/deploy_helper.sh b/examples/deploy_helper.sh
index 1234567..89abcdef 100755
--- a/examples/deploy_helper.sh
+++ b/examples/deploy_helper.sh
@@ -6 +6 @@ validate_port() {
-    if [[ -z "$port" ]]; then
+    if [[ -n "$port" ]]; then
@@ -12,2 +12,0 @@ validate_port() {
-    echo "deleted1"
-    echo "deleted2"
@@ -18,0 +19,3 @@ check_env_ready() {
+    if [[ "$retries" -gt 10 ]]; then
+        return 1
+    fi
`
	lines, err := gitdiff.ParseUnifiedDiffLines(rawDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiffLines failed: %v", err)
	}

	expected := map[uint]bool{
		6:  true,
		19: true,
		20: true,
		21: true,
	}
	if !reflect.DeepEqual(lines, expected) {
		t.Errorf("want %v, got %v", expected, lines)
	}
}

func TestParseUnifiedDiffLines_MalformedAndErrors(t *testing.T) {
	// Hunks missing " +" or " @@" should be ignored without panicking
	ignoredDiff := "@@ -1,2 @@\n@@ -1,2 +3,4\n"
	lines, err := gitdiff.ParseUnifiedDiffLines(ignoredDiff)
	if err != nil {
		t.Fatalf("unexpected error for ignored headers: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected 0 lines, got %v", lines)
	}

	// Invalid start number and invalid count number should return errors
	for _, bad := range []string{
		"@@ -1 +abc @@\n",
		"@@ -1 +1,xyz @@\n",
	} {
		if _, err := gitdiff.ParseUnifiedDiffLines(bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func TestChangedLines_InTempGitRepo(t *testing.T) {
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}

	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")

	file := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(file, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "script.sh")
	runGit("commit", "-m", "initial")

	if err := os.WriteFile(file, []byte("line1\nmodified_line2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := gitdiff.ChangedLines(context.Background(), dir, "HEAD", "script.sh")
	if err != nil {
		t.Fatalf("ChangedLines failed: %v", err)
	}
	if !reflect.DeepEqual(changed, map[uint]bool{2: true}) {
		t.Errorf("want line 2 changed, got %v", changed)
	}

	// Invalid git ref should return an error
	if _, err := gitdiff.ChangedLines(context.Background(), dir, "nonexistent-ref-12345", "script.sh"); err == nil {
		t.Error("expected error for invalid git ref, got nil")
	}

	// Ref starting with '-' (option injection attempt) must be rejected before running git
	for _, flagRef := range []string{"--output=" + filepath.Join(dir, "pwned"), "-q", " --ext-diff"} {
		if _, err := gitdiff.ChangedLines(context.Background(), dir, flagRef, "script.sh"); err == nil {
			t.Errorf("expected error for flag-like git ref %q, got nil", flagRef)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("git diff option injection created a file on disk")
	}

	// Untracked file should return an informative error
	untrackedFile := filepath.Join(dir, "untracked.sh")
	if err := os.WriteFile(untrackedFile, []byte("echo untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitdiff.ChangedLines(context.Background(), dir, "HEAD", "untracked.sh"); err == nil {
		t.Error("expected error for untracked file, got nil")
	}

	// color.diff=always in repo git config should not disrupt ChangedLines
	runGit("config", "color.diff", "always")
	changedWithColor, err := gitdiff.ChangedLines(context.Background(), dir, "HEAD", "script.sh")
	if err != nil {
		t.Fatalf("ChangedLines with color.diff=always failed: %v", err)
	}
	if !reflect.DeepEqual(changedWithColor, map[uint]bool{2: true}) {
		t.Errorf("want line 2 changed with color.diff=always, got %v", changedWithColor)
	}
}

func TestParseUnifiedDiffLines_WithANSIColorCodes(t *testing.T) {
	coloredDiff := "\x1b[36m@@ -10,2 +12,4 @@\x1b[m validate_port()\n" +
		"\x1b[31m-echo old\x1b[m\n" +
		"\x1b[32m+echo new\x1b[m\n"
	lines, err := gitdiff.ParseUnifiedDiffLines(coloredDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiffLines failed with ANSI color: %v", err)
	}
	expected := map[uint]bool{12: true, 13: true, 14: true, 15: true}
	if !reflect.DeepEqual(lines, expected) {
		t.Errorf("want %v, got %v", expected, lines)
	}
}

func TestParseUnifiedDiffLines_OversizedLineDoesNotFail(t *testing.T) {
	longLine := strings.Repeat("A", 128*1024)
	rawDiff := "@@ -1 +1 @@\n-" + longLine + "\n+" + longLine + "\n@@ -10,0 +11,2 @@\n+echo a\n+echo b\n"
	lines, err := gitdiff.ParseUnifiedDiffLines(rawDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiffLines failed on >64KiB line: %v", err)
	}
	expected := map[uint]bool{1: true, 11: true, 12: true}
	if !reflect.DeepEqual(lines, expected) {
		t.Errorf("want %v, got %v", expected, lines)
	}
}
