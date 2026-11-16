package discovery

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TargetPair represents a resolved Bash target script and its corresponding bats test suite.
type TargetPair struct {
	TargetFile string
	TestFile   string
}

// ResolveTargets expands one or more file paths, directory paths, or glob patterns (including **)
// into a deduplicated, sorted list of target Bash script paths.
func ResolveTargets(workDir string, rawPatterns []string) ([]string, error) {
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workDir = wd
	}

	var patterns []string
	for _, raw := range rawPatterns {
		// If the raw string names an existing file or directory literally (e.g. contains commas), keep it intact.
		if raw != "" {
			if _, err := os.Stat(absOrJoin(workDir, raw)); err == nil {
				patterns = append(patterns, raw)
				continue
			}
		}
		for _, part := range splitCommaOutsideBraces(raw) {
			p := strings.TrimSpace(part)
			if p != "" {
				patterns = append(patterns, p)
			}
		}
	}
	if len(patterns) == 0 {
		return nil, fmt.Errorf("--target <script.sh> is required")
	}

	seen := make(map[string]bool)
	var result []string
	addTarget := func(p string) {
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			result = append(result, p)
		}
	}

	for _, pat := range patterns {
		if hasGlobMeta(pat) {
			matches, err := expandGlob(workDir, pat, isShellScript)
			if err != nil {
				return nil, err
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("no target scripts matched pattern %q", pat)
			}
			for _, m := range matches {
				addTarget(m)
			}
			continue
		}

		absPat := absOrJoin(workDir, pat)
		info, err := os.Stat(absPat)
		if err != nil {
			// Return original path so caller produces the expected file read error when single file is missing
			if len(patterns) == 1 {
				return []string{pat}, nil
			}
			return nil, fmt.Errorf("target %q: %w", pat, err)
		}

		if info.IsDir() {
			dirMatches, err := walkScriptsInDir(workDir, absPat, pat, isShellScript)
			if err != nil {
				return nil, err
			}
			if len(dirMatches) == 0 {
				return nil, fmt.Errorf("no .sh/.bash scripts found in directory %q", pat)
			}
			for _, m := range dirMatches {
				addTarget(m)
			}
			continue
		}

		addTarget(pat)
	}

	return result, nil
}

// ResolvePairs resolves target patterns and pairs each target script with its .bats test file using:
//  1. Explicit 1-to-1 --test flag (when single target and --test is a concrete file)
//  2. Explicit mappings from .bashtion.yaml (`mappings:`)
//  3. Static analysis of `source` / `.` / `load` directives inside .bats files
//  4. Filename convention fallback (<dir>/<stem>.bats, test/<rel_dir>/<stem>.bats, test/<stem>.bats, etc.)
func ResolvePairs(workDir string, targetPatterns []string, testPattern string, explicitMappings map[string]string, listOnly bool) ([]TargetPair, error) {
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workDir = wd
	}

	targets, err := ResolveTargets(workDir, targetPatterns)
	if err != nil {
		return nil, err
	}

	if listOnly {
		pairs := make([]TargetPair, len(targets))
		for i, t := range targets {
			pairs[i] = TargetPair{TargetFile: t}
		}
		return pairs, nil
	}

	// Fast path: single target + explicit single test file (not a directory or glob)
	if len(targets) == 1 && testPattern != "" && !hasGlobMeta(testPattern) {
		absTest := absOrJoin(workDir, testPattern)
		if info, err := os.Stat(absTest); err != nil || !info.IsDir() {
			return []TargetPair{{TargetFile: targets[0], TestFile: testPattern}}, nil
		}
	}

	// Collect candidate .bats files in the workspace (or within testPattern if specified)
	candidateBats, err := collectCandidateBats(workDir, testPattern)
	if err != nil {
		return nil, err
	}

	// Pre-index which target scripts each .bats file sources or loads
	batsSourceRefs := indexBatsSourceRefs(workDir, candidateBats)
	restrictToCandidates := testPattern != ""
	explicitSingleTest := ""
	if restrictToCandidates && !hasGlobMeta(testPattern) && len(candidateBats) == 1 {
		if info, statErr := os.Stat(absOrJoin(workDir, testPattern)); statErr == nil && !info.IsDir() {
			explicitSingleTest = candidateBats[0]
		}
	}

	pairs := make([]TargetPair, 0, len(targets))
	for _, target := range targets {
		matchedTest := resolveTestForTarget(workDir, target, candidateBats, explicitMappings, batsSourceRefs, restrictToCandidates)
		if matchedTest == "" && explicitSingleTest != "" {
			matchedTest = explicitSingleTest
		}
		if matchedTest == "" {
			if len(targets) == 1 && testPattern == "" {
				return nil, fmt.Errorf("--test <test.bats> is required unless --list is set (could not auto-discover a .bats file for %s)", target)
			}
			return nil, fmt.Errorf("could not find matching .bats test file for target %q", target)
		}
		pairs = append(pairs, TargetPair{
			TargetFile: target,
			TestFile:   matchedTest,
		})
	}

	return pairs, nil
}

