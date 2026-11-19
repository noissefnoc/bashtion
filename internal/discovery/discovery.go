package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TargetPair represents a resolved Bash target script and its corresponding bats test file(s).
type TargetPair struct {
	TargetFile string
	TestFiles  []string
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

// ResolvePairs resolves target patterns and pairs each target script with one or more .bats test files using:
//  1. Explicit concrete --test file(s) or glob (when single target)
//  2. Explicit mappings from .bashtion.toml (`[mappings]`)
//  3. Naming convention candidates (<dir>/<stem>.bats, test/<rel_dir>/<stem>.bats, test/<stem>.bats, etc.)
//  4. Static analysis of `source` / `.` / `load` directives inside .bats files
func ResolvePairs(workDir string, targetPatterns []string, testPatterns []string, explicitMappings map[string][]string, listOnly bool) ([]TargetPair, error) {
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

	expandedTestPatterns := expandRawPatterns(workDir, testPatterns)

	// Fast path: single target + explicit concrete test file(s) (no glob or directory)
	if len(targets) == 1 && len(expandedTestPatterns) > 0 && areAllConcreteFiles(workDir, expandedTestPatterns) {
		return []TargetPair{{
			TargetFile: targets[0],
			TestFiles:  dedupePaths(expandedTestPatterns),
		}}, nil
	}

	// Collect candidate .bats files in the workspace (or within testPatterns if specified)
	candidateBats, allExplicitConcrete, hasDirPattern, err := collectCandidateBats(workDir, expandedTestPatterns)
	if err != nil {
		return nil, err
	}

	// Single target with explicit glob pattern(s) (e.g. --test "test/foo_*.bats"): run all matched .bats files
	if len(targets) == 1 && len(expandedTestPatterns) > 0 && !hasDirPattern && len(candidateBats) > 0 {
		return []TargetPair{{
			TargetFile: targets[0],
			TestFiles:  candidateBats,
		}}, nil
	}

	// Pre-index which target scripts each .bats file sources or loads
	batsSourceRefs := indexBatsSourceRefs(workDir, candidateBats)
	restrictToCandidates := len(expandedTestPatterns) > 0

	pairs := make([]TargetPair, 0, len(targets))
	for _, target := range targets {
		matchedTests := resolveTestsForTarget(workDir, target, candidateBats, explicitMappings, batsSourceRefs, restrictToCandidates)
		if len(matchedTests) == 0 && allExplicitConcrete && len(candidateBats) > 0 {
			matchedTests = append([]string(nil), candidateBats...)
		}
		if len(matchedTests) == 0 {
			return nil, formatDiscoveryFailureError(workDir, target, expandedTestPatterns, candidateBats)
		}
		pairs = append(pairs, TargetPair{
			TargetFile: target,
			TestFiles:  matchedTests,
		})
	}

	return pairs, nil
}

func expandRawPatterns(workDir string, rawPatterns []string) []string {
	var patterns []string
	for _, raw := range rawPatterns {
		if raw != "" {
			if _, err := os.Stat(absOrJoin(workDir, raw)); err == nil {
				patterns = append(patterns, raw)
				continue
			}
		}
		for _, part := range splitCommaOutsideBraces(raw) {
			if p := strings.TrimSpace(part); p != "" {
				patterns = append(patterns, p)
			}
		}
	}
	return patterns
}

func areAllConcreteFiles(workDir string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		if hasGlobMeta(p) {
			return false
		}
		if info, err := os.Stat(absOrJoin(workDir, p)); err == nil && info.IsDir() {
			return false
		}
	}
	return true
}

func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, p)
		}
	}
	return out
}

func conventionCandidates(cleanTarget string) []string {
	targetBase := path.Base(cleanTarget)
	ext := path.Ext(targetBase)
	stem := strings.TrimSuffix(targetBase, ext)
	targetDir := path.Dir(cleanTarget)

	var raw []string
	if targetDir == "." {
		raw = []string{
			stem + ".bats",
			path.Join("test", stem+".bats"),
			path.Join("tests", stem+".bats"),
		}
	} else {
		trimmedSubdir := trimSourceRootPrefix(targetDir)
		raw = []string{
			path.Join(targetDir, stem+".bats"),
			path.Join("test", targetDir, stem+".bats"),
			path.Join("tests", targetDir, stem+".bats"),
			path.Join("test", trimmedSubdir, stem+".bats"),
			path.Join("tests", trimmedSubdir, stem+".bats"),
			path.Join("test", stem+".bats"),
			path.Join("tests", stem+".bats"),
		}
	}

	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		clean := path.Clean(c)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out
}

