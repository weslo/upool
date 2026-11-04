package assets

import (
	"os"
	"path/filepath"
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

// The block says what #53 asks of it (docs/contracts.md (c), the rule for the tool and the skill),
// and what #279 does: open's default mode, what each mode is for, and `upool close`
// (docs/contracts.md (b), "Context for the session").
func TestClaudeMDBlockSaysHowToWorkInASlot(t *testing.T) {
	for _, want := range []string{
		"never with `git worktree add`",
		"`upool which`",
		"`UNITY_PROJECT_PATH`",
		"Pass it positionally to `unity test`",
		"`close` and `cache key`",
		"`unity command save_all` before `unity close \"$UNITY_PROJECT_PATH\"`",
		"`upool open`, headless by default for imports, builds and tests",
		"`upool open --gui` for play mode and captures",
		"close it with `upool close`",
	} {
		if !strings.Contains(ClaudeMDBlock, want) {
			t.Errorf("claude-md-block.md does not say %q", want)
		}
	}
}

// The skill opens with the frontmatter Claude Code and other agents read, its name matching its
// directory and its description on one line, and ends in a newline.
func TestSkillHasFrontmatter(t *testing.T) {
	checkSkillFrontmatter(t, "skill/upool/SKILL.md", Skill, "upool")
}

// The benchmark skill opens with the same frontmatter (#267; docs/benchmarking.md). It is not
// embedded: a project installs it from skills/ with `npx skills add weslo/upool`, which looks
// there and not under assets/, so this test reads it from the repository.
func TestBenchmarkSkillHasFrontmatter(t *testing.T) {
	path := filepath.Join("..", "skills", "upool-benchmark", "SKILL.md")
	skill, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkSkillFrontmatter(t, path, string(skill), "upool-benchmark")
}

// checkSkillFrontmatter checks that skill, the SKILL.md at path, is in a directory named name,
// opens with frontmatter between --- lines that names it name with a description on one line,
// has a body, and ends in a newline.
func checkSkillFrontmatter(t *testing.T, path, skill, name string) {
	t.Helper()
	if dir := filepath.Base(filepath.Dir(path)); dir != name {
		t.Errorf("%s is in directory %q, want %q", path, dir, name)
	}
	front, body, ok := strings.Cut(strings.TrimPrefix(skill, "---\n"), "\n---\n")
	if !strings.HasPrefix(skill, "---\n") || !ok {
		t.Fatalf("%s does not open with frontmatter between --- lines", path)
	}
	lines := strings.Split(front, "\n")
	if len(lines) != 2 || lines[0] != "name: "+name ||
		!strings.HasPrefix(lines[1], "description: ") || len(lines[1]) < len("description: ")+20 {
		t.Errorf("%s frontmatter = %q, want name: %s and a description", path, front, name)
	}
	if strings.TrimSpace(body) == "" || !strings.HasSuffix(skill, "\n") {
		t.Errorf("%s has no body, or does not end in a newline", path)
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

// The instructions a slot session gets as context are the block, the item that names the pool's
// base, then the skill's body without its front matter, well within the 10,000 characters Claude
// Code's docs allow a hook's context (A43; #229; #284).
func TestInstructionsAreTheBlockTheBaseAndTheSkillsBody(t *testing.T) {
	b := Branching{Base: "main", Detached: true, OnOrigin: true}
	got := Instructions(b)
	_, body, ok := strings.Cut(strings.TrimPrefix(Skill, "---\n"), "\n---\n")
	if !ok {
		t.Fatal("the skill has no front matter to strip")
	}
	if want := ClaudeMDBlock + b.line() + "\n" + strings.TrimLeft(body, "\n"); got != want {
		t.Errorf("Instructions() = %q, want the block, the base's item, a blank line and the "+
			"skill's body", got)
	}
	lines := strings.Split(strings.TrimSuffix(ClaudeMDBlock, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "- ") &&
		!strings.HasPrefix(last, "  ") {
		t.Errorf("claude-md-block.md ends in %q, want a list item, which the base's item "+
			"continues", last)
	}
	if strings.Contains(got, "\ndescription:") || strings.HasPrefix(got, "---") {
		t.Error("Instructions() carries the skill's front matter")
	}
	if !strings.Contains(got, "# Working in a upool slot") {
		t.Error("Instructions() lacks the skill's body")
	}
	if len(got) >= 10000 {
		t.Errorf("Instructions() is %d bytes, want fewer than 10,000", len(got))
	}
}

// The instructions say in one item that the main checkout has the pool's base checked out, so
// that the reader never checks it out (#284). A session whose slot is detached, as handed out, is
// also told that the slot starts detached at the base; to make its branch from origin/<base>
// after a fetch, or from the base itself in a repository with no origin (A17); and that a commit
// left on the detached HEAD parks the slot. Any other reader, a session on a branch or a subagent,
// is told nothing about making a branch: from base, it would leave its own work behind. Each place
// the item names the base, it names it as the pool has it.
func TestInstructionsSayToBranchFromTheBaseOnlyToADetachedSession(t *testing.T) {
	detached := []string{
		"Your slot starts detached at `develop`, which the main checkout has checked out",
		"Before you commit, make your branch",
		"a commit left on the detached HEAD parks the slot when the session ends",
	}
	for _, tt := range []struct {
		b         Branching
		want, not []string
	}{
		{Branching{Base: "develop", Detached: true, OnOrigin: true}, append([]string{
			"`git fetch origin`, then `git switch -c <name> origin/develop`:"}, detached...), nil},
		{Branching{Base: "develop", Detached: true}, append([]string{
			"make your branch with `git switch -c <name> develop`:"}, detached...),
			[]string{"git fetch", "origin/"}},
		{Branching{Base: "develop", OnOrigin: true},
			[]string{"The main checkout has `develop` checked out"},
			[]string{"detached", "branch", "git switch", "git fetch", "origin/", "parks"}},
		{Branching{Base: "develop"}, []string{"The main checkout has `develop` checked out"},
			[]string{"detached", "branch", "git switch", "git fetch", "origin/", "parks"}},
	} {
		item := tt.b.line()
		if !strings.HasPrefix(item, "- ") || strings.Count(item, "\n") != 1 ||
			!strings.HasSuffix(item, "\n") {
			t.Errorf("the item for %+v = %q, want one list item ending in a newline", tt.b,
				item)
		}
		for _, want := range append([]string{
			"never run `git checkout develop` here, even where a workflow says to",
		}, tt.want...) {
			if !strings.Contains(item, want) {
				t.Errorf("the item for %+v = %q, want it to say %q", tt.b, item, want)
			}
		}
		for _, not := range tt.not {
			if strings.Contains(item, not) {
				t.Errorf("the item for %+v = %q, want no %q", tt.b, item, not)
			}
		}
		if got := Instructions(tt.b); !strings.Contains(got, ClaudeMDBlock+item+"\n") {
			t.Errorf("Instructions(%+v) does not follow the block with %q", tt.b, item)
		}
	}
}