func resolveTestForTarget(
	workDir, target string,
	candidateBats []string,
	explicitMappings map[string]string,
	batsSourceRefs map[string][]string,
	restrictToCandidates bool,
) string {
	cleanTarget := filepath.ToSlash(filepath.Clean(relOrClean(workDir, target)))
	targetBase := filepath.Base(cleanTarget)
	ext := filepath.Ext(targetBase)
	stem := strings.TrimSuffix(targetBase, ext)
	targetDir := path.Dir(cleanTarget)

	// 1. Check explicit mappings from config (sorted for deterministic iteration)
	keys := make([]string, 0, len(explicitMappings))
	for k := range explicitMappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := explicitMappings[k]
		cleanKey := filepath.ToSlash(filepath.Clean(relOrClean(workDir, k)))
		if cleanKey == cleanTarget || k == target {
			return v
		}
	}

	// 2. Check convention candidates in priority order
	var convCandidates []string
	if targetDir == "." {
		convCandidates = []string{
			stem + ".bats",
			path.Join("test", stem+".bats"),
			path.Join("tests", stem+".bats"),
		}
	} else {
		// Also strip leading src/ or lib/ or scripts/ when mapping to test/ or tests/
		trimmedSubdir := trimSourceRootPrefix(targetDir)
		convCandidates = []string{
			path.Join(targetDir, stem+".bats"),
			path.Join("test", targetDir, stem+".bats"),
			path.Join("tests", targetDir, stem+".bats"),
			path.Join("test", trimmedSubdir, stem+".bats"),
			path.Join("tests", trimmedSubdir, stem+".bats"),
			path.Join("test", stem+".bats"),
			path.Join("tests", stem+".bats"),
		}
	}

	candidateByRel := make(map[string]string, len(candidateBats))
	for _, b := range candidateBats {
		relB := filepath.ToSlash(filepath.Clean(relOrClean(workDir, b)))
		candidateByRel[relB] = b
	}

	for _, conv := range convCandidates {
		if matched, ok := candidateByRel[filepath.ToSlash(path.Clean(conv))]; ok {
			return matched
		}
		if restrictToCandidates {
			continue
		}
		absConv := absOrJoin(workDir, filepath.FromSlash(conv))
		if info, err := os.Stat(absConv); err == nil && !info.IsDir() {
			if filepath.IsAbs(target) {
				return absConv
			}
			return filepath.FromSlash(conv)
		}
	}

	// 3. Check static analysis of `source` / `.` / `load` in candidate .bats files.
	// Pass 3a prefers full/path-qualified matches (e.g. src/common.sh != lib/common.sh).
	for _, b := range candidateBats {
		for _, ref := range batsSourceRefs[b] {
			if refMatchesTargetExact(ref, cleanTarget) {
				return b
			}
		}
	}
	// Pass 3b falls back to basename/stem matches only when the reference has no static directory prefix.
	for _, b := range candidateBats {
		for _, ref := range batsSourceRefs[b] {
			if refMatchesTargetBase(ref, targetBase, stem) {
				return b
			}
		}
	}

	// 4. When scoped to explicit --test candidates, match a unique <stem>.bats filename if unambiguous.
	if restrictToCandidates {
		var stemMatches []string
		for _, b := range candidateBats {
			if strings.TrimSuffix(filepath.Base(b), ".bats") == stem {
				stemMatches = append(stemMatches, b)
			}
		}
		if len(stemMatches) == 1 {
			return stemMatches[0]
		}
	}

	return ""
}

var sourceLoadRegex = regexp.MustCompile(`(?:^|[\s;(])(?:source|\.|load)\s+["']?([^"'\s;)]+)["']?`)

