package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The dead-holder reclaim as take and doctor run it (#61). The holder is this test process at a
// start time it did not start at, which internal/proc reads as dead: macOS only, as start times
// are.

// deadHolderPool is a harness pool whose slot-1 was taken on feature-x by a holder now dead, and
// whose slot-2 is leased by a bare take, which has no holder PID.
func deadHolderPool(t *testing.T) harnessPool {
	t.Helper()
	p := newHarnessPool(t)
	for _, name := range []string{"feature-x", "feature-y"} {
		if r := runIn(t, p.Env, p.Repo, "take", name); r.code != output.ExitOK {
			t.Fatalf("take %s: exit %d, stderr %q", name, r.code, r.stderr)
		}
	}
	pid := os.Getpid()
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].Lease.HolderPID, pl.Slots[0].Lease.HolderPIDStart = &pid, &clock
	})
	return p
}

// take reclaims the dead holder's clean slot, says so, and hands it out; the bare take's lease
// stays.
func TestTakeReclaimsADeadHoldersSlot(t *testing.T) {
	p := deadHolderPool(t)
	r := runIn(t, p.Env, p.Repo, "take", "feature-z")
	want := "upool: reclaimed slot-1, since its holder, process " + strconv.Itoa(os.Getpid()) +
		", is gone\n"
	if r.code != output.ExitOK || r.stdout != p.slot("slot-1")+"\n" || r.stderr != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0, slot-1, and %q", r.code, r.stdout,
			r.stderr, want)
	}
	if got := p.inSlot("symbolic-ref", "--short", "HEAD"); got != "feature-z" {
		t.Errorf("slot-1 HEAD = %s, want feature-z", got)
	}
	if got := p.Git("branch", "--list", "feature-x"); got == "" {
		t.Error("feature-x is gone, want the dead holder's branch kept")
	}
	if s := p.loadPool(t).Slots[1]; s.State != pool.StateLeased || s.Lease == nil {
		t.Errorf("slot-2 = %s, lease %+v; want the bare take's lease kept", s.State, s.Lease)
	}
}

// A dead holder's dirty slot is parked, not handed out, and take says why.
func TestTakeParksADeadHoldersDirtySlot(t *testing.T) {
	p := deadHolderPool(t)
	p.WriteFile(".claude/worktrees/slot-1/spike-reuse.txt", "left behind\n")
	r := runIn(t, p.Env, p.Repo, "take", "feature-z")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, "upool: parked slot-1, since "+
		"its holder, process "+strconv.Itoa(os.Getpid())+", is gone and it is dirty: "+
		"?? spike-reuse.txt\n") || !strings.Contains(r.stderr, "skipping slot-1: parked") {
		t.Errorf("exit %d, stderr %q; want 1, slot-1 parked and then skipped", r.code, r.stderr)
	}
}

// doctor and status only report a dead holder's lease; doctor --fix reclaims it.
func TestDoctorReportsAndFixReclaimsADeadHolder(t *testing.T) {
	p := deadHolderPool(t)
	before := p.stateFile(t)
	if r := runIn(t, p.Env, p.Repo, "status"); r.code != output.ExitOK {
		t.Errorf("status: exit %d, stderr %q", r.code, r.stderr)
	}
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	c := findCheck(t, doctorChecks(t, r), "slot-1")
	want := "leased by process " + strconv.Itoa(os.Getpid()) + ", which is gone; upool doctor " +
		"--fix or the next upool take reclaims it"
	if r.code != output.ExitOK || c.Result != checkWarn || c.Reason != want {
		t.Errorf("doctor: exit %d, slot-1 %s %q; want 0 and warn %q", r.code, c.Result, c.Reason,
			want)
	}
	if after := p.stateFile(t); string(after) != string(before) {
		t.Errorf("status and doctor changed state.json:\n%s\nwant\n%s", after, before)
	}

	r = runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
	c = findCheck(t, doctorChecks(t, r), "slot-1")
	want = "fixed: reset it to ready, since its holder, process " + strconv.Itoa(os.Getpid()) +
		", was gone"
	if r.code != output.ExitOK || c.Result != checkPass || c.Reason != want {
		t.Errorf("doctor --fix: exit %d, slot-1 %s %q; want 0 and %q", r.code, c.Result,
			c.Reason, want)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want ready with none", s.State, s.Lease)
	}
	if s := p.loadPool(t).Slots[1]; s.State != pool.StateLeased {
		t.Errorf("slot-2 = %s, want the bare take's lease kept", s.State)
	}
}
