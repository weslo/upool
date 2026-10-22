package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/pool"
)

// mover moves a pool's idle ready slots to base and re-warms them, refresh's last step
// ((f) transitions 3 and 18; docs/design.md, "Slot lifecycle: seed only from a Library current at
// HEAD, refresh on handout, reset without `-x`": `refresh --fetch` moves idle ready slots to
// `origin/<base>` so the Editor reimports only the delta; #97).
type mover struct {
	inv *invocation
	rp  registeredPool
	// repo is the pool's main checkout.
	repo string
	g    git.Client
	// keys compares each slot's cache key across its checkout (A20).
	keys clone.KeyCheck
	// warn adds a line to refresh's warnings.
	warn func(line string)
}

// moveReady moves each ready slot that no lease holds and no Editor has open, and whose HEAD is not
// base's commit, to that commit, and records in r what it moved. base is the pool's base or, with
// fetch, `origin/<base>` after `git fetch origin <base>`. A slot it could not move is a warning,
// not a failure, as a needs-reset slot the top-up could not reset is; only a failure to fetch, to
// read the pool or to resolve base is moveReady's error.
func (m mover) moveReady(ctx context.Context, fetch bool, r *refreshReport) error {
	key := m.rp.repo.Key
	p, err := pool.Load(m.inv.home, key)
	if err != nil {
		return err
	}
	base := p.Base
	if fetch {
		if err := m.g.Fetch(ctx, m.repo, "origin", p.Base); err != nil {
			return fmt.Errorf("fetching %s from origin: %w", p.Base, err)
		}
		base = "origin/" + p.Base
	}
	commit, err := m.g.Commit(ctx, m.repo, base)
	if err != nil {
		return err
	}
	editors, err := ledger.Load(m.inv.home)
	if err != nil {
		return err
	}
	for _, s := range p.Slots {
		if s.State != pool.StateReady || s.Lease != nil {
			continue
		}
		head, err := m.g.Head(ctx, s.Path)
		if err != nil {
			m.warn(fmt.Sprintf("did not move %s to %s: %v", s.Name, base, err))
			continue
		}
		if head == commit {
			continue
		}
		live, err := editors.LiveIn(m.inv.checker(), s.Path)
		if err != nil {
			return err
		}
		if live {
			m.warn(fmt.Sprintf("did not move %s to %s: an Editor has it open", s.Name, base))
			continue
		}
		moved, ok, err := m.move(ctx, s, base, commit)
		if err != nil {
			return err
		}
		if ok {
			r.Moved = append(r.Moved, moved)
		}
	}
	if len(r.Moved) > 0 {
		r.Base, r.Commit = base, commit
	}
	return nil
}

// move moves the ready slot s to commit, which base names, and re-warms it.
//
//  1. Under the lock, it marks s warming, with upool's own PID as its owner, if it is still ready
//     and unleased ((f) transition 3; A4), so that no take hands it out mid-checkout or while its
//     Editor holds Temp/UnityLockfile.
//  2. With the lock dropped, it runs `git checkout --detach <commit>` between two `unity cache
//     key` calls, and a changed key sets reimport_pending (A20).
//  3. It re-warms the slot as `upool open --headless` and `upool close` do: admitted to the ledger,
//     as a cold import when reimport_pending is set and waiting its turn under max_cold_imports,
//     opened, `status --until-ready`, then save_all and close.
//  4. It returns the slot to ready, once clean ((f) transition 18).
//
// A slot that is no longer ready when step 1 looks is skipped. A failed checkout or re-warm is a
// warning, and the slot goes back to ready all the same, its next open doing the import. A slot
// left dirty, or whose Editor could not be closed, stays warming, a warning saying so: once this
// refresh has exited and no Editor is live in it, the next take, status or doctor checks it again
// ((f) transition 22). It reports whether it returned the slot to ready.
func (m mover) move(ctx context.Context, s pool.Slot, base, commit string) (movedSlot, bool,
	error) {
	held, err := m.hold(ctx, s)
	if err != nil || !held {
		return movedSlot{}, false, err
	}
	_, _ = fmt.Fprintf(m.inv.stderr, "upool: moving %s to %s (%s) and re-warming it\n", s.Name,
		base, shortCommit(commit)) // progress only

	changed, note, err := m.keys.Around(ctx, s.Path, func() error {
		return m.g.CheckoutDetach(ctx, s.Path, commit)
	})
	if note != "" {
		m.warn(note)
	}
	if err != nil {
		m.warn(fmt.Sprintf("did not move %s to %s: %v", s.Name, base, err))
		return m.finish(ctx, s, movedSlot{Slot: s.Name})
	}
	if changed {
		if err := m.setReimportPending(ctx, s.Name); err != nil {
			return movedSlot{}, false, err
		}
		s.ReimportPending = true
		m.warn(clone.ReimportWarning(s.Name))
	}

	moved := movedSlot{Slot: s.Name}
	begin := m.inv.now()
	_, warnings, err := openSlotEditor(ctx, m.inv, m.rp, s, modeHeadless)
	for _, w := range warnings {
		m.warn(w)
	}
	if err != nil {
		m.warn(fmt.Sprintf("moved %s to %s but did not re-warm it: %v", s.Name, base, err))
		return m.finish(ctx, s, moved)
	}
	_, warnings, err = closeSlotEditor(ctx, m.inv, m.rp, s)
	for _, w := range warnings {
		m.warn(w)
	}
	if err != nil {
		m.warn(fmt.Sprintf("re-warmed %s but could not close its Editor, so it stays warming "+
			"until the Editor is gone: %v", s.Name, err))
		return movedSlot{}, false, nil
	}
	moved.Rewarmed, moved.RewarmSeconds = true, m.inv.now().Sub(begin).Seconds()
	return m.finish(ctx, s, moved)
}

