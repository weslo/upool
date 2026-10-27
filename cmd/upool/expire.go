package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// expireLeases ends the expired unconfirmed leases of the registered pools keep accepts, before
// status or doctor reads them (docs/contracts.md (f) transition 7; A23; #75). A pool with an
// unconfirmed lease has its confirm_timeout loaded and, if a lease has outlived it, its state.json
// rewritten under the lock; a pool without one is not written, so a read that finds nothing to
// expire changes nothing. It returns a line for each lease it ended, and for each pool whose
// configuration could not be read, whose leases it leaves for the next read.
func expireLeases(ctx context.Context, inv *invocation, keep func(registry.Repo) bool) ([]string,
	error) {
	repos, err := registry.List(inv.home)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, repo := range repos {
		if !keep(repo) {
			continue
		}
		p, err := pool.Load(inv.home, repo.Key)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(p.Slots, func(s pool.Slot) bool {
			return s.State == pool.StateLeasedUnconfirmed
		}) {
			continue
		}
		checkout := p.Repo
		if checkout == "" {
			checkout = repo.Path
		}
		cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: checkout, Home: inv.home})
		if err != nil {
			notes = append(notes, fmt.Sprintf("not ending the unconfirmed leases of %s: %v",
				checkout, err))
			continue
		}
		now, timeout := inv.now(), cfg.ConfirmTimeout.Value
		if !slices.ContainsFunc(p.Slots, func(s pool.Slot) bool {
			return s.ConfirmExpired(now, timeout)
		}) {
			continue
		}
		var expired []pool.Slot
		// An expiry counts towards quarantine only while the app's record cannot be read (A50).
		count := !desktop.Load(inv.home).Known
		err = pool.Update(ctx, inv.home, repo.Key, func(p *pool.Pool) error {
			var err error
			expired, err = p.ExpireUnconfirmed(now, timeout, count)
			return err
		})
		if err != nil {
			return nil, err
		}
		g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
		notes = append(notes, pool.EndExpired(ctx, g, checkout, expired, timeout)...)
	}
	return notes, nil
}

// recoverWarming checks again the warming slots whose owner died in the registered pools keep
// accepts, before status or doctor reads them, as take does before it chooses a slot
// (docs/contracts.md (f) transition 22; A4; #97): a clean slot goes back to ready and a dirty one
// is parked, once the ledger shows no live Editor in it. A pool without a warming slot is not
// written. It returns a line for each slot it checked again.
func recoverWarming(ctx context.Context, inv *invocation, keep func(registry.Repo) bool) ([]string,
	error) {
	repos, err := registry.List(inv.home)
	if err != nil {
		return nil, err
	}
	var notes []string
	for _, repo := range repos {
		if !keep(repo) {
			continue
		}
		p, err := pool.Load(inv.home, repo.Key)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(p.Slots, func(s pool.Slot) bool {
			return s.State == pool.StateWarming
		}) {
			continue
		}
		r := pool.Recoverer{Git: git.Client{Runner: git.Exec{}, Env: inv.environ()},
			Proc: inv.checker(), Home: inv.home, Key: repo.Key}
		done, err := r.Recover(ctx)
		if err != nil {
			return nil, err
		}
		for _, rc := range done {
			notes = append(notes, recoveredLine(rc))
		}
	}
	return notes, nil
}

// recoveredLine says what checking a warming slot again did with it.
func recoveredLine(rc pool.Recovered) string {
	gone := "its owner is gone"
	if rc.OwnerPID != 0 {
		gone = fmt.Sprintf("its owner, process %d, is gone", rc.OwnerPID)
	}
	switch {
	case rc.Err != nil:
		return fmt.Sprintf("could not check %s again, though it is warming and %s: %v", rc.Slot,
			gone, rc.Err)
	case rc.State == pool.StateParked:
		return fmt.Sprintf("parked %s, which was warming, since %s and it is dirty: %s", rc.Slot,
			gone, strings.Join(rc.Dirty, "; "))
	}
	return fmt.Sprintf("returned %s, which was warming, to ready, since %s", rc.Slot, gone)
}
