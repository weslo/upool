package main

import (
	"context"
	"errors"
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
	// base is where the slots belong, the pool's base or origin/<base>, and commit the commit it
	// names, as resolve found them.
	base, commit string
	// tried are the slots this refresh has tried to move, by name, each tried once.
	tried map[string]bool
}

// resolve finds base and its commit: the pool's base or, with fetch, `origin/<base>` after
// `git fetch origin <base>`. A failure to fetch, or to resolve base, fails the refresh.
func (m *mover) resolve(ctx context.Context, fetch bool) error {
	p, err := pool.Load(m.inv.home, m.rp.repo.Key)
	if err != nil {
		return err
	}
	m.base = p.Base
	if fetch {
		if err := m.g.Fetch(ctx, m.repo, "origin", p.Base); err != nil {
			return fmt.Errorf("fetching %s from origin: %w", p.Base, err)
		}
		m.base = "origin/" + p.Base
	}
	m.commit, err = m.g.Commit(ctx, m.repo, m.base)
	return err
}

// moveReady moves each ready slot that no lease holds, no Editor has open, whose HEAD is an
// ancestor of base's commit but not that commit (A41), and that it has not tried before, to that
// commit, records in r what it moved, and returns how many slots it tried. A slot it could not move is a warning, not a failure, as a
// needs-reset slot the top-up could not reset is; only a failure to read the pool or the ledger is
// moveReady's error.
func (m mover) moveReady(ctx context.Context, r *refreshReport) (int, error) {
	p, err := pool.Load(m.inv.home, m.rp.repo.Key)
	if err != nil {
		return 0, err
	}
	editors, err := ledger.Load(m.inv.home)
	if err != nil {
		return 0, err
	}
	tried := 0
	for _, s := range p.Slots {
		if s.State != pool.StateReady || s.Lease != nil || m.tried[s.Name] {
			continue
		}
		head, err := m.g.Head(ctx, s.Path)
		if err != nil {
			m.warn(fmt.Sprintf("did not move %s to %s: %v", s.Name, m.base, err))
			m.tried[s.Name] = true
			continue
		}
		if head == m.commit {
			continue
		}
		forward, err := m.g.IsAncestor(ctx, m.repo, head, m.commit)
		if err != nil {
			m.warn(fmt.Sprintf("did not move %s to %s: %v", s.Name, m.base, err))
			m.tried[s.Name] = true
			continue
		}
		if !forward {
			continue // a slot is moved only forward (A41)
		}
		live, err := editors.LiveIn(m.inv.checker(), s.Path)
		if err != nil {
			return tried, err
		}
		if live {
			m.warn(fmt.Sprintf("did not move %s to %s: an Editor has it open", s.Name, m.base))
			m.tried[s.Name] = true
			continue
		}
		m.tried[s.Name] = true
		tried++
		moved, ok, err := m.move(ctx, s)
		if err != nil {
			return tried, err
		}
		if ok {
			r.Moved = append(r.Moved, moved)
		}
	}
	if len(r.Moved) > 0 {
		r.Base, r.Commit = m.base, m.commit
	}
	return tried, nil
}

// move moves the ready slot s to base's commit and re-warms it.
//
//  1. Under the lock, it marks s warming, with upool's own PID as its owner, if it is still ready
//     and unleased ((f) transition 3; A4), so that no take hands it out mid-checkout or while its
//     Editor holds Temp/UnityLockfile.
//  2. With the lock dropped, it runs `git checkout --detach <commit>` between two `unity cache
//     key` calls, and a changed key sets reimport_pending (A20).
//  3. It re-warms the slot as `upool open --headless` and `upool close` do: admitted to the ledger,
//     as a cold import when reimport_pending is set and waiting its turn under max_cold_imports,
//     opened, `status --until-ready` for A37's cold-import timeout whatever the charge, then
//     save_all and close (A40).
//  4. It returns the slot to ready, once clean ((f) transition 18).
//
// A slot that is no longer ready when step 1 looks is skipped. A failed checkout is a warning, and
// the slot goes back to ready where it was, not moved. A failed re-warm is a warning: an Editor
// still running when the wait ran out is closed without save_all, which it has no work for, the
// slot's reimport_pending is set, so that its next open is charged as the cold import the re-warm
// did not finish, and the moved slot goes back to ready (A40). A slot left dirty, or whose Editor
// could not be closed, stays warming, a warning saying so: once this refresh has exited and no
// Editor is live in it, the next take, status or doctor checks it again ((f) transition 22). It
// reports whether it moved the slot and returned it to ready.
func (m mover) move(ctx context.Context, s pool.Slot) (movedSlot, bool, error) {
	held, err := m.hold(ctx, s)
	if err != nil || !held {
		return movedSlot{}, false, err
	}
	_, _ = fmt.Fprintf(m.inv.stderr, "upool: moving %s to %s (%s) and re-warming it\n", s.Name,
		m.base, shortCommit(m.commit)) // progress only

	changed, note, err := m.keys.Around(ctx, s.Path, func() error {
		return m.g.CheckoutDetach(ctx, s.Path, m.commit)
	})
	if note != "" {
		m.warn(note)
	}
	if err != nil {
		m.warn(fmt.Sprintf("did not move %s to %s: %v", s.Name, m.base, err))
		_, _, err := m.finish(ctx, s, movedSlot{})
		return movedSlot{}, false, err
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
	_, warnings, err := openSlotEditor(ctx, m.inv, m.rp, s, modeHeadless, true)
	for _, w := range warnings {
		m.warn(w)
	}
	if err != nil {
		return m.unwarmed(ctx, s, err)
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

// unwarmed deals with the slot s, moved to base, whose re-warm failed with err (A40). An Editor
// still running when the wait ran out is closed without save_all; if that fails, the Editor stays
// in the ledger and the slot warming. Otherwise the slot's reimport_pending is set and it goes back
// to ready, not re-warmed.
func (m mover) unwarmed(ctx context.Context, s pool.Slot, err error) (movedSlot, bool, error) {
	var notReady *notReadyError
	if errors.As(err, &notReady) {
		_, warnings, closeErr := shutSlotEditor(ctx, m.inv, m.rp, s)
		for _, w := range warnings {
			m.warn(w)
		}
		if closeErr != nil {
			m.warn(fmt.Sprintf("moved %s to %s but did not re-warm it: %v; nor could it close "+
				"the Editor, so the slot stays warming until the Editor is gone: %v", s.Name,
				m.base, err, closeErr))
			return movedSlot{}, false, nil
		}
		err = fmt.Errorf("%w; its Editor is closed", err)
	}
	m.warn(fmt.Sprintf("moved %s to %s but did not re-warm it: %v", s.Name, m.base, err))
	if err := m.setReimportPending(ctx, s.Name); err != nil {
		return movedSlot{}, false, err
	}
	return m.finish(ctx, s, movedSlot{Slot: s.Name})
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
// key (A20) or whose re-warm failed (A40).
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