// hold marks the ready slot s warming, with upool's own PID and its start time as the owner, if
// state.json still has it ready and unleased, and reports whether it did ((f) transition 3).
func (m mover) hold(ctx context.Context, s pool.Slot) (bool, error) {
	start, err := m.inv.checker().Start(m.inv.pid)
	if err != nil {
		return false, fmt.Errorf("reading the start time of process %d, upool itself: %w",
			m.inv.pid, err)
	}
	held := false
	err = pool.Update(ctx, m.inv.home, m.rp.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(c pool.Slot) bool { return c.Name == s.Name })
		if i < 0 || p.Slots[i].State != pool.StateReady || p.Slots[i].Lease != nil {
			return nil
		}
		state, err := pool.Next(p.Slots[i], pool.Refresh{})
		if err != nil {
			return err
		}
		pid := m.inv.pid
		p.Slots[i].State, p.Slots[i].OwnerPID, p.Slots[i].OwnerPIDStart = state, &pid, &start
		held = true
		return nil
	})
	return held, err
}

// setReimportPending sets the reimport_pending of the slot name, whose checkout changed its cache
// key (A20).
func (m mover) setReimportPending(ctx context.Context, name string) error {
	return pool.Update(ctx, m.inv.home, m.rp.repo.Key, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == name {
				p.Slots[i].ReimportPending = true
			}
		}
		return nil
	})
}

// finish returns the warming slot s to ready once its define-order noise is reverted and it is
// clean ((f) transition 18), and reports moved as the slot moved. A dirty slot stays warming, with
// a warning: only its owner's death returns it, as parked ((f) transition 22).
func (m mover) finish(ctx context.Context, s pool.Slot, moved movedSlot) (movedSlot, bool,
	error) {
	if _, err := pool.RevertNoise(ctx, m.g, s.Path); err != nil {
		return movedSlot{}, false, err
	}
	dirty, err := pool.Dirty(ctx, m.g, s.Path)
	if err != nil {
		return movedSlot{}, false, err
	}
	if len(dirty) > 0 {
		m.warn(fmt.Sprintf("left %s warming, since it is dirty after its move: %s; once this "+
			"refresh has exited, the next take, status or doctor parks it", s.Name,
			strings.Join(dirty, "; ")))
		return movedSlot{}, false, nil
	}
	err = pool.Update(ctx, m.inv.home, m.rp.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(c pool.Slot) bool { return c.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		c := &p.Slots[i]
		if c.State != pool.StateWarming || c.OwnerPID == nil || *c.OwnerPID != m.inv.pid {
			return fmt.Errorf("%s is %s, no longer held by process %d", s.Name, c.State,
				m.inv.pid)
		}
		state, err := pool.Next(*c, pool.WarmingFinish{Clean: true})
		if err != nil {
			return err
		}
		c.State, c.OwnerPID, c.OwnerPIDStart = state, nil, nil
		return nil
	})
	if err != nil {
		return movedSlot{}, false, err
	}
	return moved, true, nil
}
