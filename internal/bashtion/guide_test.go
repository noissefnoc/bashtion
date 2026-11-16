package bashtion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentGuideAndSkillAreInSync(t *testing.T) {
	skillPath := filepath.Join("..", "..", "skills", "bashtion", "SKILL.md")
	skillBytes, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("failed to read %s: %v", skillPath, err)
	}
	skillContent := strings.ReplaceAll(string(skillBytes), "\r\n", "\n")
	guideContent := strings.ReplaceAll(AgentGuide, "\r\n", "\n")

	const bodyMarker = "\n---\n\n## "
	guideIdx := strings.Index(guideContent, bodyMarker)
	if guideIdx < 0 {
		t.Fatalf("expected %q section delimiter in internal/bashtion/guide.md", bodyMarker)
	}
	skillIdx := strings.Index(skillContent, bodyMarker)
	if skillIdx < 0 {
		t.Fatalf("expected %q section delimiter in %s", bodyMarker, skillPath)
	}

	// Verify the intro line differs only by "Use this guide" vs "Use this skill".
	guidePreamble := guideContent[:guideIdx]
	skillPreamble := skillContent[:skillIdx]
	guideIntroLine := extractIntroLine(t, guidePreamble, "internal/bashtion/guide.md")
	skillIntroLine := extractIntroLine(t, skillPreamble, skillPath)
	if got := strings.Replace(skillIntroLine, "Use this skill ", "Use this guide ", 1); got != guideIntroLine {
		t.Errorf("intro paragraph mismatch between guide.md and SKILL.md:\n  guide: %s\n  skill: %s", guideIntroLine, skillIntroLine)
	}

	guideBody := guideContent[guideIdx:]
	skillBody := skillContent[skillIdx:]
	if guideBody != skillBody {
		t.Errorf("internal/bashtion/guide.md and %s are out of sync after the header/frontmatter; keep both files updated together", skillPath)
	}
}

func extractIntroLine(t *testing.T, preamble, name string) string {
	t.Helper()
	for _, line := range strings.Split(preamble, "\n") {
		if strings.HasPrefix(line, "Use this ") {
			return line
		}
	}
	t.Fatalf("missing 'Use this ...' intro line in %s", name)
	return ""
}
