// Package hooks is the logic of the Claude Code hooks that `upool hook <event>` runs: it decodes a
// hook's stdin, tells who sent it, and handles it. A handler returns the worktree path and an
// error, and only the dispatcher writes stdout, where Claude Code takes the path from, so that
// nothing else can reach it (docs/design.md, "Repository, CI and Homebrew distribution": the
// layout and the rules after it; docs/contracts.md (a), "WorktreeCreate output and app
// behaviour").
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// The hook_event_name values of the events upool handles (docs/contracts.md (a) and (b)).
const (
	EventWorktreeCreate = "WorktreeCreate"
	EventWorktreeRemove = "WorktreeRemove"
	EventSessionStart   = "SessionStart"
)

// Request is a hook's stdin, decoded leniently: a field Claude Code adds is ignored, and one it
// leaves out is empty (docs/contracts.md (a), "WorktreeCreate fields by request kind"; (b)).
type Request struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	// Name is the worktree's name: generated from the first prompt in the desktop app, given by
	// the user to `claude --worktree`, agent-<17 hex> for a subagent. Only a subagent's names a
	// path; for a session it is a hint, never a key.
	Name string `json:"name"`
	// ScratchpadDir and PromptID are a subagent's create request's, and absent from a session's.
	ScratchpadDir string `json:"scratchpad_dir"`
	PromptID      string `json:"prompt_id"`
	// WorktreePath is WorktreeRemove's: the path the create hook printed.
	WorktreePath string `json:"worktree_path"`
	// Source is SessionStart's: startup, resume, clear or compact.
	Source string `json:"source"`
}

// Decode reads a hook's stdin.
func Decode(r io.Reader) (Request, error) {
	var req Request
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return Request{}, fmt.Errorf("decoding the hook's stdin: %w", err)
	}
	return req, nil
}

// Kind is who sent a WorktreeCreate or WorktreeRemove request.
type Kind string

// The kinds of request (docs/contracts.md (a), "Telling the kinds apart").
const (
	Subagent       Kind = "subagent"
	DesktopSession Kind = "desktop session"
	CLISession     Kind = "CLI session"
)

// Kind returns who sent a WorktreeCreate or WorktreeRemove request: a subagent if it has a
// prompt_id, a desktop session if its transcript_path is empty, and otherwise a CLI session
// (docs/contracts.md (a), "Telling the kinds apart"). A SessionStart request cannot be told apart
// this way, since its transcript_path is always set ((b)).
func (r Request) Kind() Kind {
	switch {
	case r.PromptID != "":
		return Subagent
	case r.TranscriptPath == "":
		return DesktopSession
	}
	return CLISession
}

// agentName is the name of a subagent's worktree, agent-<17 hex> in every capture
// (docs/contracts.md (a)).
var agentName = regexp.MustCompile(`^agent-[0-9a-f]{17}$`)

// Corroborated says whether a subagent's request also has a scratchpad_dir and a name of the form
// agent-<17 hex>, as every captured one does (docs/contracts.md (a), "Telling the kinds apart").
func (r Request) Corroborated() bool {
	return r.ScratchpadDir != "" && agentName.MatchString(r.Name)
}

// Env is what the hooks take from the process that runs them.
type Env struct {
	Git  git.Client
	Home string
	// ParentPID is the hook process's parent: the session's CLI, which a subagent's worktree
	// records as its parent (docs/contracts.md (e), Agent worktree).
	ParentPID int
	Now       func() time.Time
	// Stderr is for warnings; Claude Code takes the worktree path from stdout alone.
	Stderr io.Writer
}

// worktreesDir is Claude Code's managed worktree directory, relative to a repository.
const worktreesDir = ".claude/worktrees"

