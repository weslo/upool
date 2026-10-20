package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// Seed is the checkout a clone copies, as Seeder.Choose chose it (docs/contracts.md (g) step 0).
type Seed struct {
	// Path is the seed's absolute path.
	Path string
	// Slot is the seed slot's name, held warming until Seeder.Done; "" for the main checkout.
	Slot string
}

// ErrNoSeed is the error Choose wraps when no candidate qualifies: nothing is cloned, and the
// caller waits or fails as for a full pool, saying why (A19).
var ErrNoSeed = errors.New("no seed qualifies")

// Seeder chooses seeds for the pool ~/.upool/pools/<Key>/state.json under Home, as docs/contracts.md
// (g) step 0 and A19 say: the first candidate with no live Editor that passes the staleness check,
// from the freshest ready slot, a needs-reset slot after its reset, and the main checkout. take
// clones from its choice, as the top-up (#79) and `init --slots` (#64) will.
type Seeder struct {
	Git git.Client
	// Proc reads OwnerPID's start time and says whether an Editor's PID is a process.
	Proc proc.Checker
	// OwnerPID is the cloning process, recorded as a slot seed's owner while it is warming
	// ((f) transition 23; A4).
	OwnerPID  int
	Home, Key string
}

// Choose returns the seed for the next clone, and why each candidate it passed over did not
// qualify, as "<candidate>: <reason>" lines. A slot it returns is held as warming, with OwnerPID
// as its owner, until Done; the main checkout is held by nothing. With no candidate qualifying it
// returns an error wrapping ErrNoSeed, and the lines say why.
//
// The freshest ready slot is the one whose Library/ScriptAssemblies is newest. A needs-reset
// slot is reset, `git checkout --detach <base>` and `git clean -fd`, and made ready first ((f)
// transition 20); one that the reset leaves stale falls through to the main checkout, since
// refreshing a stale seed arrives in M4 (A19).
func (sd Seeder) Choose(ctx context.Context) (Seed, []string, error) {
	p, err := pool.Load(sd.Home, sd.Key)
	if err != nil {
		return Seed{}, nil, err
	}
	editors, err := ledger.Load(sd.Home)
	if err != nil {
		return Seed{}, nil, err
	}
	var skipped []string
	skip := func(name, reason string) { skipped = append(skipped, name+": "+reason) }

	// The ready slots, freshest first, then the needs-reset slots once reset.
	type candidate struct {
		slot      pool.Slot
		assembled time.Time
	}
	var ready []candidate
	for _, s := range p.Slots {
		if s.State != pool.StateReady || s.Lease != nil {
			continue
		}
		if info, err := os.Stat(s.Path); err != nil || !info.IsDir() {
			skip(s.Name, "it is not on disk")
			continue
		}
		st, why, err := sd.qualifies(ctx, editors, s.Path)
		if err != nil {
			return Seed{}, nil, err
		}
		if why != "" {
			skip(s.Name, why)
			continue
		}
		ready = append(ready, candidate{s, st.AssembliesTime})
	}
	slices.SortStableFunc(ready, func(a, b candidate) int {
		return b.assembled.Compare(a.assembled)
	})
	for _, c := range ready {
		held, err := sd.hold(ctx, c.slot)
		if err != nil {
			return Seed{}, nil, err
		}
		if held {
			return Seed{Path: c.slot.Path, Slot: c.slot.Name}, skipped, nil
		}
		skip(c.slot.Name, "taken meanwhile")
	}
	for _, s := range p.Slots {
		if s.State != pool.StateNeedsReset {
			continue
		}
		reset, err := sd.reset(ctx, p, s)
		if err != nil {
			return Seed{}, nil, err
		}
		if !reset {
			skip(s.Name, "taken meanwhile")
			continue
		}
		_, why, err := sd.qualifies(ctx, editors, s.Path)
		if err != nil {
			return Seed{}, nil, err
		}
		if why != "" {
			skip(s.Name, "after its reset, "+why)
			continue
		}
		s.State = pool.StateReady
		held, err := sd.hold(ctx, s)
		if err != nil {
			return Seed{}, nil, err
		}
		if held {
			return Seed{Path: s.Path, Slot: s.Name}, skipped, nil
		}
		skip(s.Name, "taken meanwhile")
	}

	_, why, err := sd.qualifies(ctx, editors, p.Repo)
	if err != nil {
		return Seed{}, nil, err
	}
	if why == "" {
		return Seed{Path: p.Repo}, skipped, nil
	}
	skip("the main checkout", why)
	return Seed{}, skipped, fmt.Errorf("%w for %s: %s", ErrNoSeed, p.Repo,
		strings.Join(skipped, "; "))
}

