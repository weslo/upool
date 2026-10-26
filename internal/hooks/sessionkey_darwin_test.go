//go:build darwin

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/pool"
)

// The acceptance test of #244: a session in a slot, and its subagents, get upool's instructions
// wherever Claude Code reports their cwd. The five captures are the M4 gate rerun's, from one
// desktop session (2026-10-25). Its startup confirm records the session's session_id on the
// lease. Then every later start finds the slot by it: a subagent from the slot, one with worktree
// isolation in its own worktree inside the slot, one after the session's `cd Assets`, and the
// compaction after that `cd`, whose export still names the slot (A46). macOS only, as the confirm
// records the holder's start time.
func TestSessionAndSubagentStartsFindTheSlotBySessionID(t *testing.T) {
	h := startHarness(t, true, "session")
	startup := h.capture(t, "desktop-slot-session-start.json")
	slot := startup.Cwd
	h.readySlot(t, "slot-1", slot)
	h.hookLease(t)
	if err := h.start(t, startup); err != nil {
		t.Fatalf("SessionStart startup: %v", err)
	}
	if l := h.slot(t, "slot-1").Lease; l == nil || l.SessionID == nil ||
		*l.SessionID != startup.SessionID {
		t.Fatalf("lease = %+v, want it to record session_id %s", l, startup.SessionID)
	}
	for _, file := range []string{"desktop-slot-subagent-start.json",
		"desktop-slot-isolated-subagent-start.json", "desktop-slot-cd-subagent-start.json"} {
		req := h.capture(t, file)
		got, err := SubagentStart(context.Background(), h.env, req)
		if err != nil {
			t.Errorf("%s: %v", file, err)
		}
		checkContext(t, got, EventSubagentStart)
	}
	compact := h.capture(t, "desktop-slot-cd-compact-session-start.json")
	if compact.Cwd == slot {
		t.Fatalf("the compaction's cwd is the slot, want the slot's Assets")
	}
	// Each start is given a CLAUDE_ENV_FILE of its own, so that an export it should make is not
	// mistaken for the startup's.
	compacted := h
	compacted.env.EnvFile = filepath.Join(t.TempDir(), "compact.sh")
	got, err := SessionStart(context.Background(), compacted.env, compact)
	if err != nil {
		t.Errorf("SessionStart compact: %v", err)
	}
	checkContext(t, got, EventSessionStart)
	compacted.checkExported(t, slot)

	// A compaction found by its session_id after the session has gone outside the slot still
	// exports the slot, as A14 has a compaction do.
	away := h
	away.env.EnvFile = filepath.Join(t.TempDir(), "away.sh")
	elsewhere := compact
	elsewhere.Cwd = t.TempDir()
	got, err = SessionStart(context.Background(), away.env, elsewhere)
	if err != nil {
		t.Errorf("SessionStart compact outside the slot: %v", err)
	}
	checkContext(t, got, EventSessionStart)
	away.checkExported(t, slot)

	// A resume found by its session_id with cwd under the slot exports the slot, as A14 has it.
	inside := h
	inside.env.EnvFile = filepath.Join(t.TempDir(), "resume-inside.sh")
	resumeInside := compact
	resumeInside.Source = "resume"
	got, err = SessionStart(context.Background(), inside.env, resumeInside)
	if err != nil {
		t.Errorf("SessionStart resume under the slot: %v", err)
	}
	checkContext(t, got, EventSessionStart)
	inside.checkExported(t, slot)

	// The session_id finds the slot when cwd is outside it too: a subagent, and a resume launched
	// from the main checkout, which records its parent as the holder and exports the slot, where
	// the resumed session works (A47).
	outside := h.capture(t, "desktop-slot-subagent-start.json")
	outside.Cwd = h.e.Repo
	got, err = SubagentStart(context.Background(), h.env, outside)
	if err != nil {
		t.Errorf("SubagentStart from the main checkout: %v", err)
	}
	checkContext(t, got, EventSubagentStart)
	resume := compact
	resume.Cwd, resume.Source = h.e.Repo, "resume"
	if err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots[0].Lease.HolderPID, p.Slots[0].Lease.HolderPIDStart = nil, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumed := h
	resumed.env.EnvFile = filepath.Join(t.TempDir(), "resumed.sh")
	got, err = SessionStart(context.Background(), resumed.env, resume)
	if err != nil {
		t.Errorf("SessionStart resume from the main checkout: %v", err)
	}
	checkContext(t, got, EventSessionStart)
	checkHolder(t, h.slot(t, "slot-1").Lease, os.Getpid())
	resumed.checkExported(t, slot)
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr)
	}
}

// A start whose session_id names no lease, as an earlier upool's confirm recorded none, falls back
// to the slot that contains its cwd, and a startup there confirms that slot. A start in the main
// checkout, which holds no lease, still gets nothing (A46).
func TestASessionStartWithNoLeaseFallsBackToTheSlotItIsIn(t *testing.T) {
	h := startHarness(t, true, "session")
	startup := h.capture(t, "desktop-slot-session-start.json")
	h.readySlot(t, "slot-1", startup.Cwd)
	h.hookLease(t)
	if err := h.start(t, startup); err != nil {
		t.Fatalf("SessionStart startup: %v", err)
	}
	req := h.capture(t, "desktop-slot-cd-subagent-start.json")
	req.SessionID = "00000000-0000-4000-8000-000000000000"
	got, err := SubagentStart(context.Background(), h.env, req)
	if err != nil {
		t.Errorf("SubagentStart: %v", err)
	}
	checkContext(t, got, EventSubagentStart)

	// A desktop /clear after the session's `cd` starts a new session, with a new session_id, whose
	// startup reports the cwd under the slot: it confirms the slot, and its session_id is recorded.
	clear := startup
	clear.Cwd = h.capture(t, "desktop-slot-cd-subagent-start.json").Cwd
	clear.SessionID = "00000000-0000-4000-8000-000000000002"
	if err := h.start(t, clear); err != nil {
		t.Errorf("SessionStart startup under the slot: %v", err)
	}
	if l := h.slot(t, "slot-1").Lease; l == nil || l.SessionID == nil ||
		*l.SessionID != clear.SessionID {
		t.Errorf("lease = %+v, want it to record the new session_id %s", l, clear.SessionID)
	}

	main := startup
	main.Cwd = h.e.Repo
	main.SessionID = "00000000-0000-4000-8000-000000000001"
	main.Source = "compact"
	if got, err := SessionStart(context.Background(), h.env, main); err != nil || got != "" {
		t.Errorf("SessionStart in the main checkout = %q, %v; want nothing", got, err)
	}
	main.HookEventName = EventSubagentStart
	if got, err := SubagentStart(context.Background(), h.env, main); err != nil || got != "" {
		t.Errorf("SubagentStart in the main checkout = %q, %v; want nothing", got, err)
	}
}
