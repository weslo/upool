package pool

import (
	"context"
	"fmt"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/paths"
)

// The git work around a slot's return to the pool, which release and the dead-holder reclaim share
// (docs/contracts.md (f) transitions 11 to 14; docs/design.md, "Slot lifecycle: seed only from a
// Library current at HEAD, refresh on handout, reset without `-x`").

// Dirty returns why the slot at dir is dirty as docs/contracts.md (f) defines it, or none when it
// is clean: its `git status --porcelain` lines, or, when those are empty, that its HEAD is on no
// branch, since no ref contains it and a reset to base would orphan it (A5). Define-order noise is
// the caller's to revert first (RevertNoise).
func Dirty(ctx context.Context, g git.Client, dir string) ([]string, error) {
	status, err := g.Status(ctx, dir)
	if err != nil {
		return nil, err
	}
	var reasons []string
	for _, e := range status {
		if e.OrigPath != "" {
			reasons = append(reasons, fmt.Sprintf("%s %s -> %s", e.XY, e.OrigPath, e.Path))
		} else {
			reasons = append(reasons, e.XY+" "+e.Path)
		}
	}
	if len(reasons) > 0 {
		return reasons, nil
	}
	reachable, err := g.HeadReachable(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !reachable {
		return []string{"HEAD is on no branch"}, nil
	}
	return nil, nil
}

// DirtyBesideNoise is Dirty with define-order noise counted as clean but left in place, for the
// WorktreeRemove hook: it runs in the desktop app's restricted helper, where git reads worked but
// git writes were never tried, so the revert waits for the deferred reset with the rest of it
// (docs/design.md, "What remains unverified"; docs/contracts.md (a), WorktreeRemove rules).
func DirtyBesideNoise(ctx context.Context, g git.Client, dir string) ([]string, error) {
	noise, err := Noise(ctx, g, dir)
	if err != nil {
		return nil, err
	}
	if !noise {
		return Dirty(ctx, g, dir)
	}
	reachable, err := g.HeadReachable(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !reachable {
		return []string{"HEAD is on no branch"}, nil
	}
	return nil, nil
}

// ResetToBase resets the clean slot at dir to base: `git checkout --detach <base>`, then
// `git clean -fd`, never -x, so the ignored Library stays warm. The branch the slot was on keeps
// its commits (A18).
func ResetToBase(ctx context.Context, g git.Client, dir, base string) error {
	if err := g.CheckoutDetach(ctx, dir, base); err != nil {
		return err
	}
	return g.Clean(ctx, dir)
}

// Unlock unlocks the worktree at path, run in repo, if git has it locked, as an earlier upool locked
// a slot for its lease's lifetime. No lease takes the lock now (A27), but a slot leased before that
// would otherwise stay locked, and the desktop app does not archive a locked worktree.
func Unlock(ctx context.Context, g git.Client, repo, path string) error {
	key, err := paths.Key(path)
	if err != nil {
		return err
	}
	wts, err := g.WorktreeList(ctx, repo)
	if err != nil {
		return err
	}
	for _, wt := range wts {
		wk, err := paths.Key(wt.Path)
		if err != nil {
			return err
		}
		if wk == key && wt.Locked {
			return g.WorktreeUnlock(ctx, repo, path)
		}
	}
	return nil
}
