package pool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
)

// The tests of the create hook's take and the expiry of its leases (#75), against a state.json
// under a temporary home, with an injected clock: start is when a lease is taken, and timeout the
// default confirm_timeout.

var start = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const timeout = 60 * time.Second

// hookTake is the create hook's take at now, for the desktop session in the captures.
func hookTake(now time.Time) UnconfirmedTake {
	return UnconfirmedTake{Holder: "49b55ea5-fc66-40a8-8f6c-b4afc31ec456",
		NameHint: "system-diagnostics-9f8a09", Now: now, ConfirmTimeout: timeout}
}

// poolOf saves a pool whose slots are in states, slot-1 onwards, and returns the home holding it.
func poolOf(t *testing.T, states ...State) string {
	t.Helper()
	home := tempDir(t)
	p := Pool{Repo: "/src/down", Base: "main"}
	for i, st := range states {
		s := Slot{Name: "slot-" + string(rune('1'+i)), Path: "/src/down/.claude/worktrees/slot-" +
			string(rune('1'+i)), State: st, LastUsed: start.Add(-time.Hour)}
		if st == StateLeased {
			s.Lease = &Lease{LeaseID: "held", Holder: "terminal", WorktreePath: s.Path,
				AcquiredAt: start.Add(-time.Hour), Confirmed: true, Lane: "script"}
		}
		p.Slots = append(p.Slots, s)
	}
	save(t, home, p)
	return home
}

// take runs the create hook's take at now on the pool in home, and fails the test on an error.
func take(t *testing.T, home string, now time.Time) Slot {
	t.Helper()
	s, _, err := hookTake(now).Take(context.Background(), home, key)
	if err != nil {
		t.Fatalf("Take = %v, want a slot", err)
	}
	return s
}

// checkUnconfirmed fails the test unless s is leased unconfirmed as the hook's take leases it:
// no holder PID, confirmed false, the session as holder and its name as the hint, and
// reset_pending as given.
func checkUnconfirmed(t *testing.T, s Slot, name string, resetPending bool) {
	t.Helper()
	l := s.Lease
	if s.Name != name || s.State != StateLeasedUnconfirmed || l == nil {
		t.Fatalf("took %s, %s, lease %+v; want %s leased unconfirmed", s.Name, s.State, l, name)
	}
	if l.Confirmed || l.HolderPID != nil || l.HolderPIDStart != nil ||
		l.Holder != "49b55ea5-fc66-40a8-8f6c-b4afc31ec456" ||
		l.NameHint != "system-diagnostics-9f8a09" || l.WorktreePath != s.Path ||
		!l.AcquiredAt.Equal(start) || l.LeaseID == "" || l.ResetPending != resetPending {
		t.Errorf("lease = %+v; want an unconfirmed lease with no holder PID, reset_pending %t",
			*l, resetPending)
	}
}

// The hand-out order puts a ready slot first, ahead of a needs-reset one earlier in the pool (A5),
// and the lease is written to state.json as the take returns it.
func TestUnconfirmedPrefersReady(t *testing.T) {
	home := poolOf(t, StateNeedsReset, StateReady)
	s := take(t, home, start)
	checkUnconfirmed(t, s, "slot-2", false)
	p := load(t, home)
	if p.Slots[0].State != StateNeedsReset || p.Slots[1].Lease == nil ||
		p.Slots[1].Lease.LeaseID != s.Lease.LeaseID {
		t.Errorf("slots = %+v, want slot-1 still needs-reset and slot-2's lease written", p.Slots)
	}
}

// With no slot ready, a needs-reset slot is handed out (transition 10), its lease marked
// reset_pending for the deferred reset (A5; A23).
func TestUnconfirmedFallsBackToNeedsReset(t *testing.T) {
	home := poolOf(t, StateLeased, StateParked, StateNeedsReset)
	checkUnconfirmed(t, take(t, home, start), "slot-3", true)
	if s := load(t, home).Slots[2]; s.Lease == nil || !s.Lease.ResetPending {
		t.Errorf("slot-3 = %+v, want reset_pending written", s)
	}
}

