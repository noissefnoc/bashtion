package cache_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noissefnoc/bashtion/internal/cache"
)

func TestStore_LoadPutSaveAndComputeKey(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, cache.DefaultFileName)

	// Loading non-existent file returns empty store without error
	store, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("Load non-existent failed: %v", err)
	}

	k1 := cache.ComputeKey("src/foo.sh", "validate", []byte("validate() { return 0; }"), []byte("@test {}"), "RETURN_CODE", "0", "1", 20)
	k2 := cache.ComputeKey("src/foo.sh", "validate", []byte("validate() { return 0; }"), []byte("@test {}"), "RETURN_CODE", "0", "1", 20)
	if k1 != k2 {
		t.Fatalf("expected identical keys, got %s vs %s", k1, k2)
	}

	// Changing scope source or test source changes key
	k3 := cache.ComputeKey("src/foo.sh", "validate", []byte("validate() { return 1; }"), []byte("@test {}"), "RETURN_CODE", "0", "1", 20)
	if k1 == k3 {
		t.Errorf("expected different key when scope source changes")
	}

	if _, ok := store.Get(k1); ok {
		t.Errorf("expected miss before Put")
	}

	store.Put(k1, cache.Entry{Status: "KILLED", Uncovered: false})
	if err := store.Save(cachePath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Reload from disk
	reloaded, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("Load existing failed: %v", err)
	}
	got, ok := reloaded.Get(k1)
	if !ok || got.Status != "KILLED" || got.Uncovered {
		t.Errorf("unexpected reloaded entry: %+v (ok=%v)", got, ok)
	}

	// Corrupted cache file falls back gracefully to empty store
	if err := os.WriteFile(cachePath, []byte("{corrupted json"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallback, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("expected nil error on corrupted json fallback, got %v", err)
	}
	if _, ok := fallback.Get(k1); ok {
		t.Errorf("expected empty store after corrupted json")
	}

	// Old schema v1 cache file must be rejected cleanly
	if err := os.WriteFile(cachePath, []byte(`{"version":"1","entries":{"abc":{"status":"KILLED"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	v1Store, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("expected nil error on stale schema load, got %v", err)
	}
	if _, ok := v1Store.Get("abc"); ok {
		t.Errorf("expected schema v1 entries to be discarded")
	}

	// Extra metadata arguments affect ComputeKey deterministically
	kExtraA := cache.ComputeKey("src/foo.sh", "validate", []byte("validate() { return 0; }"), []byte("@test {}"), "RETURN_CODE", "0", "1", 20, "tool=1.0", "manifest=abc")
	kExtraB := cache.ComputeKey("src/foo.sh", "validate", []byte("validate() { return 0; }"), []byte("@test {}"), "RETURN_CODE", "0", "1", 20, "tool=1.1", "manifest=abc")
	if kExtraA == kExtraB || kExtraA == k1 {
		t.Errorf("expected distinct keys when extra metadata differs")
	}
}

func TestResolveXDGCacheFile(t *testing.T) {
	tmpDir := t.TempDir()
	xdgDir := filepath.Join(tmpDir, "xdg-cache")
	projA := filepath.Join(tmpDir, "proj-a")
	projB := filepath.Join(tmpDir, "proj-b")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("XDG_CACHE_HOME", xdgDir)
	pA1 := cache.ResolveXDGCacheFile(projA)
	pA2 := cache.ResolveXDGCacheFile(projA)
	pB := cache.ResolveXDGCacheFile(projB)

	if pA1 == "" || !strings.HasPrefix(pA1, filepath.Join(xdgDir, "bashtion")+string(filepath.Separator)) {
		t.Fatalf("expected XDG cache path under %s, got %q", filepath.Join(xdgDir, "bashtion"), pA1)
	}
	if !strings.HasSuffix(pA1, ".json") {
		t.Errorf("expected .json cache file, got %q", pA1)
	}
	if pA1 != pA2 {
		t.Errorf("expected deterministic cache path for same project, got %q vs %q", pA1, pA2)
	}
	if pA1 == pB {
		t.Errorf("expected distinct cache hashes for distinct projects, got %q", pA1)
	}

	// Fallback to HOME/.cache when XDG_CACHE_HOME is empty.
	fakeHome := filepath.Join(tmpDir, "home")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", fakeHome)
	fallbackPath := cache.ResolveXDGCacheFile(projA)
	wantPrefix := filepath.Join(fakeHome, ".cache", "bashtion") + string(filepath.Separator)
	if !strings.HasPrefix(fallbackPath, wantPrefix) {
		t.Errorf("expected fallback under %q when XDG_CACHE_HOME is empty, got %q", wantPrefix, fallbackPath)
	}
}

func TestStore_SaveMergesConcurrentEntriesAndPrunes(t *testing.T) {
	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, cache.DefaultFileName)

	s1 := cache.NewStore()
	s1.Put("k-from-s1", cache.Entry{Status: "KILLED", LastUsed: 100})
	if err := s1.Save(cachePath); err != nil {
		t.Fatalf("s1 Save failed: %v", err)
	}

	// Second independent store writes a different key without loading s1's key first.
	s2 := cache.NewStore()
	s2.Put("k-from-s2", cache.Entry{Status: "SURVIVED", LastUsed: 200})
	if err := s2.Save(cachePath); err != nil {
		t.Fatalf("s2 Save failed: %v", err)
	}

	merged, err := cache.Load(cachePath)
	if err != nil {
		t.Fatalf("Load merged failed: %v", err)
	}
	if _, ok := merged.Get("k-from-s1"); !ok {
		t.Errorf("expected k-from-s1 to be preserved via merge on Save")
	}
	if _, ok := merged.Get("k-from-s2"); !ok {
		t.Errorf("expected k-from-s2 to be saved")
	}

	// Verify pruning keeps at most 10000 entries and evicts oldest LastUsed.
	big := cache.NewStore()
	for i := 0; i < 10050; i++ {
		big.Put(fmt.Sprintf("key-%05d", i), cache.Entry{Status: "KILLED", LastUsed: int64(i + 1)})
	}
	bigPath := filepath.Join(tmpDir, "big.json")
	if err := big.Save(bigPath); err != nil {
		t.Fatalf("big Save failed: %v", err)
	}
	reloadedBig, err := cache.Load(bigPath)
	if err != nil {
		t.Fatalf("Load big failed: %v", err)
	}
	if len(reloadedBig.Entries) != 10000 {
		t.Errorf("expected pruned store size 10000, got %d", len(reloadedBig.Entries))
	}
	if _, ok := reloadedBig.Get("key-00000"); ok {
		t.Errorf("expected oldest key-00000 to be evicted")
	}
	if _, ok := reloadedBig.Get("key-10049"); !ok {
		t.Errorf("expected newest key-10049 to be retained")
	}
}
