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
