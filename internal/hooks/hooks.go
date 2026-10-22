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
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/paths"
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
	// ToolName and ToolInput are PreToolUse's: the tool about to run, and the arguments that name
	// the file it writes (testdata/captures/hooks/pretooluse-*.json).
	ToolName  string    `json:"tool_name"`
	ToolInput ToolInput `json:"tool_input"`
}

// ToolInput is the part of a PreToolUse request's tool_input that names the file the tool writes:
// file_path for Edit and Write, notebook_path for NotebookEdit.
type ToolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
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

// Kind returns who sent a WorktreeCreate request: a subagent if it has a prompt_id, a desktop
// session if its transcript_path is empty, and otherwise a CLI session (docs/contracts.md (a),
// "Telling the kinds apart"). Only WorktreeCreate can be read this way: the CLI's own
// WorktreeRemove and SessionEnd carry a prompt_id too (testdata/captures/hooks/cli-session-*.json),
// and SessionStart's transcript_path is always set ((b)).
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
	// EnvFile is CLAUDE_ENV_FILE, set for SessionStart hooks: the file whose export lines reach
	// the session's Bash tool (docs/contracts.md (b), `CLAUDE_ENV_FILE`).
	EnvFile string
	// Getenv looks up an environment variable, for the configuration the pooled branch loads.
	Getenv func(string) string
	// StartRefresh starts `upool refresh` detached for the pool key names, in its main checkout,
	// and returns once it has started; the session-start hook calls it after each handout it
	// confirms (#79). Nil starts nothing.
	StartRefresh func(checkout, key string) error
}

// worktreesDir is Claude Code's managed worktree directory, relative to a repository.
const worktreesDir = ".claude/worktrees"

// WorktreeCreate handles a WorktreeCreate request and returns the path of the worktree the
// session or subagent is to work in; an error aborts the creation, and with it the session or
// subagent (docs/contracts.md (a)). A user-scope hook has no matcher, so it runs for every
// repository and every kind of request, and branches three ways on stdin alone: a subagent gets an
// ordinary worktree, a session in a pool one of its slots, and any other session the pass-through
// worktree (docs/design.md, "Route Claude Code's worktrees through the pool: three branches in
// one hook").
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
		return pooledWorktree(ctx, env, req, repo)
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
// that, under ~/Documents, could run git and stat paths but not read a file (docs/contracts.md
// (a)), so the branch opens no file in the repository: git does all of it.
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

