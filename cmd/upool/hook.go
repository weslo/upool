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
	"github.com/weslo/upool/internal/tcc"
)

// worktreeCreate is `upool hook worktree-create`: it decodes the request on stdin and hands it to
// internal/hooks, which returns the worktree path the dispatcher prints (docs/contracts.md (a)).
func worktreeCreate(inv *invocation) (string, error) {
	return logged(inv, hooks.EventWorktreeCreate, func(env hooks.Env, req hooks.Request) (string,
		error) {
		return hooks.WorktreeCreate(context.Background(), withConsent(inv, env), req)
	})
}

// worktreeCreateDryRun is `upool hook worktree-create --dry-run`: the request on stdin run as far
// as the pooled branch's git call in the main checkout, making and taking nothing, so that it
// prints nothing. doctor's documents-access check runs it through the desktop app's helper
// (#286). It writes nothing to the pool's hook log, which records the hooks Claude Code runs, so
// that a check does not read as a session's create ((e), "The hook log").
func worktreeCreateDryRun(inv *invocation) (string, error) {
	req, err := hooks.Decode(inv.stdin)
	if err != nil {
		return "", err
	}
	return "", hooks.WorktreeCreateDryRun(context.Background(), withConsent(inv, hooksEnv(inv)),
		req)
}

// withConsent is env, the create hook's, with what it reads when its git call in the main checkout
// stalls (#286): the unified log, and upool's own binary with its symlinks resolved, the client
// macOS's Documents prompt names. A binary whose path cannot be read or resolved leaves Self empty,
// so that a prompt about any client counts.
func withConsent(inv *invocation, env hooks.Env) hooks.Env {
	env.Consent = tcc.LogShow{Env: inv.environ()}
	if inv.executable != nil {
		if exe, err := inv.executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				env.Self = resolved
			}
		}
	}
	return env
}

// worktreeRemove is `upool hook worktree-remove`: it decodes the request on stdin and hands it to
// internal/hooks. Its failExit is 0, so its error goes to stderr and the hook still exits 0
// (docs/contracts.md (d)).
func worktreeRemove(inv *invocation) (string, error) {
	return logged(inv, hooks.EventWorktreeRemove, func(env hooks.Env, req hooks.Request) (string,
		error) {
		return hooks.WorktreeRemove(context.Background(), env, req)
	})
}

// sessionStart is `upool hook session-start`: it decodes the request on stdin and hands it to
// internal/hooks, and returns the context document the dispatcher prints (A43). Its failExit is
// 0, so its error goes to stderr and the hook still exits 0 (docs/contracts.md (d)). A failure
// that still leaves a context, such as a confirm refused, goes to stderr here, so that the session
// gets its instructions all the same.
func sessionStart(inv *invocation) (string, error) {
	doc, err := logged(inv, hooks.EventSessionStart, func(env hooks.Env, req hooks.Request) (string,
		error) {
		return hooks.SessionStart(context.Background(), env, req)
	})
	if err != nil && doc != "" {
		say(inv.stderr, "hook session-start: "+err.Error())
		return doc, nil
	}
	return doc, err
}

// subagentStart is `upool hook subagent-start`: it decodes the request on stdin and hands it to
// internal/hooks, and returns the context document that gives a slot session's subagent upool's
// instructions (A43). Its failExit is 0, so its error goes to stderr and the hook still exits 0.
func subagentStart(inv *invocation) (string, error) {
	return logged(inv, hooks.EventSubagentStart, func(env hooks.Env, req hooks.Request) (string,
		error) {
		return hooks.SubagentStart(context.Background(), env, req)
	})
}

// preToolUse is `upool hook pre-tool-use`: it decodes the request on stdin and hands it to the lane
// guard, which returns the decision the dispatcher prints: a denial for a scene, prefab, asset or
// .meta file written in a script lane, and otherwise nothing (docs/design.md, "Lanes: scripts in
// parallel, scenes one at a time, `.meta` never regenerated").
func preToolUse(inv *invocation) (hooks.Decision, error) {
	return logged(inv, hooks.EventPreToolUse, func(env hooks.Env, req hooks.Request) (
		hooks.Decision, error) {
		return hooks.LaneGuard(env, req)
	})
}

// logged decodes the hook request on stdin and runs handle on it, between the two lines the hook
// log of the request's pool gets for the run of event's hook: one before the work, so that a run
// that is killed still leaves it, and one after, with what the hook did and the error it ended
// with (#278; docs/contracts.md (e), "The hook log"). A request that is not a pool's, or stdin that
// does not decode, logs nothing.
func logged[T any](inv *invocation, event string,
	handle func(env hooks.Env, req hooks.Request) (T, error)) (T, error) {
	req, err := hooks.Decode(inv.stdin)
	if err != nil {
		var none T
		return none, err
	}
	env := hooksEnv(inv)
	env.Log = hooks.StartLog(env, event, inv.pid, req)
	out, err := handle(env, req)
	env.Log.End(err)
	return out, err
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
