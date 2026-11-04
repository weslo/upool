package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// The hook log (#278; docs/contracts.md (e), "The hook log").

// logOf returns the lines of h's pool's hook log.
func (h harness) logOf(t *testing.T) []LogLine {
	t.Helper()
	lines, err := ReadLog(h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// StartLog appends the start line at once, before any work, so that a run that never ends, as one
// that is killed, leaves it; End appends the end line, with what the handler did.
func TestHookLogStartLineComesBeforeTheWork(t *testing.T) {
	h := newHarness(t, true)
	req := h.capture(t, "desktop-session-remove.json")
	l := StartLog(h.env, EventWorktreeRemove, 4242, req)
	lines := h.logOf(t)
	slot := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	want := LogLine{Time: clock, PID: 4242, Event: EventWorktreeRemove, Phase: PhaseStart,
		Kind: string(DesktopSession), Path: slot}
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("log = %+v, want only the start line %+v", lines, want)
	}

	l.did(OutcomeParked, "slot-1", "")
	l.End(nil)
	lines = h.logOf(t)
	want.Phase, want.Outcome, want.Slot = PhaseEnd, OutcomeParked, "slot-1"
	if len(lines) != 2 || lines[1] != want {
		t.Errorf("log = %+v, want the start line, then %+v", lines, want)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr.String())
	}
}

// An end line with no outcome recorded is none when the hook succeeded and failed when it did not,
// and carries the error either way; a nil Log logs nothing and records nothing.
func TestHookLogEndWithoutAnOutcome(t *testing.T) {
	h := newHarness(t, true)
	req := h.capture(t, "desktop-session-create.json")
	StartLog(h.env, EventWorktreeCreate, 1, req).End(nil)
	StartLog(h.env, EventWorktreeCreate, 2, req).End(errors.New("no slot was free"))
	l := StartLog(h.env, EventSessionStart, 3, h.capture(t, "session-start.json"))
	l.did(OutcomeContext, "slot-1", "")
	l.End(errors.New("confirming: refused"))
	var ends []string
	for _, line := range h.logOf(t) {
		if line.Phase == PhaseEnd {
			ends = append(ends, line.Outcome+"|"+line.Error)
		}
	}
	want := []string{"none|", "failed|no slot was free", "context|confirming: refused"}
	if strings.Join(ends, " ") != strings.Join(want, " ") {
		t.Errorf("end lines = %q, want %q", ends, want)
	}

	var none *Log
	none.did(OutcomeSlot, "slot-1", "")
	none.End(nil)
	if got := len(h.logOf(t)); got != 6 {
		t.Errorf("log has %d lines, want the 6 above", got)
	}
}

// The kind and path of each event's lines: who sent a create or a remove, a start's source, a
// subagent, a tool; and the remove's worktree_path, the file a tool writes, made absolute against
// cwd, or else cwd.
func TestHookLogKindAndPath(t *testing.T) {
	h := newHarness(t, false)
	slot := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	tests := []struct {
		event, capture string
		set            func(*Request)
		kind, path     string
	}{
		{EventWorktreeCreate, "subagent-create.json", nil, string(Subagent), h.e.Repo},
		{EventWorktreeCreate, "desktop-session-create.json", nil, string(DesktopSession),
			h.e.Repo},
		{EventWorktreeCreate, "cli-session-create.json", nil, string(CLISession), h.e.Repo},
		{EventWorktreeRemove, "desktop-session-remove.json", nil, string(DesktopSession), slot},
		{EventWorktreeRemove, "cli-session-remove.json", nil, string(CLISession),
			filepath.Join(h.e.Repo, ".claude", "worktrees", "a15-probe")},
		{EventSessionStart, "session-start-compact.json", nil, "compact", ""},
		{EventSubagentStart, "cli-subagent-start.json", nil, string(Subagent), ""},
		{EventPreToolUse, "pretooluse-write.json", func(r *Request) {
			r.Cwd, r.ToolInput.FilePath = slot, "Assets/Main.unity"
		}, "Write", filepath.Join(slot, "Assets", "Main.unity")},
		{EventPreToolUse, "pretooluse-notebookedit.json", nil, "NotebookEdit", ""},
	}
	for _, tt := range tests {
		req := h.capture(t, tt.capture)
		if tt.set != nil {
			tt.set(&req)
		}
		path := tt.path
		switch {
		case path == "" && tt.event == EventPreToolUse:
			path = req.ToolInput.NotebookPath
		case path == "":
			path = req.Cwd
		}
		if k, p := logKind(tt.event, req), logPath(tt.event, req); k != tt.kind || p != path {
			t.Errorf("%s on %s: kind %q, path %q; want %q and %q", tt.event, tt.capture, k, p,
				tt.kind, path)
		}
	}
}