// WorktreeRemove handles a WorktreeRemove request, which the desktop app sends about a second after
// a session is archived, and returns the removed worktree's path. For a pool's slot it judges the
// slot clean or dirty with git reads alone, which worked from the app's restricted helper, and
// marks a clean slot needs-reset and a dirty one parked, its status lines on stderr, clearing the
// lease (docs/contracts.md (f) transitions 8 and 9). The slot stays on disk for the deferred reset:
// the app accepts exit 0 with the directory still present, and
// hands the path out again (docs/contracts.md (a), WorktreeRemove rules). Any other worktree is a
// pass-through one, and is removed when clean (A22). The hook exits 0 whatever this returns, its
// error on stderr ((d)).
func WorktreeRemove(ctx context.Context, env Env, req Request) (string, error) {
	if req.HookEventName != EventWorktreeRemove {
		return "", fmt.Errorf("stdin is a %q request, not %s", req.HookEventName,
			EventWorktreeRemove)
	}
	if !filepath.IsAbs(req.WorktreePath) {
		return "", fmt.Errorf("the request's worktree_path %q is not an absolute path",
			req.WorktreePath)
	}
	ref, ok, err := findSlot(env.Home, req.WorktreePath)
	if err != nil {
		return "", err
	}
	if !ok {
		return removePassThrough(ctx, env, req)
	}
	s := ref.slot
	if s.Lease == nil {
		return "", fmt.Errorf("%s is %s, with no lease to end", s.Name, s.State)
	}
	dirty, err := pool.DirtyBesideNoise(ctx, env.Git, s.Path)
	if err != nil {
		return "", fmt.Errorf("checking whether %s is clean: %w", s.Name, err)
	}
	err = pool.Update(ctx, env.Home, ref.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		cur := &p.Slots[i]
		if cur.Lease == nil || cur.Lease.LeaseID != s.Lease.LeaseID {
			return fmt.Errorf("%s's lease changed in state.json meanwhile", s.Name)
		}
		next, err := pool.Next(*cur, pool.Remove{Clean: len(dirty) == 0})
		if err != nil {
			return err
		}
		cur.State, cur.Lease, cur.LastUsed = next, nil, env.Now().UTC()
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, d := range dirty {
		// stderr is for warnings only; the app reads nothing from a remove hook's stdout.
		_, _ = fmt.Fprintf(env.Stderr, "upool: parked %s, which is dirty: %s\n", s.Name, d)
	}
	return s.Path, nil
}

// removePassThrough handles the removal of a worktree that is no pool's slot: one the pass-through
// branch made, for a repository upool does not manage or a session in a slot. Clean as
// docs/contracts.md (f) defines it, it is removed with `git -C <cwd> worktree remove
// <worktree_path>`, its branch and commits kept; dirty, it is kept, and stderr says why (A22).
// Nothing records pass-through worktrees, so nothing else would ever remove one. Define-order noise
// counts as clean and is reverted first, so that git's own check, which refuses any change, lets
// the worktree go: the removal is a git write already, of the kind the create branch makes from the
// app's restricted helper.
func removePassThrough(ctx context.Context, env Env, req Request) (string, error) {
	if !filepath.IsAbs(req.Cwd) {
		return "", fmt.Errorf("the request's cwd %q is not an absolute path", req.Cwd)
	}
	path := req.WorktreePath
	// A stat, which worked from the helper under ~/Documents (docs/contracts.md (a)).
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return path, nil // already gone: nothing is left to remove
	}
	dirty, err := pool.DirtyBesideNoise(ctx, env.Git, path)
	if err != nil {
		return "", fmt.Errorf("checking whether %s is clean: %w", path, err)
	}
	if len(dirty) > 0 {
		for _, d := range dirty {
			// stderr is for warnings only; the app reads nothing from a remove hook's stdout.
			_, _ = fmt.Fprintf(env.Stderr, "upool: kept %s, which is dirty: %s\n", path, d)
		}
		return path, nil
	}
	if _, err := pool.RevertNoise(ctx, env.Git, path); err != nil {
		return "", fmt.Errorf("reverting define-order noise in %s: %w", path, err)
	}
	if err := env.Git.WorktreeRemoveClean(ctx, req.Cwd, path); err != nil {
		return "", fmt.Errorf("removing %s: %w", path, err)
	}
	return path, nil
}

// slotRef is a slot of a registered pool: the registry's entry for the pool, the pool as
// state.json records it, and the slot.
type slotRef struct {
	repo registry.Repo
	pool pool.Pool
	slot pool.Slot
}

// findSlot returns the slot of a registered pool whose path is path, by canonical key, since the
// desktop app may hand the path back lowercased, and whether there is one.
func findSlot(home, path string) (slotRef, bool, error) {
	want, err := paths.Key(path)
	if err != nil {
		return slotRef{}, false, err
	}
	repos, err := registry.List(home)
	if err != nil {
		return slotRef{}, false, err
	}
	for _, repo := range repos {
		p, err := pool.Load(home, repo.Key)
		if err != nil {
			return slotRef{}, false, err
		}
		for _, s := range p.Slots {
			k, err := paths.Key(s.Path)
			if err != nil {
				return slotRef{}, false, err
			}
			if k == want {
				return slotRef{repo: repo, pool: p, slot: s}, true, nil
			}
		}
	}
	return slotRef{}, false, nil
}
