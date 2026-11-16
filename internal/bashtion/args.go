//go:build unix

package bashtion

import (
	"fmt"
	"strings"
)

// SplitCommandArgs splits a command argument string into individual arguments,
// preserving quoted substrings (single and double quotes) and backslash escaping.
func SplitCommandArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && !inSingle {
			escaped = true
			continue
		}
		if c == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if c == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (c == ' ' || c == '\t' || c == '\n') && !inSingle && !inDouble {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(c)
	}

	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("unmatched quote or trailing escape in %q", s)
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args, nil
}
