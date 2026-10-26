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
// assets.Instructions as additionalContext, which reaches the model on every start and stays out of
// the project's own files (docs/contracts.md (b); A43).
func ContextDocument(event string) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(map[string]map[string]string{"hookSpecificOutput": {
		"hookEventName": event, "additionalContext": assets.Instructions()}})
	if err != nil {
		return "", fmt.Errorf("encoding the %s context: %w", event, err)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// SubagentStart handles a SubagentStart request: for a subagent of a session in a slot, found by
// the session's session_id or, failing that, by the slot that contains the subagent's cwd, as
// SessionStart finds it (sessionSlot; A46), it returns the context document that gives the
// subagent upool's instructions, since a subagent does not see the SessionStart context its
// session had (docs/contracts.md (b); A43). For any other subagent it returns nothing. It
// changes no state. The hook exits 0 whatever this returns, its error on stderr.
func SubagentStart(_ context.Context, env Env, req Request) (string, error) {
	if req.HookEventName != EventSubagentStart {
		return "", fmt.Errorf("stdin is a %q request, not %s", req.HookEventName,
			EventSubagentStart)
	}
	if !filepath.IsAbs(req.Cwd) {
		return "", fmt.Errorf("the request's cwd %q is not an absolute path", req.Cwd)
	}
	_, ok, _, err := sessionSlot(env.Home, req.SessionID, req.Cwd)
	if err != nil || !ok {
		return "", err
	}
	return ContextDocument(EventSubagentStart)
}
