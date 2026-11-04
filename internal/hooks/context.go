package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/git"
)

// ContextDocument returns what a SessionStart or SubagentStart hook prints, on one line, to give
// the session upool's instructions: Claude Code's hookSpecificOutput with event as hookEventName and
// assets.Instructions, saying what b says about branching, as additionalContext, which reaches the
// model on every start and stays out of the project's own files (docs/contracts.md (b); A43;
// #284).
func ContextDocument(event string, b assets.Branching) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]map[string]string{"hookSpecificOutput": {
		"hookEventName": event, "additionalContext": assets.Instructions(b)}})
	if err != nil {
		return "", fmt.Errorf("encoding the %s context: %w", event, err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// sessionBranching returns what the instructions tell a session in ref's slot about branching
// (#284). The session is told to make its branch from the pool's base only while its slot is as it
// was handed out: detached, at a commit some ref holds, which a startup's confirm, run first, may
// just have made it with the deferred reset (A5). A session that has made its branch, or committed
// on the detached HEAD, would leave its own work behind, as a `compact` or `resume` start that
// sends the instructions again would tell it to. Such a session is told only never to check base
// out. A Detached session branches from origin/<base> when the repository has it. sessionBranching
// asks git: in the slot, whether HEAD is detached and held by a ref, and in the main checkout,
// which shares its refs with every slot, whether origin has the base. When git cannot say, it
// warns on stderr and says less: nothing of making a branch when the slot's HEAD is unknown, and
// the base alone, which a session can branch from in any repository, when origin is. A failed
// check costs the session advice, never its instructions.
func sessionBranching(ctx context.Context, env Env, ref slotRef) assets.Branching {
	b := assets.Branching{Base: ref.pool.Base}
	detached, err := handedOut(ctx, env.Git, ref.slot.Path)
	if err != nil {
		// stderr is for warnings only; stdout would reach the session's context.
		_, _ = fmt.Fprintf(env.Stderr, "upool: warning: could not tell whether %s is detached, so "+
			"the context says nothing of making a branch: %v\n", ref.slot.Name, err)
		return b
	}
	if !detached {
		return b
	}
	b.Detached = true
	b.OnOrigin, err = env.Git.RemoteBranchExists(ctx, ref.pool.Repo, "origin", b.Base)
	if err != nil {
		// stderr is for warnings only; stdout would reach the session's context.
		_, _ = fmt.Fprintf(env.Stderr, "upool: warning: could not tell whether origin has %s, so "+
			"the context names %s alone: %v\n", b.Base, b.Base, err)
	}
	return b
}

// handedOut reports whether the worktree at dir is as a slot is handed out: its HEAD detached,
// at a commit some ref holds, so that no commit of a session's would be left behind
// (docs/contracts.md (f), the definition of clean).
func handedOut(ctx context.Context, g git.Client, dir string) (bool, error) {
	_, onBranch, err := g.SymbolicRef(ctx, dir, "HEAD")
	if err != nil || onBranch {
		return false, err
	}
	return g.HeadReachable(ctx, dir)
}

// SubagentStart handles a SubagentStart request: for a subagent of a session in a slot, found by
// the session's session_id or, failing that, by the slot that contains the subagent's cwd, as
// SessionStart finds it (sessionSlot; A46), it returns the context document that gives the
// subagent upool's instructions, since a subagent does not see the SessionStart context its
// session had (docs/contracts.md (b); A43). They tell it never to check out the pool's base, and
// nothing of making a branch from it: its worktree, the slot itself or one the create hook made at
// the slot's HEAD, is wherever its session has taken it, and from base its branch would leave the
// session's work behind (#284). For any other subagent it returns nothing. It changes no state.
// The hook exits 0 whatever this returns, its error on stderr.
func SubagentStart(_ context.Context, env Env, req Request) (string, error) {
	if req.HookEventName != EventSubagentStart {
		return "", fmt.Errorf("stdin is a %q request, not %s", req.HookEventName,
			EventSubagentStart)
	}
	if !filepath.IsAbs(req.Cwd) {
		return "", fmt.Errorf("the request's cwd %q is not an absolute path", req.Cwd)
	}
	ref, ok, err := sessionSlot(env.Home, req.SessionID, req.Cwd)
	if err != nil || !ok {
		return "", err
	}
	doc, err := ContextDocument(EventSubagentStart, assets.Branching{Base: ref.pool.Base})
	if err == nil {
		env.Log.did(OutcomeContext, ref.slot.Name, "")
	}
	return doc, err
}