// qualifies runs the checks every candidate must pass, and returns why it does not, or "": no
// live Editor, by the ledger or by the instance files an open Editor keeps in its Library, and a
// Library current with its scripts ((g) steps 0 and 1).
func (sd Seeder) qualifies(ctx context.Context, editors ledger.Ledger,
	dir string) (Staleness, string, error) {
	live, err := editors.LiveIn(sd.Proc, dir)
	if err != nil {
		return Staleness{}, "", err
	}
	if live {
		return Staleness{}, "an Editor upool opened has it open", nil
	}
	for _, name := range instanceFiles {
		_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
		if err == nil {
			return Staleness{}, "the Unity Editor has it open (" + name + " exists)", nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Staleness{}, "", fmt.Errorf("checking for %s in %s: %w", name, dir, err)
		}
	}
	st, err := CheckStaleness(ctx, sd.Git, dir)
	if err != nil {
		return Staleness{}, "", err
	}
	if st.Stale() {
		return st, "its Library is stale: " + st.Reason(), nil
	}
	return st, "", nil
}

// hold moves the ready slot s to warming, with OwnerPID as its owner, if state.json still has it
// ready and unleased, and reports whether it did ((f) transition 23).
func (sd Seeder) hold(ctx context.Context, s pool.Slot) (bool, error) {
	start, err := sd.Proc.Start(sd.OwnerPID)
	if err != nil {
		return false, fmt.Errorf("reading the start time of process %d: %w", sd.OwnerPID, err)
	}
	held := false
	err = pool.Update(ctx, sd.Home, sd.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(c pool.Slot) bool { return c.Name == s.Name })
		if i < 0 || p.Slots[i].State != pool.StateReady || p.Slots[i].Lease != nil {
			return nil
		}
		state, err := pool.Next(p.Slots[i], pool.Seed{})
		if err != nil {
			return err
		}
		pid := sd.OwnerPID
		p.Slots[i].State, p.Slots[i].OwnerPID, p.Slots[i].OwnerPIDStart = state, &pid, &start
		held = true
		return nil
	})
	return held, err
}

// reset resets the needs-reset slot s of p to base and makes it ready, if state.json still has it
// needs-reset, and reports whether it did ((f) transition 20).
func (sd Seeder) reset(ctx context.Context, p pool.Pool, s pool.Slot) (bool, error) {
	if err := pool.ResetToBase(ctx, sd.Git, s.Path, p.Base); err != nil {
		return false, err
	}
	done := false
	err := pool.Update(ctx, sd.Home, sd.Key, func(cur *pool.Pool) error {
		i := slices.IndexFunc(cur.Slots, func(c pool.Slot) bool { return c.Name == s.Name })
		if i < 0 || cur.Slots[i].State != pool.StateNeedsReset {
			return nil
		}
		state, err := pool.Next(cur.Slots[i], pool.Refresh{})
		if err != nil {
			return err
		}
		cur.Slots[i].State, done = state, true
		return nil
	})
	return done, err
}

// Done returns a slot seed to ready once the copy has read it ((f) transition 18): the copy only
// reads the seed, so it is as clean as it was when Choose held it. The main checkout needs
// nothing.
func (sd Seeder) Done(ctx context.Context, seed Seed) error {
	if seed.Slot == "" {
		return nil
	}
	return pool.Update(ctx, sd.Home, sd.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(c pool.Slot) bool { return c.Name == seed.Slot })
		if i < 0 {
			return fmt.Errorf("the seed %s left state.json meanwhile", seed.Slot)
		}
		s := &p.Slots[i]
		if s.State != pool.StateWarming || s.OwnerPID == nil || *s.OwnerPID != sd.OwnerPID {
			return fmt.Errorf("the seed %s is %s, no longer held by process %d", seed.Slot,
				s.State, sd.OwnerPID)
		}
		state, err := pool.Next(*s, pool.WarmingFinish{Clean: true})
		if err != nil {
			return err
		}
		s.State, s.OwnerPID, s.OwnerPIDStart = state, nil, nil
		return nil
	})
}
