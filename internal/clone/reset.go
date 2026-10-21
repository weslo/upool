package clone

import (
	"context"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pool"
)

// ResetNeedsReset resets the slot named name, of the pool key names under home, to the pool's base
// and makes it ready, if state.json still has it needs-reset and unleased ((f) transition 20; A5):
// define-order noise reverted, then `git checkout --detach <base>` and `git clean -fd`, with the
// slot's cache key compared across the checkout (A20). It runs under ~/.upool/lock, so that the
// create hook, whose take needs the lock, cannot hand the slot out halfway through. A reset of a
// clean slot is short, unlike the clone and the import the lock is never held across. A slot that
// is dirty now, though it was clean when it was marked needs-reset, is left as it is rather than
// have git clean take its files. It returns whether it reset the slot, why not when it did not,
// and the warnings for the caller to say.
func ResetNeedsReset(ctx context.Context, g git.Client, keys KeyCheck, home, key,
	name string) (reset bool, why string, warnings []string, err error) {
	err = pool.Update(ctx, home, key, func(p *pool.Pool) error {
		reset, why, warnings = false, "", nil
		i := slices.IndexFunc(p.Slots, func(s pool.Slot) bool { return s.Name == name })
		if i < 0 {
			why = "it left state.json meanwhile"
			return nil
		}
		s := &p.Slots[i]
		if s.State != pool.StateNeedsReset || s.Lease != nil {
			why = "it was taken meanwhile"
			return nil
		}
		if _, err := pool.RevertNoise(ctx, g, s.Path); err != nil {
			return err
		}
		dirty, err := pool.Dirty(ctx, g, s.Path)
		if err != nil {
			return err
		}
		if len(dirty) > 0 {
			why = "it is dirty, so it was not reset: " + strings.Join(dirty, "; ")
			return nil
		}
		changed, note, err := keys.Around(ctx, s.Path, func() error {
			return pool.ResetToBase(ctx, g, s.Path, p.Base)
		})
		if err != nil {
			return err
		}
		if note != "" {
			warnings = append(warnings, note)
		}
		state, err := pool.Next(*s, pool.Refresh{})
		if err != nil {
			return err
		}
		s.State = state
		if changed {
			s.ReimportPending = true
			warnings = append(warnings, ReimportWarning(s.Name))
		}
		reset = true
		return nil
	})
	return reset, why, warnings, err
}
