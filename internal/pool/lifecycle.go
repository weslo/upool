package pool

import (
	"fmt"
	"slices"
)

// The two outcomes in the To column of docs/contracts.md (f) that are not states. Next returns
// them, and no slot is ever written to state.json in either.
const (
	// StateRemoved is a slot whose entry, worktree and directory go: a pruned ready slot
	// (transition 15), or a clone whose owner died (transition 21).
	StateRemoved State = "removed"
	// StateMissing is a slot that left the disk or `git worktree list` (transition 17): a
	// condition take skips and doctor reports, not a state.
	StateMissing State = "missing"
)

// states are the seven slot states of docs/contracts.md (f), the ones transition 17's "any" means.
var states = []State{
	StateCreating, StateWarming, StateReady, StateLeasedUnconfirmed, StateLeased, StateParked,
	StateNeedsReset,
}

// Event is a cause of a transition in docs/contracts.md (f), carrying the facts Next cannot read
// from the slot: callers gather them (git status, internal/proc, the ledger, the rest of the
// pool). The types in this file are the only events.
type Event interface {
	fmt.Stringer
	// event seals the interface.
	event()
}

// CloneStart is a clone beginning: init's first slots, a take that finds no slot to hand out, or
// refresh topping the pool up (transition 1). It applies to the zero Slot, one not yet in
// state.json.
type CloneStart struct{}

// CloneFinish is the clone recipe completing with its step 8 checks passed (transition 2;
// docs/contracts.md (g)).
type CloneFinish struct{}

// Refresh is `upool refresh` reaching the slot: it moves a ready slot to base and re-warms it
// (transition 3), and resets a needs-reset slot before it clones anything (transition 20).
type Refresh struct{}

// Seed is a clone choosing a ready slot as its seed, holding it while step 4 copies it
// (transition 23; A19).
type Seed struct{}

// WarmingFinish is the process holding a warming slot finishing with it: refresh's move and
// re-warm, or the copy that read it as a seed (transition 18).
type WarmingFinish struct {
	// Clean is whether the slot is clean, as (f) defines it. Only a clean slot is ready again.
	Clean bool
}

// HookTake is the create hook's take, `upool take --no-create --holder <session_id>`, whose
// lease starts unconfirmed (transitions 4 and 10; A6).
type HookTake struct {
	// ReadyAvailable is whether the pool has a ready slot. Next reads it only for a needs-reset
	// slot, which is handed out only when no slot is ready (A5).
	ReadyAvailable bool
}

// Take is every other take: `upool take <name>` from Terminal, a subagent's explicit take, and
// `upool run`, whose lease starts confirmed (transitions 5 and 19; A6).
type Take struct {
	// ReadyAvailable is as for HookTake.
	ReadyAvailable bool
}

// Confirm is the SessionStart hook's `upool take --confirm`, matched by worktree path
// (transition 6).
type Confirm struct{}

// ConfirmTimeout is an unconfirmed lease outliving confirm_timeout: the app refused the session
// and no remove will come (transition 7).
type ConfirmTimeout struct{}

// Remove is the WorktreeRemove hook (transitions 8 and 9).
type Remove struct {
	// Clean is whether the slot is clean, as (f) defines it.
	Clean bool
}

// Release is `upool release` (transitions 11 and 12).
type Release struct {
	// Clean is whether the slot is clean, as (f) defines it.
	Clean bool
}

// Reclaim is doctor, take or refresh finding a lease to check: its holder is reclaimed only when
// dead (transitions 13 and 14; docs/design.md, "Leases that survive crashes and never reclaim a
// live tree"). A lease with no holder PID, a bare take's, is never reclaimed (A6).
type Reclaim struct {
	// HolderAlive is whether the lease's holder PID, at its start time, is alive (internal/proc).
	HolderAlive bool
	// EditorLive is whether the ledger shows a live Editor in the slot.
	EditorLive bool
	// Clean is whether the slot is clean, as (f) defines it.
	Clean bool
}

// Prune is `upool prune --yes` removing a ready slot (transition 15). Which slots it chooses is
// prune's: idle, merged and clean beyond ready, LRU to max_disk, shrink_after.
type Prune struct{}

// Missing is the slot found gone from the disk or from `git worktree list` (transition 17).
type Missing struct{}

// DeadOwner is a creating or warming slot whose owner PID is dead (transitions 21 and 22; A4).
type DeadOwner struct {
	// EditorLive is whether the ledger shows a live Editor in the slot.
	EditorLive bool
	// Clean is whether the slot is clean, as (f) defines it.
	Clean bool
}

func (CloneStart) event()     {}
func (CloneFinish) event()    {}
func (Refresh) event()        {}
func (Seed) event()           {}
func (WarmingFinish) event()  {}
func (HookTake) event()       {}
func (Take) event()           {}
func (Confirm) event()        {}
func (ConfirmTimeout) event() {}
func (Remove) event()         {}
func (Release) event()        {}
func (Reclaim) event()        {}
func (Prune) event()          {}
func (Missing) event()        {}
func (DeadOwner) event()      {}