// WorktreeCreate handles a WorktreeCreate request and returns the path of the worktree the
// session or subagent is to work in; an error aborts the creation, and with it the session or
// subagent (docs/contracts.md (a)). A user-scope hook has no matcher, so it runs for every
// repository and every kind of request, and branches three ways on stdin alone: a subagent gets an
// ordinary worktree, a session in a pool one of its slots, and any other session the pass-through
// worktree (docs/design.md, "Route Claude Code's worktrees through the pool: three branches in
// one hook"). The pooled branch is refused until it is built (#78).
func WorktreeCreate(ctx context.Context, env Env, req Request) (string, error) {
	if req.HookEventName != EventWorktreeCreate {
		return "", fmt.Errorf("stdin is a %q request, not %s", req.HookEventName,
			EventWorktreeCreate)
	}
	if !filepath.IsAbs(req.Cwd) {
		return "", fmt.Errorf("the request's cwd %q is not an absolute path", req.Cwd)
	}
	if req.Kind() == Subagent {
		return subagentWorktree(ctx, env, req)
	}
	repo, pooled, err := registry.Containing(env.Home, req.Cwd)
	if err != nil {
		return "", err
	}
	if pooled {
		return "", fmt.Errorf("%s is in pool %s, and handing a session one of its slots is not "+
			"built yet", req.Cwd, repo.Key)
	}
	return passThroughWorktree(ctx, env, req)
}

// checkName refuses a worktree name that is not one path element, before it becomes part of a
// path under .claude/worktrees.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("the worktree name %q is not a single path element", name)
	}
	return nil
}

// passThroughWorktree is the pass-through branch, for a session in a repository upool does not
// manage, or in one of a pool's slots (A13): the worktree Claude Code would have made itself, at
// <cwd>/.claude/worktrees/<name> on a branch named <name>, so that the user's other projects keep
// working under a hook they cannot opt out of (docs/design.md, "Route Claude Code's worktrees
// through the pool: three branches in one hook"). The desktop app runs a session's hook in a helper
// that, under ~/Documents, could run only git and stat paths (docs/contracts.md (a)), so the
// branch opens no file in the repository: git does all of it.
func passThroughWorktree(ctx context.Context, env Env, req Request) (string, error) {
	if err := checkName(req.Name); err != nil {
		return "", err
	}
	path := filepath.Join(req.Cwd, filepath.FromSlash(worktreesDir), req.Name)
	if err := env.Git.WorktreeAddBranch(ctx, req.Cwd, path); err != nil {
		return "", fmt.Errorf("making the worktree %s: %w", path, err)
	}
	return path, nil
}

// subagentWorktree is the subagent branch: an ordinary worktree, detached at the repository's
// HEAD, at <cwd>/.claude/worktrees/<name>, which is what Claude Code would have made itself. A
// subagent never gets a pool slot, since no WorktreeRemove ever comes for it (spike §9 F.3). If cwd
// is in a registered pool, the worktree is recorded in its state.json for `prune` to collect.
func subagentWorktree(ctx context.Context, env Env, req Request) (string, error) {
	if err := checkName(req.Name); err != nil {
		return "", err
	}
	if !req.Corroborated() {
		_, _ = fmt.Fprintf(env.Stderr, "upool: warning: a request with a prompt_id, so from a "+
			"subagent, has no scratchpad_dir or a name %q not of the form agent-<17 hex>\n",
			req.Name) // stderr is for warnings only
	}
	path := filepath.Join(req.Cwd, filepath.FromSlash(worktreesDir), req.Name)
	if err := env.Git.WorktreeAddDetached(ctx, req.Cwd, path); err != nil {
		return "", fmt.Errorf("making the subagent's worktree %s: %w", path, err)
	}
	if err := recordAgentWorktree(ctx, env, req, path); err != nil {
		if rmErr := env.Git.WorktreeRemove(ctx, req.Cwd, path); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("removing %s again: %w", path, rmErr))
		}
		return "", err
	}
	return path, nil
}

// recordAgentWorktree adds the agent worktree at path to the state.json of the pool that the
// request's cwd is in, matched as A13 says, with the request's session as its parent; outside
// every pool it records nothing.
func recordAgentWorktree(ctx context.Context, env Env, req Request, path string) error {
	repo, ok, err := registry.Containing(env.Home, req.Cwd)
	if err != nil || !ok {
		return err
	}
	return pool.Update(ctx, env.Home, repo.Key, func(p *pool.Pool) error {
		// git made the worktree just now, so an entry already at its path is a stale one, for a
		// worktree removed by hand: the new one replaces it.
		p.AgentWorktrees = slices.DeleteFunc(p.AgentWorktrees, func(a pool.AgentWorktree) bool {
			return a.Path == path
		})
		p.AgentWorktrees = append(p.AgentWorktrees, pool.AgentWorktree{Path: path,
			ParentSessionID: req.SessionID, ParentPID: env.ParentPID,
			CreatedAt: env.Now().UTC()})
		return nil
	})
}
