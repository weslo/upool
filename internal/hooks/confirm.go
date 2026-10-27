package hooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// Confirmed is what Confirm did.
type Confirmed struct {
	// Path is the slot's path as state.json records it, and Slot its name.
	Path, Slot string
	// LeaseID is the lease confirmed, and Repo the pool's main checkout.
	LeaseID, Repo string
	// Reset says whether the confirm ran the slot's deferred reset (A23).
	Reset bool
}

// Confirm is the confirm that `upool take --confirm <path>` and the SessionStart hook run for the
// session's slot (docs/design.md, "Environment, confirmation and release"; docs/contracts.md (b);
// (f) transition 6). It finds the slot by its canonical path, runs the deferred reset of a slot
// handed out from needs-reset, and confirms the lease with env.ParentPID, the session's CLI, as its
// holder (A6), recording sessionID, the session's own, when it is not empty, by which the
// session's later starts find the slot wherever their cwd is (A46). A confirmed lease is confirmed
// again. It copies no file into the slot: Claude Code
// has read CLAUDE.md before SessionStart runs, so the files a project's .worktreeinclude names are
// mirrored into free slots before a handout instead (A44; internal/mirror). The warnings are for
// stderr.
func Confirm(ctx context.Context, env Env, path, sessionID string) (Confirmed, []string, error) {
	ref, err := confirmTarget(env.Home, path)
	if err != nil {
		return Confirmed{}, nil, err
	}
	s, repo := ref.slot, ref.pool.Repo
	if repo == "" {
		repo = ref.repo.Path
	}
	var warnings []string
	reset, reimport := false, false
	if s.Lease.ResetPending {
		changed, note, err := deferredReset(ctx, env, s, ref.pool.Base)
		if err != nil {
			return Confirmed{}, nil, err
		}
		if note != "" {
			warnings = append(warnings, note)
		}
		if changed {
			warnings = append(warnings, clone.ReimportWarning(s.Name))
		}
		reset, reimport = true, changed
	}

	holderPID, holderStart, err := holder(env.ParentPID)
	if err != nil {
		return Confirmed{}, nil, err
	}
	err = pool.Update(ctx, env.Home, ref.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		cur := &p.Slots[i]
		if cur.Lease == nil || cur.Lease.LeaseID != s.Lease.LeaseID {
			return fmt.Errorf("%s's lease changed meanwhile: it is %s now, and nothing was "+
				"confirmed", s.Name, cur.State)
		}
		if cur.State == pool.StateLeasedUnconfirmed {
			next, err := pool.Next(*cur, pool.Confirm{})
			if err != nil {
				return err
			}
			cur.State = next
		}
		cur.Lease.Confirmed = true
		cur.Lease.HolderPID, cur.Lease.HolderPIDStart = holderPID, holderStart
		// A session took the slot, so its path is not refused: the run of unconfirmed leases
		// that would quarantine it ends (A50).
		cur.UnconfirmedExpiries = 0
		if sessionID != "" {
			cur.Lease.SessionID = &sessionID
		}
		if reset {
			cur.Lease.ResetPending = false
		}
		if reimport {
			cur.ReimportPending = true
		}
		return nil
	})
	if err != nil {
		return Confirmed{}, nil, err
	}
	return Confirmed{Path: s.Path, Slot: s.Name, LeaseID: s.Lease.LeaseID, Repo: repo,
		Reset: reset}, warnings, nil
}

// confirmTarget finds the slot whose path is path's canonical key in the registered pools, since
// the desktop app may hand a path back lowercased, and refuses one that holds no lease.
func confirmTarget(home, path string) (slotRef, error) {
	ref, found, err := findSlot(home, path)
	if err != nil {
		return slotRef{}, err
	}
	if !found {
		return slotRef{}, fmt.Errorf("%s is not a slot of any registered pool", path)
	}
	if s := ref.slot; s.Lease == nil ||
		(s.State != pool.StateLeasedUnconfirmed && s.State != pool.StateLeased) {
		return slotRef{}, fmt.Errorf("%s is %s, not leased, so there is no lease to confirm",
			s.Name, s.State)
	}
	return ref, nil
}

// deferredReset is the reset a slot handed out from needs-reset still owes (A5; A23): define-order
// noise reverted, then `git checkout --detach <base>` and `git clean -fd`, with the slot's cache
// key compared across the checkout (A20). The slot was clean when it was marked needs-reset; one
// that is not clean now is refused, with nothing reset, rather than have git clean take its files.
func deferredReset(ctx context.Context, env Env, s pool.Slot, base string) (changed bool,
	note string, err error) {
	if _, err := pool.RevertNoise(ctx, env.Git, s.Path); err != nil {
		return false, "", err
	}
	dirty, err := pool.Dirty(ctx, env.Git, s.Path)
	if err != nil {
		return false, "", err
	}
	if len(dirty) > 0 {
		return false, "", fmt.Errorf("%s needs its deferred reset but is dirty, so nothing was "+
			"reset or confirmed: %v", s.Name, dirty)
	}
	keys := clone.KeyCheck{Env: env.Git.Env}
	return keys.Around(ctx, s.Path, func() error {
		return pool.ResetToBase(ctx, env.Git, s.Path, base)
	})
}

// holder returns the process a lease follows, ppid, and its start time (A6). Off macOS, where
// internal/proc reads no start times, the lease records no holder, as `upool run`'s does there.
func holder(ppid int) (*int, *time.Time, error) {
	start, err := (proc.System{}).Start(ppid)
	if errors.Is(err, errors.ErrUnsupported) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the start time of process %d, upool's parent: %w",
			ppid, err)
	}
	return &ppid, &start, nil
}