// A hook's lines go to the pool of the registered repository that contains the request's path, a
// slot under it included, the innermost where repositories nest, or else the one containing its
// cwd; a request in no registered repository has none.
func TestHookLogPool(t *testing.T) {
	e := testenv.New(t)
	outer, err := registry.Add(context.Background(), e.Home, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(e.Repo, "Packages", "inner")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	inner, err := registry.Add(context.Background(), e.Home, nested)
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	slot := filepath.Join(e.Repo, ".claude", "worktrees", "slot-1")
	tests := []struct {
		path, cwd, want string
	}{
		{slot, e.Repo, outer.Key},
		{filepath.Join(nested, "Assets", "a.prefab"), e.Repo, inner.Key},
		{elsewhere, slot, outer.Key},
		{"", e.Repo, outer.Key},
		{elsewhere, elsewhere, ""},
	}
	for _, tt := range tests {
		key, ok, err := logPool(e.Home, tt.path, tt.cwd)
		if err != nil || key != tt.want || ok != (tt.want != "") {
			t.Errorf("logPool(%q, %q) = %q, %t, %v; want %q", tt.path, tt.cwd, key, ok, err,
				tt.want)
		}
	}
}

// ReadLog reads a missing log as none, and passes over a line that does not decode, keeping the
// lines around it.
func TestHookLogReadPassesOverABadLine(t *testing.T) {
	h := newHarness(t, true)
	if lines := h.logOf(t); len(lines) != 0 {
		t.Fatalf("missing log = %+v, want no lines", lines)
	}
	req := h.capture(t, "desktop-session-create.json")
	StartLog(h.env, EventWorktreeCreate, 7, req)
	path := filepath.Join(h.e.Home, ".upool", "pools", h.key, LogFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"time":"2026-10-19T12:00:00Z","pid":7,"ev` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	StartLog(h.env, EventWorktreeCreate, 8, req)
	lines := h.logOf(t)
	if len(lines) != 2 || lines[0].PID != 7 || lines[1].PID != 8 {
		t.Errorf("log = %+v, want the lines of processes 7 and 8", lines)
	}
}

// A WorktreeRemove that runs after the slot's directory is gone, as when the desktop app deletes it
// first, ends failed, saying why, with the slot still leased: the order the hook log tells apart
// (docs/contracts.md (a), WorktreeRemove rules).
func TestHookLogRemoveAfterTheDirectoryIsGone(t *testing.T) {
	h := newHarness(t, true)
	slot := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", slot)
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	req := h.capture(t, "desktop-session-remove.json")
	env := h.env
	env.Log = StartLog(env, EventWorktreeRemove, 9, req)
	_, err := WorktreeRemove(context.Background(), env, req)
	env.Log.End(err)
	lines := h.logOf(t)
	if err == nil || len(lines) != 2 || lines[1].Outcome != OutcomeFailed ||
		!strings.Contains(lines[1].Error, "checking whether slot-1 is clean") {
		t.Errorf("err %v, log %+v; want the remove failed checking slot-1, and logged so", err,
			lines)
	}
	if s := h.slot(t, "slot-1"); s.State != pool.StateLeased {
		t.Errorf("slot-1 = %s, want leased still", s.State)
	}
}
