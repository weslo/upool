package main

import (
	"os"
	"strconv"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// A lease whose holder process is alive is refused without --force or its own --if-lease-id; one
// whose holder is dead is released (docs/design.md, "Leases that survive crashes and never reclaim
// a live tree"; A6). The holder is this test process, alive at its real start time and dead at any
// other, which is how a reused PID reads (internal/proc). macOS only, as start times are.
func TestReleaseRefusesLiveHolderWithoutForce(t *testing.T) {
	pid := os.Getpid()
	start, err := proc.Start(pid)
	if err != nil {
		t.Fatal(err)
	}
	holdBy := func(t *testing.T, p harnessPool, at *pool.Lease) string {
		t.Helper()
		var id string
		p.update(t, func(pl *pool.Pool) {
			l := pl.Slots[0].Lease
			l.HolderPID, l.HolderPIDStart = at.HolderPID, at.HolderPIDStart
			id = l.LeaseID
		})
		return id
	}
	live := &pool.Lease{HolderPID: &pid, HolderPIDStart: &start}

	p := takenPool(t)
	id := holdBy(t, p, live)
	r := runIn(t, p.Env, p.Repo, "release", "feature-x")
	want := "upool: slot-1 is held by process " + strconv.Itoa(pid) + ", which is alive; pass " +
		"--if-lease-id " + id + " or --force to release it anyway\n"
	if r.code != output.ExitFailure || r.stderr != want {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want it still leased", s.State, s.Lease)
	}
	if r := runIn(t, p.Env, p.Repo, "release", "feature-x", "--force"); r.code != output.ExitOK {
		t.Errorf("--force: exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	p.checkReleased(t, pool.StateReady)

	p = takenPool(t)
	id = holdBy(t, p, live)
	r = runIn(t, p.Env, p.Repo, "release", "feature-x", "--if-lease-id", id)
	if r.code != output.ExitOK {
		t.Errorf("--if-lease-id: exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	p.checkReleased(t, pool.StateReady)

	p = takenPool(t)
	holdBy(t, p, &pool.Lease{HolderPID: &pid, HolderPIDStart: &clock}) // a dead holder
	if r := runIn(t, p.Env, p.Repo, "release", "feature-x"); r.code != output.ExitOK {
		t.Errorf("a dead holder: exit %d, stderr %q; want 0", r.code, r.stderr)
	}
	p.checkReleased(t, pool.StateReady)
}
