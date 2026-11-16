package gitdiff

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiEscapeRegex.ReplaceAllString(s, "")
}

// ChangedLines runs `git diff --no-color --no-ext-diff --no-textconv --unified=0 <ref> -- <targetFile>`
// and returns the set of modified/added 1-based line numbers in the new version of targetFile.
func ChangedLines(ctx context.Context, workDir, ref, targetFile string) (map[uint]bool, error) {
	trimmedRef := strings.TrimSpace(ref)
	if strings.HasPrefix(trimmedRef, "-") {
		return nil, fmt.Errorf("invalid git diff ref %q: must not start with '-'", ref)
	}

	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--unified=0"}
	if trimmedRef != "" {
		args = append(args, trimmedRef)
	}
	args = append(args, "--", targetFile)

	cmd := exec.CommandContext(ctx, "git", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git diff failed (%v): %s", err, strings.TrimSpace(string(out)))
	}

	lines, err := ParseUnifiedDiffLines(string(out))
	if err != nil {
		return nil, err
	}

	if len(lines) == 0 {
		// Check whether targetFile is tracked by git to give an informative error on untracked files
		chkCmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", "--", targetFile)
		if workDir != "" {
			chkCmd.Dir = workDir
		}
		if chkOut, chkErr := chkCmd.CombinedOutput(); chkErr != nil {
			return nil, fmt.Errorf("target file %q is not tracked by git; cannot compute --git-diff: %s", targetFile, strings.TrimSpace(string(chkOut)))
		}
	}

	return lines, nil
}

// ParseUnifiedDiffLines parses unified diff output and returns all 1-based line numbers
// added or modified on the "+" side of diff hunks.
func ParseUnifiedDiffLines(diffOutput string) (map[uint]bool, error) {
	lines := make(map[uint]bool)
	br := bufio.NewReaderSize(strings.NewReader(diffOutput), 64*1024)
	const maxHunkHeaderInspectBytes = 8 * 1024

	for {
		rawLine, readErr := readDiffLinePrefix(br, maxHunkHeaderInspectBytes)
		if len(rawLine) > 0 {
			line := stripANSI(rawLine)
			if strings.HasPrefix(line, "@@ ") {
				// Example header: @@ -10,2 +12,4 @@ or @@ -10 +12 @@
				plusIdx := strings.Index(line, " +")
				if plusIdx >= 0 {
					rest := line[plusIdx+2:]
					endIdx := strings.Index(rest, " @@")
					if endIdx >= 0 {
						rangeSpec := rest[:endIdx]
						start, count, err := parseHunkRange(rangeSpec)
						if err != nil {
							return nil, fmt.Errorf("invalid hunk range %q: %w", rangeSpec, err)
						}
						for i := uint(0); i < count; i++ {
							lines[start+i] = true
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

	return lines, nil
}

// readDiffLinePrefix reads up to maxInspect bytes of the next line and drains any remaining
// bytes through '\n' so arbitrarily long diff lines (>64KiB) never fail parsing.
func readDiffLinePrefix(br *bufio.Reader, maxInspect int) (string, error) {
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
			return strings.TrimRight(string(buf), "\r\n"), err
		}
	}
}

func parseHunkRange(spec string) (uint, uint, error) {
	parts := strings.SplitN(spec, ",", 2)
	start, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return 0, 0, err
	}
	if len(parts) == 1 {
		return uint(start), 1, nil
	}
	count, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return 0, 0, err
	}
	return uint(start), uint(count), nil
}
