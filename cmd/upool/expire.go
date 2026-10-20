package main

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/weslo/upool/internal/config"
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
		err = pool.Update(ctx, inv.home, repo.Key, func(p *pool.Pool) error {
			var err error
			expired, err = p.ExpireUnconfirmed(now, timeout)
			return err
		})
		if err != nil {
			return nil, err
		}
		g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
		notes = append(notes, endExpired(ctx, g, checkout, expired, timeout)...)
	}
	return notes, nil
}

// endExpired finishes the unconfirmed leases an expiry ended in the pool whose main checkout is
// repo, once state.json no longer holds them: it releases each slot's git worktree lock, if the
// take that handed the slot out held one, and returns a line saying what became of each slot.
func endExpired(ctx context.Context, g git.Client, repo string, expired []pool.Slot,
	timeout time.Duration) []string {
	var lines []string
	for _, s := range expired {
		state := pool.StateReady
		if s.Lease.ResetPending {
			state = pool.StateNeedsReset
		}
		line := fmt.Sprintf("ended the unconfirmed lease on %s, which no session confirmed "+
			"within %s: %s is %s again", s.Name, timeout, s.Name, state)
		if err := pool.Unlock(ctx, g, repo, s.Path); err != nil {
			line += fmt.Sprintf(", but its git worktree lock stays: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
}
