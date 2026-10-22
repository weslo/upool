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
	// Keys compares a needs-reset slot's cache key across its reset (A20), and Warn, when it is
	// not nil, is told what the reset and a refresh warned.
	Keys KeyCheck
	Warn func(line string)
	// Refresher refreshes a candidate whose Library is stale ((g) step 1; #98); with none, a stale
	// candidate is passed over.
	Refresher *SeedRefresher
}

// Choose returns the seed for the next clone, and why each candidate it passed over did not
// qualify, as "<candidate>: <reason>" lines. A slot it returns is held as warming, with OwnerPID
// as its owner, until Done; the main checkout is held by nothing. With no candidate qualifying it
// returns an error wrapping ErrNoSeed, and the lines say why.
//
// The freshest ready slot is the one its Editor compiled last (Staleness.Compiled). A needs-reset
// slot is reset and made ready first, as ResetNeedsReset does it ((f) transition 20); one that is
// dirty falls through to the main checkout. A candidate whose Library is stale is refreshed, a
// slot while Choose holds it, and checked again: it is the seed if it is current, and otherwise,
// or if the refresh fails, the next candidate is tried ((g) steps 0 and 1; A19; #98).
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
		stale     Staleness
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
		ready = append(ready, candidate{s, st.Compiled(), st})
	}
	slices.SortStableFunc(ready, func(a, b candidate) int {
		return b.assembled.Compare(a.assembled)
	})
	for _, c := range ready {
		held, err := sd.hold(ctx, c.slot)
		if err != nil {
			return Seed{}, nil, err
		}
		if !held {
			skip(c.slot.Name, "taken meanwhile")
			continue
		}
		seed := Seed{Path: c.slot.Path, Slot: c.slot.Name}
		why, err := sd.current(ctx, seed, c.stale)
		if err != nil || why == "" {
			return seed, skipped, err
		}
		skip(c.slot.Name, why)
	}
	for _, s := range p.Slots {
		if s.State != pool.StateNeedsReset {
			continue
		}
		reset, why, warnings, err := ResetNeedsReset(ctx, sd.Git, sd.Keys, sd.Home, sd.Key,
			s.Name)
		if err != nil {
			return Seed{}, nil, err
		}
		for _, w := range warnings {
			if sd.Warn != nil {
				sd.Warn(w)
			}
		}
		if !reset {
			skip(s.Name, why)
			continue
		}
		st, why, err := sd.qualifies(ctx, editors, s.Path)
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
		if !held {
			skip(s.Name, "taken meanwhile")
			continue
		}
		seed := Seed{Path: s.Path, Slot: s.Name}
		why, err = sd.current(ctx, seed, st)
		if err != nil || why == "" {
			return seed, skipped, err
		}
		skip(s.Name, "after its reset, "+why)
	}

	st, why, err := sd.qualifies(ctx, editors, p.Repo)
	if err != nil {
		return Seed{}, nil, err
	}
	if why == "" {
		seed := Seed{Path: p.Repo}
		if why, err = sd.current(ctx, seed, st); err != nil || why == "" {
			return seed, skipped, err
		}
	}
	skip("the main checkout", why)
	return Seed{}, skipped, fmt.Errorf("%w for %s: %s", ErrNoSeed, p.Repo,
		strings.Join(skipped, "; "))
}

// qualifies runs the checks every candidate must pass before it is held, and returns its
// staleness and why it does not pass, or "": no live Editor, by the ledger or by the instance
// files an open Editor keeps in its Library ((g) step 0). A stale Library is current's to deal
// with.
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
	return st, "", nil
}

// current returns "" when seed, a candidate that qualifies with staleness st, may be cloned:
// its Library is current, or Refresher refreshed it and the check, run again, finds it current
// ((g) step 1; #98). A slot's reimport_pending makes its refresh a cold import, and a refresh that
// brings it current clears the flag, as the open that does the import does (A20; A39). Otherwise
// current returns why not, for Choose to try the next candidate, and gives back a slot seed it
// holds, as ready. A slot seed is clean when it is held, and its refresh's Editor may leave
// define-order noise, which is reverted. A slot its refresh left dirty otherwise, or with an
// Editor that could not be closed, is passed over without being given back, so that it stays
// warming until this process has exited and no Editor is live in it ((f) transition 22). Only a
// failure to read or write the seed or the pool is its error.
func (sd Seeder) current(ctx context.Context, seed Seed, st Staleness) (string, error) {
	if !st.Stale() {
		return "", nil
	}
	if sd.Refresher == nil {
		return sd.passOver(ctx, seed, "its Library is stale: "+st.Reason())
	}
	pending, err := sd.pending(seed)
	if err != nil {
		return "", err
	}
	warnings, err := sd.Refresher.Refresh(ctx, seed.Path, pending)
	for _, w := range warnings {
		if sd.Warn != nil {
			sd.Warn(w)
		}
	}
	why := ""
	switch {
	case errors.Is(err, ErrRefreshedOnce):
		why = "its Library is stale, and " + err.Error()
	case err != nil:
		why = "refreshing its stale Library failed: " + err.Error()
	default:
		st, err := CheckStaleness(ctx, sd.Git, seed.Path)
		if err != nil {
			return "", err
		}
		if st.Stale() {
			why = "its Library is still stale after its refresh: " + st.Reason()
		}
	}
	if seed.Slot != "" && errors.Is(err, ErrEditorLeft) {
		return why + "; it stays warming until the Editor is gone", nil
	}
	if seed.Slot != "" {
		if _, err := pool.RevertNoise(ctx, sd.Git, seed.Path); err != nil {
			return "", err
		}
		dirty, err := pool.Dirty(ctx, sd.Git, seed.Path)
		if err != nil {
			return "", err
		}
		if len(dirty) > 0 {
			return "its refresh left it dirty, so it stays warming until this process exits " +
				"and is then parked: " + strings.Join(dirty, "; "), nil
		}
	}
	if why == "" {
		if pending {
			return "", sd.clearPending(ctx, seed)
		}
		return "", nil
	}
	return sd.passOver(ctx, seed, why)
}

// pending returns the reimport_pending of the slot seed, or false for the main checkout.
func (sd Seeder) pending(seed Seed) (bool, error) {
	if seed.Slot == "" {
		return false, nil
	}
	p, err := pool.Load(sd.Home, sd.Key)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(p.Slots, func(s pool.Slot) bool { return s.Name == seed.Slot })
	return i >= 0 && p.Slots[i].ReimportPending, nil
}

// clearPending clears the reimport_pending of the slot seed, whose refresh has done the import
// the flag announced (A39).
func (sd Seeder) clearPending(ctx context.Context, seed Seed) error {
	return pool.Update(ctx, sd.Home, sd.Key, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == seed.Slot {
				p.Slots[i].ReimportPending = false
			}
		}
		return nil
	})
}

// passOver gives back seed, when it is a slot Choose holds, as ready, and returns why it was
// passed over.
func (sd Seeder) passOver(ctx context.Context, seed Seed, why string) (string, error) {
	if seed.Slot == "" {
		return why, nil
	}
	return why, sd.Done(ctx, seed)
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
