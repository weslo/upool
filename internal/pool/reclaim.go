package pool

import (
	"context"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/proc"
)

// Reclaimer reclaims the leases of one pool whose holder process died: a clean slot gets the
// release reset and goes back to ready, and a dirty one is parked ((f) transitions 13 and 14;
// docs/design.md, "Leases that survive crashes and never reclaim a live tree": reclaim happens
// only when the holder PID is dead and the ledger shows no live Editor in the slot). take reclaims
// before it chooses a slot, and `doctor --fix` reclaims; status and doctor without --fix only
// report.
type Reclaimer struct {
	Git git.Client
	// Proc says whether a process is alive, at its recorded start time for a holder, so that a
	// reused PID reads as dead (internal/proc).
	Proc proc.Checker
	// Home holds ~/.upool, and Key names the pool's ~/.upool/pools/<key>/state.json.
	Home, Key string
	// Now stamps last_used on a slot reclaimed clean.
	Now func() time.Time
	// Held says whether the desktop app's record still names a slot, and how: the session and
	// the worktree, as desktop.Worktree says them. A dead holder's lease on such a slot is kept: the app keeps a session's record through
	// a quit or a crash and resumes the session into the slot, and refuses the path to any other
	// (A50; #192). Nil holds none.
	Held func(Slot) (string, bool)
}

// Kept is a lease whose holder is dead that Reclaim leaves, since the app still records its slot.
type Kept struct {
	Slot string
	// HolderPID is the dead holder's PID, and App how the app records the slot: the session and
	// its worktree.
	HolderPID int
	App       string
}

// Reclaimed is one lease Reclaim dealt with.
type Reclaimed struct {
	Slot string
	// HolderPID is the dead holder's PID.
	HolderPID int
	// State is ready for a slot reset clean, or parked for a dirty one; empty when Err is set.
	State State
	// Dirty is why a parked slot is dirty.
	Dirty []string
	// Err is why the slot could not be reclaimed; the others are reclaimed regardless.
	Err error
}

// Candidates returns the slots of p that Reclaim would reclaim, changing nothing: leased, with a
// holder PID that Proc says is dead at its recorded start time, no live Editor in the slot in
// ~/.upool/editors.json, and no record of it in the desktop app (Held; A50). A lease with no holder
// PID, a bare take's, is never reclaimed (A6), and nor is one whose holder's start time was not
// recorded, since it cannot be shown dead.
func (r Reclaimer) Candidates(p Pool) ([]Slot, error) {
	dead, err := r.dead(p)
	if err != nil {
		return nil, err
	}
	var candidates []Slot
	for _, s := range dead {
		if _, held := r.held(s); !held {
			candidates = append(candidates, s)
		}
	}
	return candidates, nil
}

// Kept returns the leases of p that would be candidates but that the desktop app still records,
// which Reclaim leaves (A50), changing nothing.
func (r Reclaimer) Kept(p Pool) ([]Kept, error) {
	dead, err := r.dead(p)
	if err != nil {
		return nil, err
	}
	var kept []Kept
	for _, s := range dead {
		if name, held := r.held(s); held {
			kept = append(kept, Kept{Slot: s.Name, HolderPID: *s.Lease.HolderPID, App: name})
		}
	}
	return kept, nil
}

// held is Held, or nothing held when it is nil.
func (r Reclaimer) held(s Slot) (string, bool) {
	if r.Held == nil {
		return "", false
	}
	return r.Held(s)
}

// dead returns the slots of p leased to a holder that Proc says is dead at its recorded start
// time, with no live Editor in the slot in ~/.upool/editors.json.
func (r Reclaimer) dead(p Pool) ([]Slot, error) {
	var leased []Slot
	for _, s := range p.Slots {
		if s.State == StateLeased && s.Lease != nil && s.Lease.HolderPID != nil &&
			s.Lease.HolderPIDStart != nil {
			leased = append(leased, s)
		}
	}
	if len(leased) == 0 {
		return nil, nil
	}
	editors, err := ledger.Load(r.Home)
	if err != nil {
		return nil, err
	}
	var dead []Slot
	for _, s := range leased {
		alive, err := r.Proc.Alive(*s.Lease.HolderPID, *s.Lease.HolderPIDStart)
		if err != nil {
			return nil, fmt.Errorf("checking %s's holder, process %d: %w", s.Name,
				*s.Lease.HolderPID, err)
		}
		if alive {
			continue
		}
		live, err := editors.LiveIn(r.Proc, s.Path)
		if err != nil {
			return nil, err
		}
		if !live {
			dead = append(dead, s)
		}
	}
	return dead, nil
}

// Reclaim reclaims every candidate of the pool. Each slot's define-order noise is reverted, then a
// clean slot is reset to base, a slot an earlier upool locked is unlocked (Unlock), and state.json
// is written under the lock, only if the lease is still the one Reclaim read. A slot that cannot
// be reclaimed is reported in its Reclaimed's Err and left as it is; only a failure to read the
// pool or the ledger is Reclaim's own error.
func (r Reclaimer) Reclaim(ctx context.Context) ([]Reclaimed, error) {
	p, err := Load(r.Home, r.Key)
	if err != nil {
		return nil, err
	}
	dead, err := r.Candidates(p)
	if err != nil {
		return nil, err
	}
	done := make([]Reclaimed, 0, len(dead))
	for _, s := range dead {
		rc := Reclaimed{Slot: s.Name, HolderPID: *s.Lease.HolderPID}
		rc.State, rc.Dirty, rc.Err = r.reclaim(ctx, p, s)
		done = append(done, rc)
	}
	return done, nil
}

// reclaim reclaims the slot s of p, whose holder is dead.
func (r Reclaimer) reclaim(ctx context.Context, p Pool, s Slot) (State, []string, error) {
	if info, err := os.Stat(s.Path); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("%s is not on disk", s.Path)
	}
	if _, err := RevertNoise(ctx, r.Git, s.Path); err != nil {
		return "", nil, err
	}
	dirty, err := Dirty(ctx, r.Git, s.Path)
	if err != nil {
		return "", nil, err
	}
	clean := len(dirty) == 0
	if clean {
		if err := ResetToBase(ctx, r.Git, s.Path, p.Base); err != nil {
			return "", nil, err
		}
	}
	if err := Unlock(ctx, r.Git, p.Repo, s.Path); err != nil {
		return "", nil, err
	}
	var state State
	err = Update(ctx, r.Home, r.Key, func(cur *Pool) error {
		i := slices.IndexFunc(cur.Slots, func(c Slot) bool { return c.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		c := &cur.Slots[i]
		if c.Lease == nil || c.Lease.LeaseID != s.Lease.LeaseID {
			return fmt.Errorf("%s's lease changed in state.json meanwhile", s.Name)
		}
		next, err := Next(*c, Reclaim{HolderAlive: false, EditorLive: false, Clean: clean})
		if err != nil {
			return err
		}
		c.State, c.Lease, state = next, nil, next
		if clean {
			c.LastUsed = r.Now().UTC()
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if clean {
		dirty = nil
	}
	return state, dirty, nil
}
