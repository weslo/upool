package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// `upool hook worktree-create` on a captured subagent request prints exactly the new worktree's
// path and a newline, writes nothing to stderr, and records its own parent as the agent
// worktree's parent_pid: the process that ran the hook (#71).
func TestHookWorktreeCreatePrintsTheSubagentsWorktree(t *testing.T) {
	e := testenv.New(t)
	repo, err := registry.Add(context.Background(), e.Home, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.Update(context.Background(), e.Home, repo.Key, func(p *pool.Pool) error {
		p.Repo, p.Base = e.Repo, testenv.Base
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"subagent-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: e.Home},
		pathvars.Var{Name: "repo", Path: e.Repo},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(e.Home, "claude-tmp")})
	if err != nil {
		t.Fatal(err)
	}

	const ppid = 31337
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid(), ppid: ppid}
	if code := d.run([]string{"hook", "worktree-create"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	path := filepath.Join(e.Repo, ".claude", "worktrees", "agent-a59f8e76142da8348")
	if want := path + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want exactly %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
	p, err := pool.Load(e.Home, repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AgentWorktrees) != 1 || p.AgentWorktrees[0].Path != path ||
		p.AgentWorktrees[0].ParentPID != ppid {
		t.Errorf("agent_worktrees = %+v, want %s with parent_pid %d", p.AgentWorktrees, path,
			ppid)
	}
}

// `upool hook worktree-create` on a captured desktop session's request, in a repository that is not
// a pool, prints exactly the pass-through worktree's path and a newline (#72).
func TestHookWorktreeCreatePrintsThePassThroughWorktree(t *testing.T) {
	e := testenv.New(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: e.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-create"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	path := filepath.Join(e.Repo, ".claude", "worktrees", "system-diagnostics-9f8a09")
	if want := path + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want exactly %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
	if got := e.Git("worktree", "list", "--porcelain"); !strings.Contains(got,
		"worktree "+path+"\n") {
		t.Errorf("git worktree list = %q, want %s", got, path)
	}
}

// After `upool take`, the WorktreeRemove hook on the captured archive of a session in slot-1 marks
// the slot needs-reset, clears the lease, prints the slot's path and exits 0 (#73).
func TestHookWorktreeRemoveMarksTheTakenSlot(t *testing.T) {
	p := newHarnessPool(t)
	if r := runIn(t, p.Env, p.Repo, "take", "feature"); r.code != output.ExitOK {
		t.Fatalf("take: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: p.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: p.Getenv, environ: p.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-remove"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	if want := p.slot("slot-1") + "\n"; out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr %q; want %q and nothing", out.String(), errOut.String(),
			want)
	}
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	if s := pl.Slots[0]; s.Name != "slot-1" || s.State != pool.StateNeedsReset || s.Lease != nil {
		t.Errorf("slot-1 = %+v, want needs-reset with no lease", s)
	}
}
