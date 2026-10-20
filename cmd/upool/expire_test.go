package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The tests of the expiry of unconfirmed leases in take, status and doctor (#75), on the harness
// pool, whose clock is the tests' fixed clock.

// leaseUnconfirmed gives p's slot-1 an unconfirmed create-hook lease taken age before the clock,
// on a slot handed out from needs-reset when resetPending is set.
func (p harnessPool) leaseUnconfirmed(t *testing.T, age time.Duration, resetPending bool) {
	t.Helper()
	p.update(t, func(pl *pool.Pool) {
		s := &pl.Slots[0]
		s.State = pool.StateLeasedUnconfirmed
		s.Lease = &pool.Lease{LeaseID: "unconfirmed",
			Holder: "49b55ea5-fc66-40a8-8f6c-b4afc31ec456", WorktreePath: s.Path, AcquiredAt: clock.Add(-age), ResetPending: resetPending,
			Lane: "script", NameHint: "system-diagnostics-9f8a09"}
	})
}

// slot1 is p's slot-1 as state.json has it.
func (p harnessPool) slot1(t *testing.T) pool.Slot {
	t.Helper()
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return pl.Slots[0]
}

// Each command that reads the pool under the lock ends an unconfirmed lease that outlived the
// default confirm_timeout of 60 s, sends its slot back to the state it was handed out from, and
// says so (docs/contracts.md (f) transition 7; A23).
func TestCommandsEndExpiredUnconfirmedLeases(t *testing.T) {
	for _, tt := range []struct {
		args         []string
		resetPending bool
		want         pool.State
	}{
		{[]string{"status"}, false, pool.StateReady},
		{[]string{"status", "--all"}, true, pool.StateNeedsReset},
		{[]string{"doctor"}, false, pool.StateReady},
		{[]string{"doctor"}, true, pool.StateNeedsReset},
	} {
		t.Run(strings.Join(tt.args, " ")+"/"+string(tt.want), func(t *testing.T) {
			p := newHarnessPool(t)
			p.leaseUnconfirmed(t, 2*time.Minute, tt.resetPending)
			r := runIn(t, p.Env, p.Repo, tt.args...)
			if r.code != output.ExitOK {
				t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
			}
			want := "upool: warning: ended the unconfirmed lease on slot-1, which no session " +
				"confirmed within 1m0s: slot-1 is " + string(tt.want) + " again\n"
			if r.stderr != want {
				t.Errorf("stderr = %q, want %q", r.stderr, want)
			}
			if s := p.slot1(t); s.State != tt.want || s.Lease != nil {
				t.Errorf("slot-1 = %s, lease %+v; want %s with no lease", s.State, s.Lease, tt.want)
			}
		})
	}
}

// take ends an expired lease before it chooses a slot, so it can hand that slot out, and says so
// on stderr; the git worktree lock the hook's take held goes with the lease.
func TestTakeEndsAnExpiredLeaseAndTakesItsSlot(t *testing.T) {
	p := newHarnessPool(t)
	p.leaseUnconfirmed(t, 2*time.Minute, false)
	p.Git("worktree", "lock", "--reason", "upool lease unconfirmed", p.slot("slot-1"))
	r := runIn(t, p.Env, p.Repo, "take", "feature")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if !strings.Contains(r.stderr, "upool: ended the unconfirmed lease on slot-1") {
		t.Errorf("stderr = %q, want it to say the lease ended", r.stderr)
	}
	if want := p.slot("slot-1") + "\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q: the slot the expiry freed", r.stdout, want)
	}
	if s := p.slot1(t); s.State != pool.StateLeased || s.Lease == nil || !s.Lease.Confirmed {
		t.Errorf("slot-1 = %+v, want it leased by the take", s)
	}
}

// The git worktree lock on an expired lease's slot is released, so that the next take can lock it.
func TestStatusReleasesAnExpiredLeasesWorktreeLock(t *testing.T) {
	p := newHarnessPool(t)
	p.leaseUnconfirmed(t, 2*time.Minute, false)
	p.Git("worktree", "lock", "--reason", "upool lease unconfirmed", p.slot("slot-1"))
	if r := runIn(t, p.Env, p.Repo, "status"); r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if got := p.Git("worktree", "list", "--porcelain"); strings.Contains(got,
		"locked upool lease unconfirmed") {
		t.Errorf("git worktree list = %q, want slot-1 unlocked", got)
	}
}

// A lease still within confirm_timeout is left alone, and a command that finds nothing to expire
// writes nothing.
func TestCommandsLeaveAYoungUnconfirmedLease(t *testing.T) {
	p := newHarnessPool(t)
	p.leaseUnconfirmed(t, 30*time.Second, false)
	state := filepath.Join(p.Home, ".upool", "pools", p.key, "state.json")
	before, err := os.Stat(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"doctor"}} {
		if r := runIn(t, p.Env, p.Repo, args...); r.code != output.ExitOK || r.stderr != "" {
			t.Errorf("%q: exit = %d, stderr %q; want %d and nothing", args, r.code, r.stderr,
				output.ExitOK)
		}
	}
	if s := p.slot1(t); s.State != pool.StateLeasedUnconfirmed || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want the lease kept", s.State, s.Lease)
	}
	if after, err := os.Stat(state); err != nil || !after.ModTime().Equal(before.ModTime()) ||
		!os.SameFile(before, after) {
		t.Errorf("state.json was rewritten, %v; want it untouched", err)
	}
}

// confirm_timeout comes from configuration, here the environment, as every setting can.
func TestConfirmTimeoutIsASetting(t *testing.T) {
	p := newHarnessPool(t)
	p.leaseUnconfirmed(t, 2*time.Minute, false)
	p.Setenv("UPOOL_CONFIRM_TIMEOUT", "5m")
	if r := runIn(t, p.Env, p.Repo, "status"); r.code != output.ExitOK || r.stderr != "" {
		t.Errorf("exit = %d, stderr %q; want %d and nothing", r.code, r.stderr, output.ExitOK)
	}
	if s := p.slot1(t); s.State != pool.StateLeasedUnconfirmed {
		t.Errorf("slot-1 = %s, want the lease kept under a 5 m confirm_timeout", s.State)
	}
}
