package assets

import (
	"strings"
	"testing"

	"github.com/weslo/upool/internal/block"
)

// The block can be upserted: a version of at least 1, and no marker in its body.
func TestClaudeMDBlockCanBeUpserted(t *testing.T) {
	b := block.Block{Version: ClaudeMDBlockVersion, Body: ClaudeMDBlock}
	if _, _, err := block.Apply([]byte("# Project\n"), b); err != nil {
		t.Errorf("Apply: %v", err)
	}
	if !strings.HasSuffix(ClaudeMDBlock, "\n") {
		t.Error("claude-md-block.md does not end in a newline")
	}
}

// The block says what #53 asks of it (docs/contracts.md (c), the rule for the tool and the skill).
func TestClaudeMDBlockSaysHowToWorkInASlot(t *testing.T) {
	for _, want := range []string{
		"never with `git worktree add`",
		"`upool which`",
		"`UNITY_PROJECT_PATH`",
		"Pass it positionally to `unity test`",
		"`close` and `cache key`",
		"`unity command save_all` before `unity close \"$UNITY_PROJECT_PATH\"`",
	} {
		if !strings.Contains(ClaudeMDBlock, want) {
			t.Errorf("claude-md-block.md does not say %q", want)
		}
	}
}

// The skill opens with the frontmatter Claude Code and other agents read, its name matching its
// directory and its description on one line, and ends in a newline.
func TestSkillHasFrontmatter(t *testing.T) {
	front, body, ok := strings.Cut(strings.TrimPrefix(Skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(Skill, "---\n") || !ok {
		t.Fatalf("SKILL.md does not open with frontmatter between --- lines")
	}
	lines := strings.Split(front, "\n")
	if len(lines) != 2 || lines[0] != "name: upool" ||
		!strings.HasPrefix(lines[1], "description: ") || len(lines[1]) < len("description: ")+20 {
		t.Errorf("frontmatter = %q, want name: upool and a description", front)
	}
	if strings.TrimSpace(body) == "" || !strings.HasSuffix(Skill, "\n") {
		t.Error("SKILL.md has no body, or does not end in a newline")
	}
}

// The skill says what #68 asks of it (docs/contracts.md (c), the rule for the tool and the skill;
// docs/design.md, "Hooks route, skills teach, leases and lanes keep agents from colliding").
func TestSkillSaysHowToDriveUnityInASlot(t *testing.T) {
	for _, want := range []string{
		"`UNITY_PROJECT_PATH`",
		"`unity test`, `run`, `build`, `close` and `cache key` ignore the variable",
		"`unity command save_all` before `unity close \"$UNITY_PROJECT_PATH\"`",
		"Never hand-edit scene or prefab YAML",
		"while a live Editor is reachable",
		"Re-query before retrying",
		"upool take <branch>",
		"`upool release <slot>`",
		"open your scene explicitly",
	} {
		if !strings.Contains(Skill, want) {
			t.Errorf("SKILL.md does not say %q", want)
		}
	}
}
