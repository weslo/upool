package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// leftoverSlot is a warming slot whose owner died with the re-warm's Editor still live in it,
// which refresh closed and checked again ((f) transition 22; #273).
type leftoverSlot struct {
	Slot string `json:"slot"`
	// PID is the Editor's process, as the ledger recorded it.
	PID int `json:"pid"`
	// State is where the slot went once the Editor was gone: ready when clean, parked when dirty.
	State pool.State `json:"state"`
}

// closeLeftovers closes, in each warming slot of the pool whose owner is dead, the Editor the
// ledger shows live there when it is one upool opened headless: a re-warm's Editor whose close
// failed (A40; #273). The close is a plain one, sent again while it leaves the Editor running and
// then forced (A48), without save_all, since a re-warm's Editor holds no work; it is refresh's
// `close` step, naming the slot (#266). Once the Editor is gone, the slot is checked again as
// transition 22 has it: clean → ready, dirty → parked. A live Editor upool did not open, and a
// close that fails even with --force, are warnings, and the slot stays warming. A warming slot
// with no live Editor is transition 22's own case, which the next take, status or doctor handles.
// It returns the slots it returned to the pool.
func closeLeftovers(ctx context.Context, inv *invocation, rp registeredPool, g git.Client,
	warn func(line string)) ([]leftoverSlot, error) {
	p, err := pool.Load(inv.home, rp.repo.Key)
	if err != nil {
		return nil, err
	}
	editors, err := ledger.Load(inv.home)
	if err != nil {
		return nil, err
	}
	procs := inv.checker()
	var done []leftoverSlot
	for _, s := range p.Slots {
		if s.State != pool.StateWarming {
			continue
		}
		if s.OwnerPID != nil && s.OwnerPIDStart != nil {
			alive, err := procs.Alive(*s.OwnerPID, *s.OwnerPIDStart)
			if errors.Is(err, errors.ErrUnsupported) {
				continue
			}
			if err != nil {
				return done, fmt.Errorf("checking %s's owner, process %d: %w", s.Name,
					*s.OwnerPID, err)
			}
			if alive {
				continue
			}
		}
		e, live, err := liveEditorIn(editors, procs, s.Path)
		if err != nil {
			return done, err
		}
		if !live {
			continue
		}
		if e.Mode != modeHeadless || !e.OpenedByUpool {
			warn(fmt.Sprintf("left %s warming: an Editor upool did not open, process %d, has "+
				"it open", s.Name, e.PID))
			continue
		}
		_, _ = fmt.Fprintf(inv.stderr, "upool: closing the Editor a re-warm left in %s, "+
			"process %d\n", s.Name, e.PID) // progress only
		err = inv.timer.Step("close", s.Name, func() error {
			_, warnings, err := shutSlotEditorRetrying(ctx, inv, rp, s)
			for _, w := range warnings {
				warn(w)
			}
			return err
		})
		if err != nil {
			warn(fmt.Sprintf("could not close the Editor a re-warm left in %s, process %d, so "+
				"it stays warming: %v", s.Name, e.PID, err))
			continue
		}
		rc := pool.Recoverer{Git: g, Proc: procs, Home: inv.home, Key: rp.repo.Key}
		recovered, err := rc.Recover(ctx)
		if err != nil {
			return done, err
		}
		for _, r := range recovered {
			switch {
			case r.Slot != s.Name:
				_, _ = fmt.Fprintln(inv.stderr, "upool: "+recoveredLine(r)) // progress only
			case r.Err != nil:
				warn(fmt.Sprintf("closed the Editor a re-warm left in %s, but could not check "+
					"the slot again: %v", s.Name, r.Err))
			default:
				done = append(done, leftoverSlot{Slot: s.Name, PID: e.PID, State: r.State})
			}
		}
	}
	return done, nil
}

// liveEditorIn returns the Editor the ledger l records with slot open, compared by canonical
// path, whose process procs finds running, and whether there is one.
func liveEditorIn(l ledger.Ledger, procs proc.Checker, slot string) (ledger.Editor, bool, error) {
	key, err := paths.Key(slot)
	if err != nil {
		return ledger.Editor{}, false, err
	}
	for _, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return ledger.Editor{}, false, err
		}
		if ek != key {
			continue
		}
		_, err = procs.Start(e.PID)
		if errors.Is(err, proc.ErrNoProcess) {
			continue
		}
		if err != nil {
			return ledger.Editor{}, false, fmt.Errorf("checking the Editor of %s, process %d: %w",
				slot, e.PID, err)
		}
		return e, true, nil
	}
	return ledger.Editor{}, false, nil
}
