package hooks

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
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
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
// (f) transition 6). It finds the slot by its canonical path, runs the deferred reset of a slot handed out from needs-reset, confirms the
// lease with env.ParentPID, the session's CLI, as its holder (A6), and re-copies the agent files
// from the main checkout. A confirmed lease is confirmed again, re-copying again. The warnings are
// for stderr.
func Confirm(ctx context.Context, env Env, path string) (Confirmed, []string, error) {
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
	if err := copyAgentFiles(ctx, clone.CP{}, repo, s.Path); err != nil {
		return Confirmed{}, warnings, err
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
