package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/weslo/upool/internal/git"
)

// The create hook's take and the expiry of its leases (docs/contracts.md (f) transitions 4, 7 and
// 10; docs/design.md, "Route Claude Code's worktrees through the pool: three branches in one hook"
// and "Leases that survive crashes and never reclaim a live tree"). The app refuses a path that a
// live session holds without sending a remove, so a lease recorded at create time would leak: it
// starts unconfirmed, and ends unless SessionStart confirms it within confirm_timeout.

// ErrNoSlot is an unconfirmed take that found no slot to hand out: none ready and none
// needs-reset. It never clones one: a 26 s clone cannot run inside the create hook.
var ErrNoSlot = errors.New("no slot is ready or needs-reset")

// UnconfirmedTake is the create hook's take, `take --no-create --holder <session_id>`, which only
// internal/hooks makes; no CLI flag reaches it (A6).
type UnconfirmedTake struct {
	// Holder is the create stdin's session_id, and NameHint its name, kept for display only.
	Holder, NameHint string
	// Now is when the lease is taken, and ConfirmTimeout how long it waits to be confirmed: the
	// take first expires the pool's unconfirmed leases older than that.
	Now            time.Time
	ConfirmTimeout time.Duration
	// Present says whether a slot is still there to hand out, on disk and in `git worktree list`,
	// as the create hook can tell from the desktop app's helper with a stat and git alone; a slot
	// it rejects is passed over. Nil passes over none.
	Present func(Slot) bool
}

// Take leases a slot of the pool repoKey names under home, under the lock: a ready slot if there
// is one and a needs-reset slot otherwise, each in the pool's order (A5). The lease starts with
// confirmed false and no holder PID, which `take --confirm` sets (A6), and reset_pending set when
// the slot was needs-reset (A23). It returns the leased slot, the slots whose leases it expired
// first, and ErrNoSlot when none can be handed out, having still written those expiries.
func (t UnconfirmedTake) Take(ctx context.Context, home, repoKey string) (taken Slot,
	expired []Slot, err error) {
	id, err := NewLeaseID()
	if err != nil {
		return Slot{}, nil, err
	}
	err = Update(ctx, home, repoKey, func(p *Pool) error {
		taken = Slot{}
		if expired, err = p.ExpireUnconfirmed(t.Now, t.ConfirmTimeout); err != nil {
			return err
		}
		i := p.handOut(t.Present)
		if i < 0 {
			return nil
		}
		s := &p.Slots[i]
		from := s.State
		state, err := Next(*s, HookTake{ReadyAvailable: from == StateReady})
		if err != nil {
			return err
		}
		s.State = state
		s.Lease = &Lease{LeaseID: id, Holder: t.Holder, WorktreePath: s.Path,
			AcquiredAt: t.Now.UTC(), Confirmed: false, ResetPending: from == StateNeedsReset,
			Lane: "script", NameHint: t.NameHint}
		taken = *s
		return nil
	})
	if err != nil {
		return Slot{}, nil, err
	}
	if taken.Lease == nil {
		return Slot{}, expired, ErrNoSlot
	}
	return taken, expired, nil
}

// handOut returns the index of the slot an unconfirmed take hands out, the first unleased ready
// slot or else the first unleased needs-reset one (A5), of those present accepts when it is not
// nil, or -1 when there is neither.
func (p *Pool) handOut(present func(Slot) bool) int {
	for _, state := range []State{StateReady, StateNeedsReset} {
		for i, s := range p.Slots {
			if s.State == state && s.Lease == nil && (present == nil || present(s)) {
				return i
			}
		}
	}
	return -1
}

// ExpireUnconfirmed ends every unconfirmed lease in p older than timeout at now, and returns the
// slots it ended, as they were before. Each goes back to the state it was handed out from: ready,
// or needs-reset when its deferred reset never ran (transition 7; A23). The caller writes p, and
// finishes each slot with EndExpired; take, status and doctor expire the leases of a pool they read
// (#75).
func (p *Pool) ExpireUnconfirmed(now time.Time, timeout time.Duration) ([]Slot, error) {
	var expired []Slot
	for i := range p.Slots {
		s := &p.Slots[i]
		if !s.ConfirmExpired(now, timeout) {
			continue
		}
		state, err := Next(*s, ConfirmTimeout{})
		if err != nil {
			return nil, err
		}
		expired = append(expired, *s)
		s.State, s.Lease = state, nil
	}
	return expired, nil
}

// EndExpired finishes the unconfirmed leases an expiry ended in the pool whose main checkout is
// repo, once state.json no longer holds them: it unlocks each slot an earlier upool locked
// (Unlock), and returns a line saying what became of each slot.
func EndExpired(ctx context.Context, g git.Client, repo string, expired []Slot,
	timeout time.Duration) []string {
	var lines []string
	for _, s := range expired {
		state := StateReady
		if s.Lease.ResetPending {
			state = StateNeedsReset
		}
		line := fmt.Sprintf("ended the unconfirmed lease on %s, which no session confirmed "+
			"within %s: %s is %s again", s.Name, timeout, s.Name, state)
		if err := Unlock(ctx, g, repo, s.Path); err != nil {
			line += fmt.Sprintf(", but its git worktree lock stays: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
}

// ConfirmExpired says whether s holds an unconfirmed lease older than timeout at now, which has
// therefore expired.
func (s Slot) ConfirmExpired(now time.Time, timeout time.Duration) bool {
	return s.State == StateLeasedUnconfirmed && s.Lease != nil && !s.Lease.Confirmed &&
		now.Sub(s.Lease.AcquiredAt) > timeout
}

// NewLeaseID returns a random lease_id, 16 bytes in hex (docs/contracts.md (e) Lease).
func NewLeaseID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("making a lease ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}
