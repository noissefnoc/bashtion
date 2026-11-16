package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultFileName is the legacy/override filename for incremental mutation testing results.
const DefaultFileName = ".bashtion-cache.json"

// ResolveXDGCacheFile returns the default per-project cache file path under the XDG cache directory:
// ${XDG_CACHE_HOME:-~/.cache}/bashtion/<project-hash>.json.
// Returns an empty string if neither XDG_CACHE_HOME nor a home/user cache directory can be resolved.
func ResolveXDGCacheFile(projectDir string) string {
	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			cacheBase = filepath.Join(home, ".cache")
		} else if userCache, err := os.UserCacheDir(); err == nil && userCache != "" {
			cacheBase = userCache
		}
	}
	if cacheBase == "" {
		return ""
	}

	targetDir := projectDir
	if targetDir == "" {
		if wd, err := os.Getwd(); err == nil {
			targetDir = wd
		}
	}
	if targetDir != "" {
		if abs, err := filepath.Abs(targetDir); err == nil {
			targetDir = abs
		}
		if resolved, err := filepath.EvalSymlinks(targetDir); err == nil {
			targetDir = resolved
		}
	}
	sum := sha256.Sum256([]byte(filepath.Clean(targetDir)))
	projectHash := hex.EncodeToString(sum[:8])
	return filepath.Join(cacheBase, "bashtion", projectHash+".json")
}

const (
	schemaVersion   = "2"
	maxCacheEntries = 10000
)

// Entry stores the cached evaluation outcome for a single mutant key.
type Entry struct {
	Status    string `json:"status"`
	Uncovered bool   `json:"uncovered,omitempty"`
	LastUsed  int64  `json:"last_used,omitempty"`
}

// Store manages thread-safe lookups and atomic persistence of mutant evaluation results.
type Store struct {
	mu      sync.RWMutex
	Version string           `json:"version"`
	Entries map[string]Entry `json:"entries"`
	dirty   bool
}

// NewStore creates an empty in-memory cache store.
func NewStore() *Store {
	return &Store{
		Version: schemaVersion,
		Entries: make(map[string]Entry),
	}
}

// Load reads a cache file from disk. If the file does not exist or has an incompatible version,
// a fresh empty Store is returned without error.
func Load(path string) (*Store, error) {
	if path == "" {
		return NewStore(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewStore(), nil
		}
		return NewStore(), err
	}

	var s Store
	if err := json.Unmarshal(data, &s); err != nil || s.Version != schemaVersion || s.Entries == nil {
		return NewStore(), nil
	}
	return &s, nil
}

// Get retrieves a cached Entry by key.
func (s *Store) Get(key string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.Entries[key]
	return e, ok
}

// Put records a cached Entry for key.
func (s *Store) Put(key string, entry Entry) {
	if s == nil || key == "" {
		return
	}
	if entry.LastUsed == 0 {
		entry.LastUsed = time.Now().Unix()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.Entries[key]; ok && existing.Status == entry.Status && existing.Uncovered == entry.Uncovered {
		return
	}
	s.Entries[key] = entry
	s.dirty = true
}

// Save atomically writes the cache store to path if any entries were modified,
// merging unseen entries from disk and bounding total entries to maxCacheEntries.
func (s *Store) Save(path string) error {
	if s == nil || path == "" {
		return nil
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	merged := make(map[string]Entry, len(s.Entries))
	for k, v := range s.Entries {
		merged[k] = v
	}
	s.dirty = false
	s.mu.Unlock()

	// Merge any entries written concurrently by another process on disk.
	if onDisk, err := os.ReadFile(path); err == nil {
		var diskStore Store
		if json.Unmarshal(onDisk, &diskStore) == nil && diskStore.Version == schemaVersion {
			for k, v := range diskStore.Entries {
				if _, exists := merged[k]; !exists {
					merged[k] = v
				}
			}
		}
	}

	pruneEntries(merged, maxCacheEntries)

	data, err := json.MarshalIndent(struct {
		Version string           `json:"version"`
		Entries map[string]Entry `json:"entries"`
	}{
		Version: s.Version,
		Entries: merged,
	}, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(dir, ".bashtion-cache-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func pruneEntries(entries map[string]Entry, limit int) {
	if limit <= 0 || len(entries) <= limit {
		return
	}
	type kv struct {
		key      string
		lastUsed int64
	}
	items := make([]kv, 0, len(entries))
	for k, v := range entries {
		items = append(items, kv{key: k, lastUsed: v.LastUsed})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].lastUsed != items[j].lastUsed {
			return items[i].lastUsed < items[j].lastUsed
		}
		return items[i].key < items[j].key
	})
	excess := len(entries) - limit
	for i := 0; i < excess; i++ {
		delete(entries, items[i].key)
	}
}

// ComputeKey computes a deterministic SHA-256 cache key from the enclosing function (or file) source,
// the relative byte offset and operator details of the mutant, the contents of the .bats test suite,
// and optional execution/environment metadata strings.
func ComputeKey(targetFile, funcName string, scopeSrc, testSrc []byte, category, original, replacement string, relOffset int, extra ...string) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "v=%s\ntarget=%s\nfunc=%s\ncat=%s\nrel=%d\norig_len=%d:%s\nrep_len=%d:%s\n",
		schemaVersion,
		filepath.ToSlash(filepath.Clean(targetFile)),
		funcName,
		category,
		relOffset,
		len(original), original,
		len(replacement), replacement,
	)
	_, _ = fmt.Fprintf(h, "scope_len=%d\n", len(scopeSrc))
	_, _ = h.Write(scopeSrc)
	_, _ = fmt.Fprintf(h, "\ntest_len=%d\n", len(testSrc))
	_, _ = h.Write(testSrc)
	_, _ = fmt.Fprintf(h, "\nextra_count=%d\n", len(extra))
	for i, item := range extra {
		_, _ = fmt.Fprintf(h, "extra[%d]=%d:%s\n", i, len(item), item)
	}
	return hex.EncodeToString(h.Sum(nil))
}
