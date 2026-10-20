package main

import (
	"context"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/hooks"
)

// worktreeCreate is `upool hook worktree-create`: it decodes the request on stdin and hands it to
// internal/hooks, which returns the worktree path the dispatcher prints (docs/contracts.md (a)).
func worktreeCreate(inv *invocation) (string, error) {
	req, err := hooks.Decode(inv.stdin)
	if err != nil {
		return "", err
	}
	return hooks.WorktreeCreate(context.Background(), hooksEnv(inv), req)
}

// hooksEnv is what internal/hooks takes from the process, from inv.
func hooksEnv(inv *invocation) hooks.Env {
	return hooks.Env{Git: git.Client{Runner: git.Exec{}, Env: inv.environ()}, Home: inv.home,
		ParentPID: inv.ppid, Now: inv.now, Stderr: inv.stderr}
}
