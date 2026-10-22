package pool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/proc"
)

// Recoverer checks again the warming slots of one pool whose owner died: the refresh moving a slot
// to base and re-warming it, or the clone holding it as its seed. Once the ledger shows no live
// Editor in such a slot, a clean slot goes back to ready and a dirty one is parked
// ((f) transition 22; A4); a later refresh moves a slot returned to ready to base. take checks
// again before it chooses a slot, and status and doctor whenever they read the pool (#97).
type Recoverer struct {
	Git git.Client
	// Proc says whether a process is alive, at its recorded start time for an owner, so that a
	// reused PID reads as dead (internal/proc).
	Proc proc.Checker
	// Home holds ~/.upool, and Key names the pool's ~/.upool/pools/<key>/state.json.
	Home, Key string
}

// Recovered is one warming slot Recover dealt with.
type Recovered struct {
	Slot string
	// OwnerPID is the dead owner's PID, or 0 when the slot recorded none.
	OwnerPID int
	// State is ready for a clean slot, or parked for a dirty one; empty when Err is set.
	State State
	// Dirty is why a parked slot is dirty.
	Dirty []string
	// Err is why the slot could not be checked again; the others are checked regardless.
	Err error
}

// Orphans returns the warming slots of p that Recover would check again, changing nothing: their
// owner is dead at its recorded start time, or none is recorded, and the ledger in
// ~/.upool/editors.json shows no live Editor in the slot. Off macOS, where internal/proc reads no
// start times, no owner can be shown dead, and no slot is returned.
func (r Recoverer) Orphans(p Pool) ([]Slot, error) {
	var warming []Slot
	for _, s := range p.Slots {
		if s.State == StateWarming {
			warming = append(warming, s)
		}
	}
	if len(warming) == 0 {
		return nil, nil
	}
	editors, err := ledger.Load(r.Home)
	if err != nil {
		return nil, err
	}
	var orphans []Slot
	for _, s := range warming {
		if s.OwnerPID != nil && s.OwnerPIDStart != nil {
			alive, err := r.Proc.Alive(*s.OwnerPID, *s.OwnerPIDStart)
			if errors.Is(err, errors.ErrUnsupported) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("checking %s's owner, process %d: %w", s.Name,
					*s.OwnerPID, err)
			}
			if alive {
				continue
			}
		}
		live, err := editors.LiveIn(r.Proc, s.Path)
		if err != nil {
			return nil, err
		}
		if !live {
			orphans = append(orphans, s)
		}
	}
	return orphans, nil
}

// Recover checks again every orphan of the pool. Each slot's define-order noise is reverted, its
// cleanness judged as (f) defines it, and state.json written under the lock, only if the slot is
// still warming under the owner Recover read. A slot that cannot be checked is reported in its
// Recovered's Err and left as it is; only a failure to read the pool or the ledger is Recover's
// own error.
func (r Recoverer) Recover(ctx context.Context) ([]Recovered, error) {
	p, err := Load(r.Home, r.Key)
	if err != nil {
		return nil, err
	}
	orphans, err := r.Orphans(p)
	if err != nil {
		return nil, err
	}
	done := make([]Recovered, 0, len(orphans))
	for _, s := range orphans {
		rc := Recovered{Slot: s.Name}
		if s.OwnerPID != nil {
			rc.OwnerPID = *s.OwnerPID
		}
		rc.State, rc.Dirty, rc.Err = r.recover(ctx, s)
		done = append(done, rc)
	}
	return done, nil
}

// recover checks again the warming slot s, whose owner is dead.
func (r Recoverer) recover(ctx context.Context, s Slot) (State, []string, error) {
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
	var state State
	err = Update(ctx, r.Home, r.Key, func(cur *Pool) error {
		i := slices.IndexFunc(cur.Slots, func(c Slot) bool { return c.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		c := &cur.Slots[i]
		if c.State != StateWarming || !sameOwner(c.OwnerPID, s.OwnerPID) {
			return fmt.Errorf("%s changed in state.json meanwhile: it is now %s", s.Name,
				c.State)
		}
		next, err := Next(*c, DeadOwner{EditorLive: false, Clean: len(dirty) == 0})
		if err != nil {
			return err
		}
		c.State, c.OwnerPID, c.OwnerPIDStart, state = next, nil, nil, next
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return state, dirty, nil
}

// sameOwner reports whether two optional owner PIDs are the same.
func sameOwner(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
