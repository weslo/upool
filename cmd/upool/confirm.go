package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// agentFiles are the files and directories of a checkout that agents read, which a slot gets from
// the main checkout at each confirm: gitignored ones a clone carries as they were, and the app's
// overlay skips when the slot already has them, so a slot's copy goes stale (docs/design.md, "Slot
// lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without `-x`";
// docs/contracts.md (b), the `take --confirm` row). .claude is copied entry by entry, without its
// worktrees, where the slots themselves live.
var agentFiles = []string{"CLAUDE.md", "AGENTS.md", ".agents", ".claude"}

// claudeDir is the agent file copied entry by entry, and worktreesEntry the entry of it that is
// neither removed from a slot nor copied into one.
const (
	claudeDir      = ".claude"
	worktreesEntry = "worktrees"
)

// confirmReport is what take --confirm did: the data of its --json envelope, whose human form is
// the slot's path alone, as take's is.
type confirmReport struct {
	Path    string `json:"path"`
	Slot    string `json:"slot"`
	LeaseID string `json:"lease_id"`
	Repo    string `json:"repo"`
	// Reset says whether the confirm ran the slot's deferred reset (A23).
	Reset bool `json:"reset"`
}

// Human returns the slot's path.
func (r confirmReport) Human() string { return r.Path }

