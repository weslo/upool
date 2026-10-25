//go:build darwin

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/mirror"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the hooks and a project's .worktreeinclude (A44; #233). They run on macOS only,
// since the files are copied with cp -cR, macOS's clonefile(2) (internal/clone).

// The acceptance test of #233, the archive: a slot whose only differences from its base are the
// files the mirror copied in, all of them gitignored, is clean, so the remove after its session's
// archive marks it needs-reset, not parked (transition 8).
func TestWorktreeIncludeMirroredSlotNeedsResetOnArchive(t *testing.T) {
	h := newHarness(t, true)
	h.e.WriteFile(mirror.File, "CLAUDE.md\n.claude/skills/\n")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", path)
	r, err := mirror.Mirror(context.Background(), h.env.Git, clone.CP{}, h.e.Repo, path)
	if err != nil || len(r.Copied) != 2 {
		t.Fatalf("Mirror = %+v, %v; want CLAUDE.md and the skill link copied", r, err)
	}
	h.remove(t, h.capture(t, "desktop-session-remove.json"), path)
	h.checkEnded(t, "slot-1", pool.StateNeedsReset)
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing for a clean slot", h.stderr)
	}
}

// A subagent's worktree, which the create hook makes, gets the files the main checkout's
// .worktreeinclude names when it is made, as Claude Code copies them into a worktree it makes
// itself, and no other gitignored file (#233).
func TestWorktreeIncludeCopiedIntoASubagentWorktree(t *testing.T) {
	h := newHarness(t, true)
	h.e.WriteFile(mirror.File, "CLAUDE.md\n.claude/skills/\n")
	req := h.capture(t, "subagent-create.json")
	path, err := WorktreeCreate(context.Background(), h.env, req)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(h.e.Path("CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(path, "CLAUDE.md")); err != nil ||
		string(got) != string(want) {
		t.Errorf("the subagent's CLAUDE.md = %q, %v; want the main checkout's %q", got, err, want)
	}
	if link, err := os.Readlink(filepath.Join(path, testenv.SkillLink)); err != nil ||
		link != testenv.SkillTarget {
		t.Errorf("the subagent's %s -> %q, %v; want a link to %q", testenv.SkillLink, link, err,
			testenv.SkillTarget)
	}
	for _, name := range []string{"AGENTS.md", ".claude/settings.local.json"} {
		if _, err := os.Lstat(filepath.Join(path, filepath.FromSlash(name))); err == nil {
			t.Errorf("the subagent's worktree has %s, which the .worktreeinclude does not name",
				name)
		}
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr)
	}
}
