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

// worktreeRemove is `upool hook worktree-remove`: it decodes the request on stdin and hands it to
// internal/hooks. Its failExit is 0, so its error goes to stderr and the hook still exits 0
// (docs/contracts.md (d)).
func worktreeRemove(inv *invocation) (string, error) {
	req, err := hooks.Decode(inv.stdin)
	if err != nil {
		return "", err
	}
	return hooks.WorktreeRemove(context.Background(), hooksEnv(inv), req)
}

// sessionStart is `upool hook session-start`: it decodes the request on stdin and hands it to
// internal/hooks. Its failExit is 0, so its error goes to stderr and the hook still exits 0
// (docs/contracts.md (d)).
func sessionStart(inv *invocation) (string, error) {
	req, err := hooks.Decode(inv.stdin)
	if err != nil {
		return "", err
	}
	return hooks.SessionStart(context.Background(), hooksEnv(inv), req)
}

// hooksEnv is what internal/hooks takes from the process, from inv.
func hooksEnv(inv *invocation) hooks.Env {
	return hooks.Env{Git: git.Client{Runner: git.Exec{}, Env: inv.environ()}, Home: inv.home,
		ParentPID: inv.ppid, Now: inv.now, Stderr: inv.stderr,
		EnvFile: inv.getenv("CLAUDE_ENV_FILE"), Getenv: inv.getenv}
}