// A slot Present rejects is passed over, ready or needs-reset, for the next it accepts, and with
// none accepted the take fails with ErrNoSlot.
func TestUnconfirmedPassesOverWhatPresentRejects(t *testing.T) {
	home := poolOf(t, StateReady, StateNeedsReset, StateReady)
	gone := map[string]bool{"slot-1": true}
	tk := hookTake(start)
	tk.Present = func(s Slot) bool { return !gone[s.Name] }
	s, _, err := tk.Take(context.Background(), home, key)
	if err != nil {
		t.Fatal(err)
	}
	checkUnconfirmed(t, s, "slot-3", false)
	gone["slot-3"] = true
	gone["slot-2"] = true
	if s, _, err := tk.Take(context.Background(), home, key); !errors.Is(err, ErrNoSlot) {
		t.Errorf("Take = %+v, %v; want ErrNoSlot with every free slot rejected", s, err)
	}
}

// With no slot ready or needs-reset the take fails with ErrNoSlot and changes nothing: it never
// clones one.
func TestUnconfirmedNeverClones(t *testing.T) {
	home := poolOf(t, StateLeased, StateParked, StateCreating, StateWarming)
	before := load(t, home)
	if s, _, err := hookTake(start).Take(context.Background(), home, key); !errors.Is(err,
		ErrNoSlot) {
		t.Fatalf("Take = %+v, %v; want ErrNoSlot", s, err)
	}
	if after := load(t, home); len(after.Slots) != len(before.Slots) {
		t.Errorf("slots = %+v, want the %d there were", after.Slots, len(before.Slots))
	}
	for i, s := range load(t, home).Slots {
		if s.State != before.Slots[i].State {
			t.Errorf("%s = %s, want %s", s.Name, s.State, before.Slots[i].State)
		}
	}
}

// An unconfirmed lease lives for confirm_timeout and no longer: up to then it holds the slot, and
// after it the slot is ready again and the next take can have it (transition 7).
func TestUnconfirmedExpiresAfterConfirmTimeout(t *testing.T) {
	home := poolOf(t, StateReady)
	first := take(t, home, start)

	p := load(t, home)
	if expired, err := p.ExpireUnconfirmed(start.Add(timeout), timeout, false); err != nil ||
		len(expired) != 0 || p.Slots[0].State != StateLeasedUnconfirmed {
		t.Errorf("at confirm_timeout: expired %+v, %v, slot %s; want nothing expired yet",
			expired, err, p.Slots[0].State)
	}
	if _, _, err := hookTake(start.Add(timeout)).Take(context.Background(), home,
		key); !errors.Is(err, ErrNoSlot) {
		t.Errorf("a second take at confirm_timeout = %v, want ErrNoSlot", err)
	}

	later := start.Add(timeout + time.Second)
	expired, err := p.ExpireUnconfirmed(later, timeout, false)
	if err != nil || len(expired) != 1 || expired[0].Lease.LeaseID != first.Lease.LeaseID {
		t.Fatalf("after confirm_timeout: expired %+v, %v; want slot-1's lease", expired, err)
	}
	if s := p.Slots[0]; s.State != StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want ready with no lease", s.State, s.Lease)
	}

	second, expired, err := hookTake(later).Take(context.Background(), home, key)
	if err != nil || len(expired) != 1 || second.Name != "slot-1" ||
		second.Lease.LeaseID == first.Lease.LeaseID || !second.Lease.AcquiredAt.Equal(later) {
		t.Errorf("the take after it = %+v, expired %+v, %v; want slot-1 under a new lease",
			second, expired, err)
	}
}

// A lease on a slot handed out from needs-reset expires back to needs-reset, since its deferred
// reset never ran (A23), not to ready as transition 7's other case does.
func TestExpiredNeedsResetLeaseReturnsToNeedsReset(t *testing.T) {
	home := poolOf(t, StateNeedsReset)
	take(t, home, start)
	p := load(t, home)
	expired, err := p.ExpireUnconfirmed(start.Add(timeout+time.Second), timeout, false)
	if err != nil || len(expired) != 1 {
		t.Fatalf("expired %+v, %v; want slot-1's lease", expired, err)
	}
	if s := p.Slots[0]; s.State != StateNeedsReset || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want needs-reset with no lease", s.State, s.Lease)
	}
}