// confirm is `upool take --confirm <path>`, which SessionStart runs for the session's slot
// (docs/design.md, "Environment, confirmation and release"; docs/contracts.md (b); (f) transition
// 6). It finds the slot by its canonical path, takes the lease's git worktree lock if the take did
// not, runs the deferred reset of a slot handed out from needs-reset, confirms the lease with this
// process's parent, the session's CLI, as its holder (A6), and re-copies the agent files from the
// main checkout. A confirmed lease is confirmed again, re-copying again. otherFlags is whether a
// flag of take's other form was given, which --confirm refuses.
func confirm(inv *invocation, path string, otherFlags bool) (any, []string, error) {
	if len(inv.args) > 0 || otherFlags {
		return nil, nil, fmt.Errorf("take --confirm takes a path alone; usage: upool %s",
			takeUsage)
	}
	ctx := context.Background()
	rp, s, err := confirmTarget(inv.home, path)
	if err != nil {
		return nil, nil, err
	}
	repo := rp.pool.Repo
	if repo == "" {
		repo = rp.repo.Path
	}
	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
	if err := lockLease(ctx, g, repo, s); err != nil {
		return nil, nil, err
	}

	var warnings []string
	reset, reimport := false, false
	if s.Lease.ResetPending {
		changed, note, err := deferredReset(ctx, inv, g, s, rp.pool.Base)
		if err != nil {
			return nil, nil, err
		}
		if note != "" {
			warnings = append(warnings, note)
		}
		if changed {
			warnings = append(warnings, clone.ReimportWarning(s.Name))
		}
		reset, reimport = true, changed
	}

	holderPID, holderStart, err := confirmHolder(inv.ppid)
	if err != nil {
		return nil, nil, err
	}
	err = pool.Update(ctx, inv.home, rp.repo.Key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		cur := &p.Slots[i]
		if cur.Lease == nil || cur.Lease.LeaseID != s.Lease.LeaseID {
			return output.Fail(output.CodeCommandFailed, "%s's lease changed meanwhile: it is "+
				"%s now, and nothing was confirmed", s.Name, cur.State)
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
		if reset {
			cur.Lease.ResetPending = false
		}
		if reimport {
			cur.ReimportPending = true
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if err := copyAgentFiles(ctx, clone.CP{}, repo, s.Path); err != nil {
		return nil, warnings, err
	}
	return confirmReport{Path: s.Path, Slot: s.Name, LeaseID: s.Lease.LeaseID, Repo: repo,
		Reset: reset}, warnings, nil
}

// confirmTarget finds the slot whose path is path's canonical key in the registered pools, since
// the desktop app may hand a path back lowercased, and refuses one that holds no lease.
func confirmTarget(home, path string) (registeredPool, pool.Slot, error) {
	want, err := paths.Key(path)
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	repos, err := registry.List(home)
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	for _, repo := range repos {
		p, err := pool.Load(home, repo.Key)
		if err != nil {
			return registeredPool{}, pool.Slot{}, err
		}
		for _, s := range p.Slots {
			k, err := paths.Key(s.Path)
			if err != nil {
				return registeredPool{}, pool.Slot{}, err
			}
			if k != want {
				continue
			}
			if s.Lease == nil || (s.State != pool.StateLeasedUnconfirmed &&
				s.State != pool.StateLeased) {
				return registeredPool{}, pool.Slot{}, output.Fail(output.CodeCommandFailed,
					"%s is %s, not leased, so there is no lease to confirm", s.Name, s.State)
			}
			return registeredPool{repo: repo, pool: p}, s, nil
		}
	}
	return registeredPool{}, pool.Slot{}, output.Fail(output.CodeCommandFailed,
		"%s is not a slot of any registered pool", path)
}

// lockLease takes the git worktree lock the lease on s holds for its lifetime, unless git already
// has the slot locked (docs/design.md, "Leases that survive crashes and never reclaim a live
// tree").
func lockLease(ctx context.Context, g git.Client, repo string, s pool.Slot) error {
	key, err := paths.Key(s.Path)
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
			return nil
		}
	}
	return g.WorktreeLock(ctx, repo, s.Path, leaseLockReason+s.Lease.LeaseID)
}

// deferredReset is the reset a slot handed out from needs-reset still owes (A5; A23): define-order
// noise reverted, then `git checkout --detach <base>` and `git clean -fd`, with the slot's cache
// key compared across the checkout (A20). The slot was clean when it was marked needs-reset; one
// that is not clean now is refused, with nothing reset, rather than have git clean take its files.
func deferredReset(ctx context.Context, inv *invocation, g git.Client, s pool.Slot,
	base string) (changed bool, note string, err error) {
	if _, err := pool.RevertNoise(ctx, g, s.Path); err != nil {
		return false, "", err
	}
	dirty, err := pool.Dirty(ctx, g, s.Path)
	if err != nil {
		return false, "", err
	}
	if len(dirty) > 0 {
		return false, "", output.Fail(output.CodeCommandFailed, "%s needs its deferred reset "+
			"but is dirty, so nothing was reset or confirmed: %v", s.Name, dirty)
	}
	keys := clone.KeyCheck{Env: inv.environ()}
	return keys.Around(ctx, s.Path, func() error { return pool.ResetToBase(ctx, g, s.Path, base) })
}

// confirmHolder returns the process the confirmed lease follows, upool's parent, and its start
// time (A6). Off macOS, where internal/proc reads no start times, the lease records no holder, as
// `upool run`'s does there.
func confirmHolder(ppid int) (*int, *time.Time, error) {
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

// copyAgentFiles replaces the slot's agent files with the main checkout's, through copier: each
// entry of agentFiles the slot has is removed, and the main checkout's copied in its place, so an
// entry the main checkout lacks is gone from the slot afterwards. .claude goes entry by entry,
// leaving the slot's .claude/worktrees alone and never copying the main checkout's, where the
// slots are. Links are copied as links, so relative ones stay relative (spike §10).
func copyAgentFiles(ctx context.Context, copier clone.Copier, repo, slot string) error {
	for _, name := range agentFiles {
		if name == claudeDir {
			if err := copyClaudeDir(ctx, copier, repo, slot); err != nil {
				return err
			}
			continue
		}
		if err := replaceEntry(ctx, copier, filepath.Join(repo, name),
			filepath.Join(slot, name)); err != nil {
			return err
		}
	}
	return nil
}

// copyClaudeDir replaces each entry of the slot's .claude but worktrees with the main checkout's.
func copyClaudeDir(ctx context.Context, copier clone.Copier, repo, slot string) error {
	src, dst := filepath.Join(repo, claudeDir), filepath.Join(slot, claudeDir)
	names := map[string]bool{}
	for _, dir := range []string{src, dst} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.Name() != worktreesEntry {
				names[e.Name()] = true
			}
		}
	}
	if _, err := os.Lstat(src); err == nil {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dst, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(names)) {
		if err := replaceEntry(ctx, copier, filepath.Join(src, name),
			filepath.Join(dst, name)); err != nil {
			return err
		}
	}
	return nil
}

// replaceEntry removes dst and, if src exists, copies it there.
func replaceEntry(ctx context.Context, copier clone.Copier, src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("removing %s: %w", dst, err)
	}
	if _, err := os.Lstat(src); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", src, err)
	}
	return copier.Copy(ctx, src, dst)
}