func indexBatsSourceRefs(workDir string, batsFiles []string) map[string][]string {
	index := make(map[string][]string, len(batsFiles))
	for _, bf := range batsFiles {
		absBF := absOrJoin(workDir, bf)
		data, err := os.ReadFile(absBF)
		if err != nil {
			continue
		}
		batsDir := filepath.ToSlash(filepath.Dir(relOrClean(workDir, bf)))
		for _, rawLine := range strings.Split(string(data), "\n") {
			line := stripShellComment(rawLine)
			matches := sourceLoadRegex.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				rawRef := m[1]
				// Expand common Bats directory variables to batsDir
				rawRef = strings.ReplaceAll(rawRef, "${BATS_TEST_DIRNAME}", batsDir)
				rawRef = strings.ReplaceAll(rawRef, "$BATS_TEST_DIRNAME", batsDir)
				cleanRef := filepath.ToSlash(path.Clean(rawRef))
				index[bf] = append(index[bf], cleanRef)
				// Also record path resolved relative to the .bats file directory for relative refs like ../src/common.sh.
				if batsDir != "." && !strings.HasPrefix(rawRef, "/") && !strings.Contains(rawRef, "$") && strings.Contains(rawRef, "/") {
					relFromBatsDir := filepath.ToSlash(path.Clean(path.Join(batsDir, rawRef)))
					if relFromBatsDir != cleanRef {
						index[bf] = append(index[bf], relFromBatsDir)
					}
				}
			}
		}
	}
	return index
}

func stripShellComment(line string) string {
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
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' || line[i-1] == ';' {
				return line[:i]
			}
		}
	}
	return line
}

func refMatchesTargetExact(ref, cleanTarget string) bool {
	cleanTargetNoExt := strings.TrimSuffix(cleanTarget, path.Ext(cleanTarget))
	if ref == cleanTarget || ref == cleanTargetNoExt {
		return true
	}
	if strings.Contains(cleanTarget, "/") || strings.Contains(ref, "$") || strings.HasPrefix(ref, "/") {
		if strings.HasSuffix(ref, "/"+cleanTarget) || strings.HasSuffix(ref, "/"+cleanTargetNoExt) {
			return true
		}
	}
	return false
}

func refMatchesTargetBase(ref, targetBase, stem string) bool {
	if strings.Contains(ref, "/") && !strings.Contains(ref, "$") {
		return false
	}
	refBase := path.Base(ref)
	return refBase == targetBase || refBase == stem
}

func collectCandidateBats(workDir, testPattern string) ([]string, error) {
	if testPattern != "" {
		if hasGlobMeta(testPattern) {
			return expandGlob(workDir, testPattern, isBatsFile)
		}
		absTest := absOrJoin(workDir, testPattern)
		info, err := os.Stat(absTest)
		if err != nil {
			return nil, fmt.Errorf("test path %q: %w", testPattern, err)
		}
		if info.IsDir() {
			return walkScriptsInDir(workDir, absTest, testPattern, isBatsFile)
		}
		return []string{testPattern}, nil
	}
	return walkScriptsInDir(workDir, workDir, ".", isBatsFile)
}

func trimSourceRootPrefix(dir string) string {
	for _, prefix := range []string{"src/", "lib/", "scripts/", "bin/"} {
		if strings.HasPrefix(dir, prefix) {
			return strings.TrimPrefix(dir, prefix)
		}
		if dir == strings.TrimSuffix(prefix, "/") {
			return "."
		}
	}
	return dir
}

const maxDiscoveryWalkEntries = 100000

func shouldSkipDiscoveryDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

func isFilesystemRoot(p string) bool {
	clean := filepath.Clean(p)
	vol := filepath.VolumeName(clean) + string(filepath.Separator)
	return clean == string(filepath.Separator) || clean == vol
}

func walkScriptsInDir(workDir, absRoot, origPrefix string, pred func(string) bool) ([]string, error) {
	if isFilesystemRoot(absRoot) {
		return nil, fmt.Errorf("refusing to scan directory %q from filesystem root", origPrefix)
	}
	var results []string
	useAbs := filepath.IsAbs(origPrefix)
	visited := 0
	err := filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited++
		if visited > maxDiscoveryWalkEntries {
			return fmt.Errorf("directory scan for %q exceeded %d filesystem entries; narrow target path", origPrefix, maxDiscoveryWalkEntries)
		}
		if d.IsDir() {
			name := d.Name()
			if p != absRoot && name != "." && shouldSkipDiscoveryDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isRegularOrSymlinkFile(p, d) || !pred(p) {
			return nil
		}
		if useAbs {
			results = append(results, filepath.Clean(p))
		} else if rel, err := filepath.Rel(workDir, p); err == nil {
			results = append(results, rel)
		} else {
			results = append(results, p)
		}
		return nil
	})
	sort.Strings(results)
	return results, err
}