func (CloneStart) String() string { return "clone start" }

func (CloneFinish) String() string { return "clone finish" }

func (Refresh) String() string { return "refresh" }

func (Seed) String() string { return "seed" }

func (e WarmingFinish) String() string { return "warming finish (" + clean(e.Clean) + ")" }

func (e HookTake) String() string { return "hook take (" + readyAvailable(e.ReadyAvailable) + ")" }

func (e Take) String() string { return "take (" + readyAvailable(e.ReadyAvailable) + ")" }

func (Confirm) String() string { return "confirm" }

func (ConfirmTimeout) String() string { return "confirm timeout" }

func (e Remove) String() string { return "remove (" + clean(e.Clean) + ")" }

func (e Release) String() string { return "release (" + clean(e.Clean) + ")" }

func (e Reclaim) String() string {
	holder := "holder dead"
	if e.HolderAlive {
		holder = "holder alive"
	}
	return "reclaim (" + holder + ", " + editorLive(e.EditorLive) + ", " + clean(e.Clean) + ")"
}

func (Prune) String() string { return "prune" }

func (Missing) String() string { return "missing" }

func (e DeadOwner) String() string {
	return "dead owner (" + editorLive(e.EditorLive) + ", " + clean(e.Clean) + ")"
}

func clean(c bool) string {
	if c {
		return "clean"
	}
	return "dirty"
}

func readyAvailable(r bool) string {
	if r {
		return "a ready slot available"
	}
	return "no slot ready"
}

func editorLive(l bool) string {
	if l {
		return "Editor live"
	}
	return "no live Editor"
}

// Next returns the state slot s moves to on event e, by the transitions in docs/contracts.md (f):
// one of the seven states, StateRemoved or StateMissing. It is the slot lifecycle as a pure
// function (docs/design.md, "Repository, CI and Homebrew distribution"): it does no I/O and
// changes nothing, so the git and file work around a transition, and writing its result, are the
// caller's. A slot not yet in state.json is the zero Slot, which only CloneStart moves.
//
// An event (f) lists no transition for from s's state, or whose facts fail the transition's
// conditions, such as Reclaim of a live holder, is an error naming the state and the event.
func Next(s Slot, e Event) (State, error) {
	switch e := e.(type) {
	case CloneStart:
		if s.State == "" {
			return StateCreating, nil // 1
		}
	case CloneFinish:
		if s.State == StateCreating {
			return StateReady, nil // 2
		}
	case Refresh:
		switch s.State {
		case StateReady:
			return StateWarming, nil // 3
		case StateNeedsReset:
			return StateReady, nil // 20
		}
	case Seed:
		if s.State == StateReady {
			return StateWarming, nil // 23
		}
	case WarmingFinish:
		if s.State == StateWarming && e.Clean {
			return StateReady, nil // 18
		}
	case HookTake:
		switch {
		case s.State == StateReady:
			return StateLeasedUnconfirmed, nil // 4
		case s.State == StateNeedsReset && !e.ReadyAvailable:
			return StateLeasedUnconfirmed, nil // 10
		}
	case Take:
		switch {
		case s.State == StateReady:
			return StateLeased, nil // 5
		case s.State == StateNeedsReset && !e.ReadyAvailable:
			return StateLeased, nil // 19
		}
	case Confirm:
		if s.State == StateLeasedUnconfirmed {
			return StateLeased, nil // 6
		}
	case ConfirmTimeout:
		if s.State == StateLeasedUnconfirmed {
			return StateReady, nil // 7
		}
	case Remove:
		if s.State == StateLeased {
			if e.Clean {
				return StateNeedsReset, nil // 8
			}
			return StateParked, nil // 9
		}
	case Release:
		if s.State == StateLeased {
			if e.Clean {
				return StateReady, nil // 11
			}
			return StateParked, nil // 12
		}
	case Reclaim:
		if s.State == StateLeased && s.Lease != nil && s.Lease.HolderPID != nil &&
			!e.HolderAlive {
			switch {
			case !e.Clean:
				return StateParked, nil // 14
			case !e.EditorLive:
				return StateReady, nil // 13
			}
		}
	case Prune:
		if s.State == StateReady {
			return StateRemoved, nil // 15
		}
	case Missing:
		if slices.Contains(states, s.State) {
			return StateMissing, nil // 17
		}
	case DeadOwner:
		switch {
		case s.State == StateCreating:
			return StateRemoved, nil // 21
		case s.State == StateWarming && !e.EditorLive:
			if e.Clean {
				return StateReady, nil // 22
			}
			return StateParked, nil // 22
		}
	}
	// A parked slot lands here for every event but Missing: it has no automatic transition, and a
	// person resolves it (16).
	if s.Name != "" {
		return "", fmt.Errorf("%s: no transition from %q on %s", s.Name, s.State, e)
	}
	return "", fmt.Errorf("no transition from %q on %s", s.State, e)
}
