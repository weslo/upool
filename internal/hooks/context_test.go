package hooks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// A subagent of a session in a slot gets upool's instructions as SubagentStart context, since it
// does not see its session's SessionStart context; a subagent of any other session gets nothing
// (A43). The capture is of a subagent of a session in a pass-through worktree, so its cwd is pointed
// at the slot.
func TestSubagentStartGivesASlotSessionsSubagentTheInstructions(t *testing.T) {
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	before := h.stateFile(t)
	req := h.capture(t, "cli-subagent-start.json")
	elsewhere := req.Cwd
	req.Cwd = path
	got, err := SubagentStart(context.Background(), h.env, req)
	if err != nil {
		t.Fatalf("SubagentStart: %v, want no error", err)
	}
	checkContext(t, got, EventSubagentStart)
	if after := h.stateFile(t); !after.ModTime().Equal(before.ModTime()) {
		t.Error("state.json was written, want it left alone")
	}
	for _, cwd := range []string{h.e.Repo, elsewhere} {
		req.Cwd = cwd
		if got, err := SubagentStart(context.Background(), h.env, req); err != nil || got != "" {
			t.Errorf("SubagentStart in %s = %q, %v; want nothing", cwd, got, err)
		}
	}
}

// originHarness is a start harness with slot-1 ready, detached at base as a slot is handed out,
// and a repository that has origin/<base>. It returns the harness and the slot's path.
func originHarness(t *testing.T) (harness, string) {
	t.Helper()
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	h.e.Git("update-ref", "refs/remotes/origin/"+testenv.Base, "HEAD")
	return h, path
}

// compactIn runs SessionStart on a compaction whose cwd is dir, which sends a session that may
// have worked in its slot for a while the instructions again, and returns what it printed.
func (h harness) compactIn(t *testing.T, dir string) string {
	t.Helper()
	req := h.capture(t, "session-start-compact.json")
	req.Cwd = dir
	got, err := SessionStart(context.Background(), h.env, req)
	if err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	return got
}

// A session is told to make its branch from origin/<base> only while its slot is as it was handed
// out, detached at a commit base holds (#284). Once it has made its branch, or committed on the
// detached HEAD, a start that sends the instructions again, as compact and resume do, tells it
// only never to check base out: a branch from base would leave its work behind. So it does when
// another ref holds that commit, as a `git branch` that does not switch, or a push of the
// detached HEAD, which writes the remote-tracking branch too, leaves it.
func TestSessionStartTellsOnlyADetachedSessionToBranchFromBase(t *testing.T) {
	h, slot := originHarness(t)
	inSlot := func(args ...string) { h.e.Git(append([]string{"-C", slot}, args...)...) }
	notBranching := assets.Branching{Base: testenv.Base}
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart,
		assets.Branching{Base: testenv.Base, Detached: true, OnOrigin: true})

	inSlot("switch", "-q", "-c", "issue-284")
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart, notBranching)
	h.e.WriteFile(".claude/worktrees/slot-1/Assets/Work.cs", "class Work { }\n")
	inSlot("add", "-A")
	inSlot("commit", "-qm", "the session's work")
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart, notBranching)

	inSlot("checkout", "-q", "--detach", testenv.Base)
	h.e.WriteFile(".claude/worktrees/slot-1/Assets/Loose.cs", "class Loose { }\n")
	inSlot("add", "-A")
	inSlot("commit", "-qm", "work on the detached HEAD")
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart, notBranching)
	inSlot("branch", "issue-285")
	inSlot("update-ref", "refs/remotes/origin/issue-285", "HEAD")
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart, notBranching)

	// Once origin's base holds the commit, as after a fetch of the merged work, or as when
	// `refresh --fetch` moves a ready slot to origin/<base> past the local base, a branch from
	// origin/<base> leaves nothing behind, and the session is told to make one.
	inSlot("update-ref", "refs/remotes/origin/"+testenv.Base, "HEAD")
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart,
		assets.Branching{Base: testenv.Base, Detached: true, OnOrigin: true})
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr.String())
	}
}

// A subagent is never told to make its branch from base, whether it works in the session's slot,
// still detached at base or on the session's branch, or in a worktree of its own, which the create
// hook makes detached at the slot's HEAD: there a branch from base would leave the session's work
// out of the subagent's tree (#284). It is told never to check base out.
func TestSubagentStartNeverTellsASubagentToBranchFromBase(t *testing.T) {
	h, slot := originHarness(t)
	want := assets.Branching{Base: testenv.Base}
	sub := h.capture(t, "cli-subagent-start.json")
	start := func(cwd string) {
		t.Helper()
		sub.Cwd = cwd
		got, err := SubagentStart(context.Background(), h.env, sub)
		if err != nil {
			t.Fatalf("SubagentStart in %s: %v, want no error", cwd, err)
		}
		checkContextFor(t, got, EventSubagentStart, want)
		for _, not := range []string{"git switch", "origin/" + testenv.Base, "detached"} {
			if strings.Contains(got, not) {
				t.Errorf("SubagentStart in %s says %q, want nothing of making a branch", cwd, not)
			}
		}
	}
	start(slot)
	h.e.Git("-C", slot, "switch", "-q", "-c", "issue-284")
	h.e.WriteFile(".claude/worktrees/slot-1/Assets/Work.cs", "class Work { }\n")
	h.e.Git("-C", slot, "add", "-A")
	h.e.Git("-C", slot, "commit", "-qm", "the session's work")
	start(slot)
	agent := filepath.Join(slot, ".claude", "worktrees", "agent-a0aa92dd6c84d1b66")
	h.e.Git("-C", slot, "worktree", "add", "-q", "--detach", agent)
	start(agent)
}

// When git cannot say whether the slot is detached, the session is told nothing of making a
// branch; when it cannot say whether origin has the base, a detached session is told to branch
// from the base alone, which it can in any repository. Each says why on stderr, and neither is an
// error: a failed check costs the session advice, never its instructions.
func TestSessionStartSaysLessWhenGitCannotSay(t *testing.T) {
	h, slot := originHarness(t)
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Repo = t.TempDir()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkContextFor(t, h.compactIn(t, slot), EventSessionStart,
		assets.Branching{Base: testenv.Base, Detached: true})
	want := "upool: warning: could not tell whether origin has " + testenv.Base
	if !strings.HasPrefix(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want a warning starting %q", h.stderr.String(), want)
	}

	h.stderr.Reset()
	gone := t.TempDir()
	err = pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots[0].Path = gone
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkContextFor(t, h.compactIn(t, gone), EventSessionStart,
		assets.Branching{Base: testenv.Base})
	want = "upool: warning: could not tell whether slot-1 is detached"
	if !strings.HasPrefix(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want a warning starting %q", h.stderr.String(), want)
	}
}

// SubagentStart refuses another event's request, and a cwd that is not absolute.
func TestSubagentStartRefusesWhatItCannotUse(t *testing.T) {
	h := startHarness(t, true, "session")
	req := h.capture(t, "cli-subagent-start.json")
	wrong := req
	wrong.HookEventName = EventSessionStart
	relative := req
	relative.Cwd = "worktrees/probe"
	for _, tt := range []struct {
		req  Request
		want string
	}{{wrong, "not SubagentStart"}, {relative, "is not an absolute path"}} {
		got, err := SubagentStart(context.Background(), h.env, tt.req)
		if err == nil || !strings.Contains(err.Error(), tt.want) || got != "" {
			t.Errorf("SubagentStart = %q, %v; want nothing and an error with %q", got, err,
				tt.want)
		}
	}
}