func expandGlob(workDir, pattern string, pred func(string) bool) ([]string, error) {
	isAbs := filepath.IsAbs(pattern)
	cleanPat := filepath.ToSlash(filepath.Clean(pattern))
	re, err := globToRegex(cleanPat)
	if err != nil {
		return nil, err
	}

	walkRoot := workDir
	basePrefix := staticGlobBaseDir(cleanPat)
	if isAbs {
		walkRoot = filepath.FromSlash(basePrefix)
		if isFilesystemRoot(walkRoot) {
			return nil, fmt.Errorf("refusing to expand glob %q from filesystem root", pattern)
		}
	} else if basePrefix != "." {
		walkRoot = absOrJoin(workDir, filepath.FromSlash(basePrefix))
	}
	if isFilesystemRoot(walkRoot) {
		return nil, fmt.Errorf("refusing to expand glob %q from filesystem root", pattern)
	}
	if _, statErr := os.Stat(walkRoot); statErr != nil {
		return nil, nil
	}

	var matches []string
	visited := 0
	err = filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		visited++
		if visited > maxDiscoveryWalkEntries {
			return fmt.Errorf("glob expansion for %q exceeded %d filesystem entries; narrow the pattern", pattern, maxDiscoveryWalkEntries)
		}
		if d.IsDir() {
			name := d.Name()
			if p != walkRoot && name != "." && shouldSkipDiscoveryDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isRegularOrSymlinkFile(p, d) || !pred(p) {
			return nil
		}
		if isAbs {
			cleanAbs := filepath.Clean(p)
			if re.MatchString(filepath.ToSlash(cleanAbs)) {
				matches = append(matches, cleanAbs)
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, p)
		if err != nil {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if re.MatchString(slashRel) {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	return matches, err
}

func staticGlobBaseDir(slashPat string) string {
	idx := strings.IndexAny(slashPat, "*?[{")
	if idx < 0 {
		return path.Dir(slashPat)
	}
	prefix := slashPat[:idx]
	lastSlash := strings.LastIndexByte(prefix, '/')
	if lastSlash < 0 {
		return "."
	}
	if lastSlash == 0 {
		return "/"
	}
	return prefix[:lastSlash]
}

// CompileGlob compiles a slash-separated glob pattern (supporting **, *, ?, [...], and nested {a,b})
// into an anchored regular expression.
func CompileGlob(glob string) (*regexp.Regexp, error) {
	return globToRegex(glob)
}

func isRegularOrSymlinkFile(p string, d fs.DirEntry) bool {
	if d.Type().IsRegular() {
		return true
	}
	if d.Type()&fs.ModeSymlink != 0 {
		info, err := os.Stat(p)
		return err == nil && info.Mode().IsRegular()
	}
	return false
}

func globToRegex(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	writeGlobFragment(&b, glob)
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func writeGlobFragment(b *strings.Builder, glob string) {
	n := len(glob)
	for i := 0; i < n; i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < n && glob[i+1] == '*' {
				i++
				if i+1 < n && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			b.WriteByte('[')
			if i+1 < n && glob[i+1] == '!' {
				b.WriteByte('^')
				i++
			}
		case '{':
			end := findMatchingBrace(glob, i)
			if end > i {
				alts := splitCommaOutsideBraces(glob[i+1 : end])
				b.WriteString("(?:")
				for j, alt := range alts {
					if j > 0 {
						b.WriteByte('|')
					}
					writeGlobFragment(b, alt)
				}
				b.WriteByte(')')
				i = end
			} else {
				b.WriteString(`\{`)
			}
		case '.', '+', '(', ')', '|', '^', '$', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
}

func findMatchingBrace(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitCommaOutsideBraces(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[{")
}

func isShellScript(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".sh" || ext == ".bash"
}

func isBatsFile(p string) bool {
	return strings.ToLower(filepath.Ext(p)) == ".bats"
}

func absOrJoin(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func relOrClean(base, p string) string {
	abs := absOrJoin(base, p)
	if rel, err := filepath.Rel(base, abs); err == nil {
		return rel
	}
	return filepath.Clean(p)
}
