package discovery_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/noissefnoc/bashtion/internal/discovery"
)

func TestResolvePairs_GlobConventionSourceAnalysisAndExplicitMappings(t *testing.T) {
	workDir := t.TempDir()

	files := map[string]string{
		"src/deploy.sh":             "#!/usr/bin/env bash\n",
		"src/sub/auth.sh":           "#!/usr/bin/env bash\n",
		"src/custom_loader.sh":      "#!/usr/bin/env bash\n",
		"src/mapped.sh":             "#!/usr/bin/env bash\n",
		"test/deploy.bats":          "#!/usr/bin/env bats\n",
		"tests/sub/auth.bats":       "#!/usr/bin/env bats\n# Note: check sentence end. custom_loader.sh in a comment must be ignored!\n",
		"specs/loader_suite.bats":   "#!/usr/bin/env bats\nsource \"${BATS_TEST_DIRNAME}/../src/custom_loader.sh\"\n",
		"custom/explicit_test.bats": "#!/usr/bin/env bats\n",
	}

	for rel, content := range files {
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pairs, err := discovery.ResolvePairs(
		workDir,
		[]string{"src/**/*.sh"},
		"",
		map[string]string{
			"src/mapped.sh": "custom/explicit_test.bats",
		},
		false,
	)
	if err != nil {
		t.Fatalf("ResolvePairs failed: %v", err)
	}

	want := []discovery.TargetPair{
		{TargetFile: filepath.FromSlash("src/custom_loader.sh"), TestFile: filepath.FromSlash("specs/loader_suite.bats")},
		{TargetFile: filepath.FromSlash("src/deploy.sh"), TestFile: filepath.FromSlash("test/deploy.bats")},
		{TargetFile: filepath.FromSlash("src/mapped.sh"), TestFile: filepath.FromSlash("custom/explicit_test.bats")},
		{TargetFile: filepath.FromSlash("src/sub/auth.sh"), TestFile: filepath.FromSlash("tests/sub/auth.bats")},
	}

	if diff := cmp.Diff(want, pairs); diff != "" {
		t.Errorf("ResolvePairs mismatch (-want +got):\n%s", diff)
	}
}

func TestResolvePairs_SameDirectoryConventionAndDirectoryTarget(t *testing.T) {
	workDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(workDir, "examples"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "examples", "helper.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "examples", "helper.bats"), []byte("#!/usr/bin/env bats\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pairs, err := discovery.ResolvePairs(workDir, []string{"examples"}, "", nil, false)
	if err != nil {
		t.Fatalf("ResolvePairs failed: %v", err)
	}
	if len(pairs) != 1 || pairs[0].TargetFile != filepath.FromSlash("examples/helper.sh") || pairs[0].TestFile != filepath.FromSlash("examples/helper.bats") {
		t.Errorf("unexpected pairs: %+v", pairs)
	}
}

func TestResolveTargets_BraceExpansionGlob(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"a.sh", "b.sh", "c.sh"} {
		p := filepath.Join(workDir, name)
		if err := os.WriteFile(p, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := discovery.ResolveTargets(workDir, []string{"{a,b}.sh"})
	if err != nil {
		t.Fatalf("ResolveTargets brace expansion failed: %v", err)
	}
	want := []string{"a.sh", "b.sh"}
	if diff := cmp.Diff(want, targets); diff != "" {
		t.Errorf("ResolveTargets brace mismatch (-want +got):\n%s", diff)
	}

	// Verify absolute glob pattern and negated character class [!c].sh
	absGlob := filepath.Join(workDir, "[!c].sh")
	absTargets, err := discovery.ResolveTargets(workDir, []string{absGlob})
	if err != nil {
		t.Fatalf("ResolveTargets absolute glob failed: %v", err)
	}
	wantAbs := []string{filepath.Join(workDir, "a.sh"), filepath.Join(workDir, "b.sh")}
	if diff := cmp.Diff(wantAbs, absTargets); diff != "" {
		t.Errorf("ResolveTargets absolute glob mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveTargets_RefusesRootGlobAndRootDir(t *testing.T) {
	workDir := t.TempDir()
	for _, pat := range []string{"/**/*.sh", "/*.sh", "/"} {
		_, err := discovery.ResolveTargets(workDir, []string{pat})
		if err == nil || !strings.Contains(err.Error(), "filesystem root") {
			t.Errorf("expected filesystem root refusal error for %q, got: %v", pat, err)
		}
	}
}

func TestResolvePairs_NoBasenameCollisionAndLiteralCommaFilename(t *testing.T) {
	workDir := t.TempDir()
	for _, dir := range []string{"src", "lib", "test"} {
		if err := os.MkdirAll(filepath.Join(workDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Two files share the basename `common.sh`.
	if err := os.WriteFile(filepath.Join(workDir, "src", "common.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "lib", "common.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Alphabetically earlier test sources lib/common.sh; src/common.sh has no test yet.
	if err := os.WriteFile(filepath.Join(workDir, "test", "a_lib_suite.bats"), []byte("source \"$BATS_TEST_DIRNAME/../lib/common.sh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// src/common.sh must NOT collide onto a_lib_suite.bats.
	if _, err := discovery.ResolvePairs(workDir, []string{"src/common.sh"}, "", nil, false); err == nil {
		t.Fatal("expected error when src/common.sh has no matching test (must not mis-pair lib/common.sh test)")
	}

	// lib/common.sh MUST pair with test/a_lib_suite.bats.
	pairs, err := discovery.ResolvePairs(workDir, []string{"lib/common.sh"}, "", nil, false)
	if err != nil {
		t.Fatalf("ResolvePairs failed for lib/common.sh: %v", err)
	}
	if len(pairs) != 1 || filepath.ToSlash(pairs[0].TestFile) != "test/a_lib_suite.bats" {
		t.Fatalf("expected test/a_lib_suite.bats, got: %+v", pairs)
	}

	// Adding src test pairs both distinctly even though filenames share basename `common.sh`.
	if err := os.WriteFile(filepath.Join(workDir, "test", "z_src_suite.bats"), []byte("source \"../src/common.sh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bothPairs, err := discovery.ResolvePairs(workDir, []string{"src/common.sh", "lib/common.sh"}, "", nil, false)
	if err != nil {
		t.Fatalf("ResolvePairs failed for both common.sh files: %v", err)
	}
	if len(bothPairs) != 2 ||
		filepath.ToSlash(bothPairs[0].TestFile) != "test/z_src_suite.bats" ||
		filepath.ToSlash(bothPairs[1].TestFile) != "test/a_lib_suite.bats" {
		t.Fatalf("unexpected pairing for common.sh files: %+v", bothPairs)
	}

	// Literal filename containing comma must remain intact in ResolveTargets.
	commaFile := "foo,bar.sh"
	if err := os.WriteFile(filepath.Join(workDir, commaFile), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	targets, err := discovery.ResolveTargets(workDir, []string{commaFile})
	if err != nil {
		t.Fatalf("ResolveTargets failed for literal comma filename: %v", err)
	}
	if diff := cmp.Diff([]string{commaFile}, targets); diff != "" {
		t.Errorf("ResolveTargets literal comma mismatch (-want +got):\n%s", diff)
	}
}

func TestResolvePairs_ExplicitTestScopeAndSingleSharedTestFallback(t *testing.T) {
	workDir := t.TempDir()
	for _, d := range []string{"src", "tests/smoke"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string]string{
		"src/a.sh":             "#!/usr/bin/env bash\n",
		"src/b.sh":             "#!/usr/bin/env bash\n",
		"tests/a.bats":         "#!/usr/bin/env bats\n",
		"tests/smoke/a.bats":   "#!/usr/bin/env bats\n",
		"tests/all_suite.bats": "#!/usr/bin/env bats\nfor f in src/*.sh; do source \"$f\"; done\n",
	} {
		if err := os.WriteFile(filepath.Join(workDir, filepath.FromSlash(rel)), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Explicit test directory `tests/smoke` must NOT pick `tests/a.bats` outside it, and must fail for `src/b.sh`.
	if _, err := discovery.ResolvePairs(workDir, []string{"src/*.sh"}, "tests/smoke", nil, false); err == nil {
		t.Fatal("expected error when src/b.sh has no matching .bats inside tests/smoke")
	}

	// 2. Explicit single test directory matching a.sh picks tests/smoke/a.bats.
	smokePairs, err := discovery.ResolvePairs(workDir, []string{"src/a.sh"}, "tests/smoke", nil, false)
	if err != nil {
		t.Fatalf("ResolvePairs with tests/smoke failed: %v", err)
	}
	if len(smokePairs) != 1 || filepath.ToSlash(smokePairs[0].TestFile) != "tests/smoke/a.bats" {
		t.Errorf("expected tests/smoke/a.bats, got: %+v", smokePairs)
	}

	// 3. Explicit single shared test file with multiple targets falls back to that test file.
	sharedPairs, err := discovery.ResolvePairs(workDir, []string{"src/*.sh"}, "tests/all_suite.bats", nil, false)
	if err != nil {
		t.Fatalf("ResolvePairs with shared test file failed: %v", err)
	}
	want := []discovery.TargetPair{
		{TargetFile: filepath.FromSlash("src/a.sh"), TestFile: filepath.FromSlash("tests/all_suite.bats")},
		{TargetFile: filepath.FromSlash("src/b.sh"), TestFile: filepath.FromSlash("tests/all_suite.bats")},
	}
	if diff := cmp.Diff(want, sharedPairs); diff != "" {
		t.Errorf("shared test fallback mismatch (-want +got):\n%s", diff)
	}
}

func TestResolveTargets_SymlinksAndNestedBraceWildcards(t *testing.T) {
	workDir := t.TempDir()
	for _, d := range []string{"real", "src/sub"} {
		if err := os.MkdirAll(filepath.Join(workDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	realScript := filepath.Join(workDir, "real", "tool.sh")
	if err := os.WriteFile(realScript, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realScript, filepath.Join(workDir, "src", "linked.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "src", "sub", "one.bash"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "src", "sub", "skip.bash"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	targets, err := discovery.ResolveTargets(workDir, []string{"src/{*.sh,sub/{one,two}.bash}"})
	if err != nil {
		t.Fatalf("ResolveTargets failed: %v", err)
	}
	want := []string{
		filepath.FromSlash("src/linked.sh"),
		filepath.FromSlash("src/sub/one.bash"),
	}
	if diff := cmp.Diff(want, targets); diff != "" {
		t.Errorf("symlink + nested brace glob mismatch (-want +got):\n%s", diff)
	}
}
