package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// pooledWait is how long the pooled branch waits for a slot: the smaller of 20 s and the desktop
// app's own limit less 5 s, so that the hook fails with upool's message before the app gives up.
// The app waited out a create hook that slept 30 s, so its limit is above that, and the wait is
// 20 s (A11; testdata/captures/hooks/desktop-create-timeout.txt).
const pooledWait = 20 * time.Second

// pooledRetry is how often the pooled branch looks again for a slot while it waits (docs/design.md,
// "Leases that survive crashes and never reclaim a live tree": pool-full is a wait, not a failure).
const pooledRetry = time.Second

// localPrefix is what the desktop app's log puts before a session's id; a create stdin's
// session_id lacks it, but the lease's holder is the bare id either way (docs/contracts.md (a)).
const localPrefix = "local_"

// pooledWorktree is the pooled branch, for a session in repo, a registered pool: it hands the
// session a free slot in milliseconds, with the create hook's unconfirmed lease, held by the
// session's id with its name as a hint (A6), which SessionStart confirms (docs/design.md, "Route
// Claude Code's worktrees through the pool: three branches in one hook"). A slot is free when it is
// ready, or else needs-reset (A5), is still on disk and in `git worktree list`, is not quarantined,
// and is not a worktree the desktop app still records for another session, whose path it would
// refuse (A50; #192). With none free
// it tries again once a second for pooledWait, then fails, which aborts the session, naming upool
// refresh. It runs in the desktop app's helper, so it opens no file under the repository: a stat
// and git are all it uses there (docs/contracts.md (a)).
func pooledWorktree(ctx context.Context, env Env, req Request, repo registry.Repo) (string,
	error) {
	p, err := pool.Load(env.Home, repo.Key)
	if err != nil {
		return "", err
	}
	checkout := p.Repo
	if checkout == "" {
		checkout = repo.Path
	}
	// The repository's .upool.toml is a file under it, so confirm_timeout comes from the user
	// file, the environment or the default (docs/contracts.md (e), `~/.upool/registry.json`).
	cfg, err := config.Load(config.Inputs{Getenv: env.Getenv, Home: env.Home})
	if err != nil {
		return "", err
	}
	timeout := cfg.ConfirmTimeout.Value
	deadline := env.Now().Add(pooledWait)
	told := map[string]bool{}
	for {
		present, err := presentSlots(ctx, env, checkout)
		if err != nil {
			return "", err
		}
		present = notRecorded(env, present, told)
		s, expired, err := pool.UnconfirmedTake{Holder: strings.TrimPrefix(req.SessionID,
			localPrefix), NameHint: req.Name, Now: env.Now(), ConfirmTimeout: timeout,
			Present: present}.Take(ctx, env.Home, repo.Key)
		for _, line := range pool.EndExpired(ctx, env.Git, checkout, expired, timeout) {
			// stderr is for warnings only; the app takes the worktree path from stdout.
			_, _ = fmt.Fprintln(env.Stderr, "upool: "+line)
		}
		if err == nil {
			return s.Path, nil
		}
		if !errors.Is(err, pool.ErrNoSlot) {
			return "", err
		}
		if !env.Now().Before(deadline) {
			return "", fmt.Errorf("no slot of %s was free within %s: run upool refresh in %s to "+
				"top the pool up, or release a slot", checkout, pooledWait, checkout)
		}
		time.Sleep(pooledRetry)
	}
}

// notRecorded returns present, passing over too each slot the desktop app's record names, which
// the app would refuse as "already in use by worktree …" (A50), and saying so once per slot in
// told. A record upool cannot read passes over nothing: the slot's quarantine is the fallback.
func notRecorded(env Env, present func(pool.Slot) bool, told map[string]bool) func(pool.Slot) bool {
	rec := desktop.Load(env.Home)
	return func(s pool.Slot) bool {
		if !present(s) {
			return false
		}
		if name, held := rec.Holds(s.Path); held {
			if !told[s.Name] {
				told[s.Name] = true
				// stderr is for warnings only; the app takes the worktree path from stdout.
				_, _ = fmt.Fprintf(env.Stderr, "upool: passing over %s: the desktop app still "+
					"records it as the worktree %s of another session\n", s.Name, name)
			}
			return false
		}
		return true
	}
}

// presentSlots returns whether a slot is on disk, by a stat, and in the repository's
// `git worktree list`, by canonical key: what the helper can check of a slot without reading a file
// in it, such as the .git file take reads for the backlink.
func presentSlots(ctx context.Context, env Env, checkout string) (func(pool.Slot) bool, error) {
	wts, err := env.Git.WorktreeList(ctx, checkout)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, wt := range wts {
		key, err := paths.Key(wt.Path)
		if err != nil {
			return nil, err
		}
		listed[key] = true
	}
	return func(s pool.Slot) bool {
		// A stat, which worked from the helper under ~/Documents (docs/contracts.md (a)).
		info, err := os.Stat(s.Path)
		if err != nil || !info.IsDir() {
			return false
		}
		key, err := paths.Key(s.Path)
		return err == nil && listed[key]
	}, nil
}