// Expiry leaves every other lease alone: a confirmed one however old, a confirmed create-hook
// lease, and an unconfirmed one still within confirm_timeout.
func TestUnconfirmedExpiryLeavesOtherLeasesAlone(t *testing.T) {
	now := start.Add(time.Hour)
	young := Slot{Name: "slot-3", State: StateLeasedUnconfirmed,
		Lease: &Lease{LeaseID: "young", AcquiredAt: now.Add(-timeout / 2)}}
	p := Pool{Slots: []Slot{
		{Name: "slot-1", State: StateLeased, Lease: &Lease{LeaseID: "old", AcquiredAt: start,
			Confirmed: true}},
		{Name: "slot-2", State: StateLeased, Lease: &Lease{LeaseID: "hook", AcquiredAt: start,
			Confirmed: true, ResetPending: true}},
		young,
	}}
	expired, err := p.ExpireUnconfirmed(now, timeout, false)
	if err != nil || len(expired) != 0 {
		t.Errorf("expired %+v, %v; want none", expired, err)
	}
	for _, s := range p.Slots {
		if s.Lease == nil {
			t.Errorf("%s lost its lease", s.Name)
		}
	}
}

// While the desktop app's record cannot be read, each lease that expires unconfirmed counts on its
// slot, and the second in a row quarantines it: the take passes it over for the next slot, saying
// so when it ends the lease, and with no other slot hands out nothing (A50; #192, where the app
// refused one slot's path each time). With the record readable, an expiry counts for nothing: the
// record keeps a refused slot from being handed out, and an expiry may have another cause.
func TestUnconfirmedExpiriesQuarantineASlot(t *testing.T) {
	for _, count := range []bool{true, false} {
		t.Run(fmt.Sprintf("counted %t", count), func(t *testing.T) {
			home := poolOf(t, StateReady, StateReady)
			take(t, home, start)
			at := start
			for n := 1; n <= QuarantineAfter; n++ {
				at = at.Add(timeout + time.Second)
				tk := hookTake(at)
				tk.CountExpiries = count
				s, expired, err := tk.Take(context.Background(), home, key)
				if err != nil || len(expired) != 1 || expired[0].Name != "slot-1" {
					t.Fatalf("take after expiry %d = %+v, %+v, %v; want slot-1's lease expired",
						n, s, expired, err)
				}
				lines := EndExpired(context.Background(), git.Client{Runner: noLocks{}},
					"/src/down", expired, timeout)
				quarantined := strings.Contains(lines[0], ", but quarantined: 2 sessions in a "+
					"row were handed slot-1 and none confirmed it, while the desktop app's "+
					"record could not be read")
				if want := count && n == QuarantineAfter; quarantined != want {
					t.Errorf("expiry %d says %q; want quarantined: %t", n, lines[0], want)
				}
				want := "slot-1"
				if count && n == QuarantineAfter {
					want = "slot-2"
				}
				if s.Name != want {
					t.Errorf("take after expiry %d = %s, want %s", n, s.Name, want)
				}
			}
			got := load(t, home).Slots[0]
			want := map[bool]int{true: QuarantineAfter, false: 0}[count]
			if got.UnconfirmedExpiries != want || got.Quarantined() != count {
				t.Errorf("slot-1 = %+v; want %d expiries counted, quarantined %t", got, want,
					count)
			}
			if !count {
				return
			}
			_, _, err := hookTake(at).Take(context.Background(), home, key)
			if !errors.Is(err, ErrNoSlot) {
				t.Errorf("a take with slot-2 leased and slot-1 quarantined = %v, want ErrNoSlot",
					err)
			}
		})
	}
}

// noLocks is a git runner for EndExpired that lists only the main checkout, so finds no slot
// locked.
type noLocks struct{}

func (noLocks) Run(context.Context, string, []string, ...string) (git.Result, error) {
	return git.Result{Stdout: []byte("worktree /src/down\nHEAD " +
		"0000000000000000000000000000000000000000\nbranch refs/heads/main\n\n")}, nil
}
