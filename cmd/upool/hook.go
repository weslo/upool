package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

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
		EnvFile: inv.getenv("CLAUDE_ENV_FILE"), Getenv: inv.getenv,
		StartRefresh: func(checkout, key string) error { return startRefresh(inv, checkout, key) }}
}

// refreshLog is the file under ~/.upool/pools/<repo-key>/ that a detached refresh appends its
// output to (#79).
const refreshLog = "refresh.log"

// startRefresh starts `upool refresh` for the pool key names, run by this binary in checkout, the
// pool's main checkout, and returns without waiting for it. It runs detached, in a session of its
// own, with stdin from /dev/null and its output appended to ~/.upool/pools/<key>/refresh.log, so
// that it outlives the hook and holds none of the hook's streams, whose ends Claude Code waits for
// (docs/design.md, "Environment, confirmation and release": SessionStart spawns a detached
// `upool refresh`).
func startRefresh(inv *invocation, checkout, key string) error {
	exe, err := inv.executable()
	if err != nil {
		return fmt.Errorf("finding upool's own binary: %w", err)
	}
	file := filepath.Join(inv.home, ".upool", "pools", key, refreshLog)
	log, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", file, err)
	}
	// The child holds its own descriptor once started, so closing this one loses nothing.
	defer func() { _ = log.Close() }()
	cmd := exec.Command(exe, "refresh")
	// A nil Stdin reads from the null device.
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = checkout, inv.environ(), log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting upool refresh in %s: %w", checkout, err)
	}
	return cmd.Process.Release()
}
