package assets

import (
	"io/fs"
	"os/exec"
	"path"
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

// Each hook script is the POSIX sh setup can fill in: a comment, set -eu, and an exec of
// HookUpool, once, for the event its name gives (CONTRIBUTING.md, "Scripts").
func TestHookScriptsExecUpoolForTheirEvent(t *testing.T) {
	names, err := fs.Glob(Hooks, "hooks/*.sh")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Errorf("hook scripts = %q, want 3", names)
	}
	for _, name := range names {
		b, err := fs.ReadFile(Hooks, name)
		if err != nil {
			t.Fatal(err)
		}
		script := string(b)
		lines := strings.Split(strings.TrimSuffix(script, "\n"), "\n")
		event := strings.TrimSuffix(path.Base(name), ".sh")
		if len(lines) < 4 || lines[0] != "#!/bin/sh" || !strings.HasPrefix(lines[1], "# ") ||
			lines[len(lines)-2] != "set -eu" {
			t.Errorf("%s = %q, want #!/bin/sh, a comment, then set -eu", name, script)
		}
		if want := "exec " + HookUpool + " hook " + event; lines[len(lines)-1] != want {
			t.Errorf("%s ends in %q, want %q", name, lines[len(lines)-1], want)
		}
		if n := strings.Count(script, HookUpool); n != 1 {
			t.Errorf("%s has HookUpool %d times, want 1", name, n)
		}
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v: %s", name, err, out)
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
