package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/assets"
)

// ContextDocument returns what a SessionStart or SubagentStart hook prints, on one line, to give
// the session upool's instructions: Claude Code's hookSpecificOutput with event as hookEventName and
// assets.Instructions for the pool's base as additionalContext, which reaches the model on every
// start and stays out of the project's own files (docs/contracts.md (b); A43; #284).
func ContextDocument(event string, base assets.Base) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]map[string]string{"hookSpecificOutput": {
		"hookEventName": event, "additionalContext": assets.Instructions(base)}})
	if err != nil {
		return "", fmt.Errorf("encoding the %s context: %w", event, err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// slotContext returns the context document for event that gives a session in ref's slot, or one
// of its subagents, upool's instructions, naming ref's pool's base, and on origin when the
// repository has origin/<base> (#284). It asks git in the main checkout, which shares its refs with
// every slot. When git cannot say, it warns on stderr and names the base alone, which a session
// can branch from in any repository, so that a failed check costs the session the fetch, not its
// instructions.
func slotContext(ctx context.Context, env Env, event string, ref slotRef) (string, error) {
	base := assets.Base{Name: ref.pool.Base}
	onOrigin, err := env.Git.RemoteBranchExists(ctx, ref.pool.Repo, "origin", base.Name)
	if err != nil {
		// stderr is for warnings only; stdout would reach the session's context.
		_, _ = fmt.Fprintf(env.Stderr, "upool: warning: could not tell whether origin has %s, so "+
			"the context names %s alone: %v\n", base.Name, base.Name, err)
	}
	base.OnOrigin = onOrigin
	return ContextDocument(event, base)
}

// SubagentStart handles a SubagentStart request: for a subagent of a session in a slot, found by
// the session's session_id or, failing that, by the slot that contains the subagent's cwd, as
// SessionStart finds it (sessionSlot; A46), it returns the context document that gives the
// subagent upool's instructions (slotContext), since a subagent does not see the SessionStart
// context its session had (docs/contracts.md (b); A43). For any other subagent it returns
// nothing. It changes no state. The hook exits 0 whatever this returns, its error on stderr.
func SubagentStart(ctx context.Context, env Env, req Request) (string, error) {
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
	doc, err := slotContext(ctx, env, EventSubagentStart, ref)
	if err == nil {
		env.Log.did(OutcomeContext, ref.slot.Name, "")
	}
	return doc, err
}