func formatDiscoveryFailureError(workDir, target string, testPatterns, candidateBats []string) error {
	cleanTarget := filepath.ToSlash(filepath.Clean(relOrClean(workDir, target)))
	convs := conventionCandidates(cleanTarget)

	var b strings.Builder
	if len(testPatterns) > 0 {
		fmt.Fprintf(&b, "could not find matching .bats test file for %q within --test %s\n", target, strings.Join(testPatterns, ", "))
	} else {
		fmt.Fprintf(&b, "could not auto-discover a .bats test file for %q\n", target)
	}
	fmt.Fprintf(&b, "  checked naming conventions: %s\n", strings.Join(convs, ", "))
	scope := "workspace"
	if len(testPatterns) > 0 {
		scope = strings.Join(testPatterns, ", ")
	}
	if len(candidateBats) == 0 {
		fmt.Fprintf(&b, "  checked source/load directives: 0 .bats files found in %s\n", scope)
	} else {
		fmt.Fprintf(&b, "  checked source/load directives in %d .bats file(s) in %s (no matching reference found)\n", len(candidateBats), scope)
	}
	fmt.Fprint(&b, "  hint: pass --test <file.bats> (repeatable or comma-separated) or configure [mappings] in .bashtion.toml")
	return errors.New(b.String())
}

func resolveTestsForTarget(
	workDir, target string,
	candidateBats []string,
	explicitMappings map[string][]string,
	batsSourceRefs map[string][]string,
	restrictToCandidates bool,
) []string {
	cleanTarget := filepath.ToSlash(filepath.Clean(relOrClean(workDir, target)))
	targetBase := filepath.Base(cleanTarget)
	ext := filepath.Ext(targetBase)
	stem := strings.TrimSuffix(targetBase, ext)

	// 1. Check explicit mappings from config (sorted for deterministic iteration)
	keys := make([]string, 0, len(explicitMappings))
	for k := range explicitMappings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := explicitMappings[k]
		if len(vals) == 0 {
			continue
		}
		cleanKey := filepath.ToSlash(filepath.Clean(relOrClean(workDir, k)))
		if cleanKey == cleanTarget || k == target {
			return dedupePaths(vals)
		}
	}

	var matched []string
	seen := make(map[string]bool)
	addMatch := func(p string) {
		norm := filepath.ToSlash(filepath.Clean(relOrClean(workDir, p)))
		if !seen[norm] {
			seen[norm] = true
			matched = append(matched, p)
		}
	}

	// 2. Check convention candidates in priority order
	convCandidates := conventionCandidates(cleanTarget)
	candidateByRel := make(map[string]string, len(candidateBats))
	for _, b := range candidateBats {
		relB := filepath.ToSlash(filepath.Clean(relOrClean(workDir, b)))
		candidateByRel[relB] = b
	}

	for _, conv := range convCandidates {
		if m, ok := candidateByRel[conv]; ok {
			addMatch(m)
			continue
		}
		if restrictToCandidates {
			continue
		}
		absConv := absOrJoin(workDir, filepath.FromSlash(conv))
		if info, err := os.Stat(absConv); err == nil && !info.IsDir() {
			if filepath.IsAbs(target) {
				addMatch(absConv)
			} else {
				addMatch(filepath.FromSlash(conv))
			}
		}
	}

	// 3. Check static analysis of `source` / `.` / `load` in candidate .bats files.
	// Pass 3a collects all full/path-qualified matches (e.g. src/common.sh != lib/common.sh).
	for _, b := range candidateBats {
		for _, ref := range batsSourceRefs[b] {
			if refMatchesTargetExact(ref, cleanTarget) {
				addMatch(b)
				break
			}
		}
	}
	if len(matched) > 0 {
		return matched
	}

	// Pass 3b falls back to basename/stem matches only when the reference has no static directory prefix.
	for _, b := range candidateBats {
		for _, ref := range batsSourceRefs[b] {
			if refMatchesTargetBase(ref, targetBase, stem) {
				addMatch(b)
				break
			}
		}
	}
	if len(matched) > 0 {
		return matched
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
			return stemMatches
		}
	}

	return nil
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

func collectCandidateBats(workDir string, testPatterns []string) (candidates []string, allExplicitConcrete bool, hasDirPattern bool, err error) {
	if len(testPatterns) == 0 {
		files, walkErr := walkScriptsInDir(workDir, workDir, ".", isBatsFile)
		return files, false, false, walkErr
	}

	allExplicitConcrete = true
	seen := make(map[string]bool)
	addCandidate := func(p string) {
		clean := filepath.Clean(p)
		if !seen[clean] {
			seen[clean] = true
			candidates = append(candidates, p)
		}
	}

	for _, pat := range testPatterns {
		if hasGlobMeta(pat) {
			allExplicitConcrete = false
			matches, globErr := expandGlob(workDir, pat, isBatsFile)
			if globErr != nil {
				return nil, false, false, globErr
			}
			if len(matches) == 0 {
				return nil, false, false, fmt.Errorf("no .bats test files matched pattern %q", pat)
			}
			for _, m := range matches {
				addCandidate(m)
			}
			continue
		}
		absTest := absOrJoin(workDir, pat)
		info, statErr := os.Stat(absTest)
		if statErr != nil {
			return nil, false, false, fmt.Errorf("test path %q: %w", pat, statErr)
		}
		if info.IsDir() {
			allExplicitConcrete = false
			hasDirPattern = true
			dirMatches, walkErr := walkScriptsInDir(workDir, absTest, pat, isBatsFile)
			if walkErr != nil {
				return nil, false, false, walkErr
			}
			for _, m := range dirMatches {
				addCandidate(m)
			}
			continue
		}
		addCandidate(pat)
	}

	return candidates, allExplicitConcrete, hasDirPattern, nil
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
