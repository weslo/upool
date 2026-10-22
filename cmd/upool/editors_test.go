package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of Editors at release and take (docs/design.md, "One binary, a dozen commands, one
// JSON file per pool", the `release` row; "Leases that survive crashes and never reclaim a live
// tree"): release saves and closes the Editor upool opened in the slot before it judges the slot,
// and take skips a slot whose Editor is running.

// slot1Editor is a ledger entry for an Editor in slot-1 of p under closedPID, the PID
// testdata/captures/unity/close-gui.json reports.
func slot1Editor(p harnessPool, openedByUpool bool) ledger.Editor {
	return ledger.Editor{Project: p.slot("slot-1"), PID: closedPID, Mode: modeGUI,
		OpenedByUpool: openedByUpool, ChargeGB: 2.5}
}

// unityArgs are the arguments of u's calls, in order.
func unityArgs(u *fake.Unity) [][]string {
	var args [][]string
	for _, c := range u.Calls() {
		args = append(args, c.Args)
	}
	return args
}

// The acceptance test of #92, release: with the Editor upool opened in slot-1 running, release
// saves and closes it first, then verifies the slot and resets it between the cache key
// comparison's two calls. The fake's log is save_all, close, projects verify, then cache key twice
// around the reset; the Editor's process is gone once the close returns, and the ledger is empty.
func TestReleaseClosesItsEditor(t *testing.T) {
	p := takenPool(t)
	u := p.installUnity(t, saveAllReply, closeReply, p.verifyReply("projects-verify-pass.json", 0),
		fake.Reply{Capture: "cache-key.json"}, fake.Reply{Capture: "cache-key.json"})
	writeLedger(t, p, slot1Editor(p, true))
	// Running when release looks, gone once `unity close` has returned.
	procs := &dyingStart{editorProcs: procsAlive(), pid: closedPID, starts: 1}
	r := runProcs(t, p.Env, p.Repo, procs, func() time.Time { return clock }, "release",
		"feature-x")
	want := "saved and closed slot-1's Editor, process 10648\nreleased slot-1: ready, detached " +
		"at " + testenv.Base + "\n"
	if r.code != output.ExitOK || r.stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr, want)
	}
	slot := p.slot("slot-1")
	wantCalls := [][]string{{"command", "save_all", "--format", "json"},
		{"close", slot, "--format", "json"},
		{"projects", "verify", slot, "--strict", "--format", "json"},
		{"cache", "key", slot, "--format", "json"}, {"cache", "key", slot, "--format", "json"}}
	if got := unityArgs(u); !slices.EqualFunc(got, wantCalls, slices.Equal) {
		t.Errorf("unity calls = %q, want save_all, close, projects verify, then cache key "+
			"twice around the reset", got)
	}
	p.checkReleased(t, pool.StateReady)
	checkEditors(t, p)
}

// An Editor upool did not open fails release, which closes only Editors the tool opened: no unity
// call, the slot still leased, the ledger as it was.
func TestReleaseRefusesAnEditorUpoolDidNotOpen(t *testing.T) {
	p := takenPool(t)
	u := p.installUnity(t)
	e := slot1Editor(p, false)
	writeLedger(t, p, e)
	r := runProcs(t, p.Env, p.Repo, procsAlive(closedPID), func() time.Time { return clock },
		"release", "feature-x")
	want := "upool: slot-1 has an Editor upool did not open, process 10648, which release " +
		"does not close: close it, then release the slot\n"
	if r.code != output.ExitFailure || r.stderr != want {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if calls := u.Calls(); len(calls) != 0 {
		t.Errorf("unity calls = %+v, want none", calls)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want it still leased", s.State, s.Lease)
	}
	checkEditors(t, p, e)
}

// A save that fails stops release before anything else: the Editor is neither closed nor charged
// off, the slot stays leased, and its work is not reset away.
func TestReleaseStopsWhenItsEditorCannotSave(t *testing.T) {
	p := takenPool(t)
	u := p.installUnity(t, fake.Reply{Capture: "command-eval-no-pipeline-instance.json",
		Vars: spikeVars, Exit: 6})
	e := slot1Editor(p, true)
	writeLedger(t, p, e)
	r := runProcs(t, p.Env, p.Repo, procsAlive(closedPID), func() time.Time { return clock },
		"release", "feature-x")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, "releasing slot-1: not "+
		"closing the Editor of "+p.slot("slot-1")+", since saving its scenes failed") {
		t.Errorf("exit %d, stderr %q; want 1 and the failed save", r.code, r.stderr)
	}
	if got := unityArgs(u); len(got) != 1 || got[0][0] != "command" {
		t.Errorf("unity calls = %q, want save_all alone", got)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased {
		t.Errorf("slot-1 = %s, want it still leased", s.State)
	}
	checkEditors(t, p, e)
}

// An Editor whose process is gone is no Editor to close: release goes ahead as without one.
func TestReleaseIgnoresAnEditorThatHasExited(t *testing.T) {
	p := takenPool(t)
	u := p.installUnity(t, p.verifyReply("projects-verify-pass.json", 0),
		fake.Reply{Capture: "cache-key.json"}, fake.Reply{Capture: "cache-key.json"})
	writeLedger(t, p, slot1Editor(p, true))
	r := runProcs(t, p.Env, p.Repo, procsAlive(), func() time.Time { return clock }, "release",
		"feature-x", "--json")
	env := decode(t, r.stdout)
	if data, _ := env.Data.(map[string]any); r.code != output.ExitOK || data["editor"] != nil {
		t.Errorf("exit %d, data %+v; want 0 and no editor", r.code, env.Data)
	}
	if got := unityArgs(u); len(got) != 3 || got[0][0] != "projects" {
		t.Errorf("unity calls = %q, want verify and the cache keys alone", got)
	}
	p.checkReleased(t, pool.StateReady)
}

// The acceptance test of #92, take: a ready slot whose Editor is running is skipped as
// editor-running, so take hands out slot-2; with slot-2 then leased and no clone allowed, take
// fails and names slot-1's reason.
func TestTakeSkipsEditorRunning(t *testing.T) {
	p := newHarnessPool(t)
	writeLedger(t, p, slot1Editor(p, true))
	procs := procsAlive(closedPID)
	now := func() time.Time { return clock }
	r := runProcs(t, p.Env, p.Repo, procs, now, "take", "feature-a", "--no-create")
	if r.code != output.ExitOK || r.stdout != p.slot("slot-2")+"\n" {
		t.Errorf("take: exit %d, stdout %q, stderr %q; want slot-2", r.code, r.stdout, r.stderr)
	}
	r = runProcs(t, p.Env, p.Repo, procs, now, "take", "feature-b", "--no-create")
	if r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, "upool: skipping slot-1: editor-running\n") {
		t.Errorf("second take: exit %d, stderr %q; want 1 and slot-1 skipped as "+
			"editor-running", r.code, r.stderr)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want it ready and unleased", s.State, s.Lease)
	}

	// Once its Editor's process is gone, the slot is handed out again.
	r = runProcs(t, p.Env, p.Repo, procsAlive(), now, "take", "feature-b", "--no-create")
	if r.code != output.ExitOK || r.stdout != p.slot("slot-1")+"\n" {
		t.Errorf("take after the Editor exited: exit %d, stdout %q, stderr %q; want slot-1",
			r.code, r.stdout, r.stderr)
	}
}
