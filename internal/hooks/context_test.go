package hooks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A subagent of a session in a slot gets upool's instructions as SubagentStart context, since it
// does not see its session's SessionStart context; a subagent of any other session gets nothing
// (A43). The capture is of a subagent of a session in a pass-through worktree, so its cwd is pointed
// at the slot.
func TestSubagentStartGivesASlotSessionsSubagentTheInstructions(t *testing.T) {
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	before := h.stateFile(t)
	req := h.capture(t, "cli-subagent-start.json")
	elsewhere := req.Cwd
	req.Cwd = path
	got, err := SubagentStart(context.Background(), h.env, req)
	if err != nil {
		t.Fatalf("SubagentStart: %v, want no error", err)
	}
	checkContext(t, got, EventSubagentStart)
	if after := h.stateFile(t); !after.ModTime().Equal(before.ModTime()) {
		t.Error("state.json was written, want it left alone")
	}
	for _, cwd := range []string{h.e.Repo, elsewhere} {
		req.Cwd = cwd
		if got, err := SubagentStart(context.Background(), h.env, req); err != nil || got != "" {
			t.Errorf("SubagentStart in %s = %q, %v; want nothing", cwd, got, err)
		}
	}
}

// SubagentStart refuses another event's request, and a cwd that is not absolute.
func TestSubagentStartRefusesWhatItCannotUse(t *testing.T) {
	h := startHarness(t, true, "session")
	req := h.capture(t, "cli-subagent-start.json")
	wrong := req
	wrong.HookEventName = EventSessionStart
	relative := req
	relative.Cwd = "worktrees/probe"
	for _, tt := range []struct {
		req  Request
		want string
	}{{wrong, "not SubagentStart"}, {relative, "is not an absolute path"}} {
		got, err := SubagentStart(context.Background(), h.env, tt.req)
		if err == nil || !strings.Contains(err.Error(), tt.want) || got != "" {
			t.Errorf("SubagentStart = %q, %v; want nothing and an error with %q", got, err,
				tt.want)
		}
	}
}
