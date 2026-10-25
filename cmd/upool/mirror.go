package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/mirror"
	"github.com/weslo/upool/internal/pool"
)

// mirroredSlot is what mirroring .worktreeinclude's files did to one free slot, by paths relative
// to it.
type mirroredSlot struct {
	Slot    string   `json:"slot"`
	Copied  []string `json:"copied"`
	Deleted []string `json:"deleted"`
}

// describeMirrored says what the mirror did to m.
func describeMirrored(m mirroredSlot) string {
	return fmt.Sprintf("mirrored %s's files into %s: %d copied, %d deleted", mirror.File, m.Slot,
		len(m.Copied), len(m.Deleted))
}

// mirrorFree mirrors the gitignored files the main checkout repo's .worktreeinclude names into each
// free slot of the pool key: ready or needs-reset, unleased, and with no Editor open, so that the
// next session handed one has them before Claude Code reads its CLAUDE.md (A44; #233;
// internal/mirror). The top-up that every session start launches and `upool release` run it,
// rather than the confirm at session start, which comes too late for the session it confirms. It
// returns the slots it changed. A slot it could not mirror is a warning, as a needs-reset slot the
// top-up could not reset is; only a failure to read the pool or the ledger is its error.
func mirrorFree(ctx context.Context, inv *invocation, g git.Client, repo, key string,
	warn func(string)) ([]mirroredSlot, error) {
	p, err := pool.Load(inv.home, key)
	if err != nil {
		return nil, err
	}
	editors, err := ledger.Load(inv.home)
	if err != nil {
		return nil, err
	}
	var out []mirroredSlot
	for _, name := range slotNamesIn(p) {
		// Each slot is read again just before its mirror, since a handout may have taken it
		// meanwhile; one taken during its mirror gets the main checkout's files all the same.
		p, err := pool.Load(inv.home, key)
		if err != nil {
			return out, err
		}
		i := slices.IndexFunc(p.Slots, func(s pool.Slot) bool { return s.Name == name })
		if i < 0 {
			continue
		}
		s := p.Slots[i]
		if s.Lease != nil || s.State != pool.StateReady && s.State != pool.StateNeedsReset {
			continue
		}
		live, err := editors.LiveIn(inv.checker(), s.Path)
		if err != nil {
			return out, err
		}
		if live {
			continue
		}
		r, err := mirror.Mirror(ctx, g, clone.CP{}, repo, s.Path)
		if err != nil {
			warn(fmt.Sprintf("did not finish mirroring %s's files into %s: %v", mirror.File,
				s.Name, err))
		}
		if r.Changed() {
			out = append(out, mirroredSlot{Slot: s.Name, Copied: r.Copied, Deleted: r.Deleted})
		}
	}
	return out, nil
}

// slotNamesIn returns the names of p's slots, in its order.
func slotNamesIn(p pool.Pool) []string {
	names := make([]string, 0, len(p.Slots))
	for _, s := range p.Slots {
		names = append(names, s.Name)
	}
	return names
}
