package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// writeDesktopRecord writes the desktop app's record of its sessions' worktrees under home, naming
// each of slots, by path, as a session's worktree; with none, the record names no worktree.
func writeDesktopRecord(t *testing.T, home string, slots ...string) {
	t.Helper()
	worktrees := map[string]any{}
	for i, path := range slots {
		name := fmt.Sprintf("gate-%d", i)
		worktrees[name] = map[string]any{"name": name, "path": path,
			"leasedBy": "local_3abad311-c5b5-4182-8699-21309c37e642"}
	}
	b, err := json.Marshal(map[string]any{"worktrees": worktrees})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, filepath.FromSlash(desktop.RecordFile))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hookLogReport returns the reasons of the hook-log lines a doctor run in p reports, in order.
func hookLogReport(t *testing.T, p harnessPool) []string {
	t.Helper()
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	var got []string
	for _, c := range doctorChecks(t, r) {
		if c.Name == checkHookLog {
			got = append(got, string(c.Result)+" "+c.Reason)
		}
	}
	return got
}

// doctor reports, from the pool's hook log, the last WorktreeRemove of each slot the desktop app's
// record no longer names: what it did, that it failed or never ended, or that none came since a
// desktop session was handed the slot, a warning while that session's lease is still on it
// (#278).
func TestDoctorReportsTheLastRemoveOfEachSlotTheAppNoLongerNames(t *testing.T) {
	p := newHarnessPool(t)
	hook := func(event, capture string, set map[string]any) {
		t.Helper()
		if code, _, errOut := runHook(t, p.Env, event, captureStdin(t, p.Env, capture,
			set)); code != output.ExitOK {
			t.Fatalf("hook %s on %s: exit %d, stderr %q", event, capture, code, errOut)
		}
	}
	at := logTime(clock)
	check := func(step string, want ...string) {
		t.Helper()
		if got := hookLogReport(t, p); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: hook-log lines =\n%s\nwant\n%s", step, strings.Join(got, "\n"),
				strings.Join(want, "\n"))
		}
	}
	// Two desktop sessions are handed slot-1 and slot-2, the first starts, confirming its lease as
	// its SessionStart would, and is archived, its slot marked needs-reset; the app records
	// neither any longer.
	hook("worktree-create", "desktop-session-create.json", nil)
	hook("worktree-create", "desktop-session-create-reuse.json", nil)
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].State, pl.Slots[0].Lease.Confirmed = pool.StateLeased, true
	})
	hook("worktree-remove", "desktop-session-remove.json", nil)
	if s := p.loadPool(t).Slots; s[0].State != pool.StateNeedsReset || s[1].Lease == nil {
		t.Fatalf("slots = %+v, want slot-1 needs-reset and slot-2 leased", s)
	}
	writeDesktopRecord(t, p.Home)
	slot2NoRemove := "warn slot-2: the desktop app no longer records it, and no WorktreeRemove " +
		"is logged since a desktop session was handed it at " + at + ": the app ran no remove " +
		"hook for that session, or one that never reached upool's own code, and its lease is " +
		"still on the slot"
	slot1Reset := "pass slot-1: its last WorktreeRemove ran at " + at + ", with outcome needs-reset"
	check("both slots let go", slot1Reset, slot2NoRemove)

	writeDesktopRecord(t, p.Home, p.slot("slot-2"))
	check("slot-2 recorded", slot1Reset)

	if err := os.Remove(filepath.Join(p.Home, filepath.FromSlash(desktop.RecordFile))); err != nil {
		t.Fatal(err)
	}
	check("no record")

	writeDesktopRecord(t, p.Home)
	p.update(t, func(pl *pool.Pool) { pl.Slots[1].State, pl.Slots[1].Lease = pool.StateReady, nil })
	check("slot-2's lease ended", slot1Reset, "pass slot-2: no WorktreeRemove is logged since a "+
		"desktop session was handed it at "+at+"; that session's lease has ended since")

	hook("worktree-remove", "desktop-session-remove.json", nil)
	req, err := hooks.Decode(strings.NewReader(captureStdin(t, p.Env,
		"desktop-session-remove.json", map[string]any{"worktree_path": p.slot("slot-2")})))
	if err != nil {
		t.Fatal(err)
	}
	hooks.StartLog(hooks.Env{Home: p.Home, Now: func() time.Time { return clock },
		Stderr: os.Stderr}, hooks.EventWorktreeRemove, 4242, req)
	check("a failed remove and one that never ended",
		"warn slot-1: its last WorktreeRemove ran at "+at+", with outcome failed: slot-1 is "+
			"needs-reset, with no lease to end",
		"warn slot-2: its last WorktreeRemove started at "+at+" and logged no end: it was "+
			"killed, or is still running")
}
