package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// pruneUsage is prune's usage line, which its handler also names when it refuses arguments.
const pruneUsage = "prune [--all] [--yes] [--json]"

// pruneCommand is `upool prune`: in the pool containing the working directory, or with --all in
// every registered pool, it lists the ready slots beyond `ready` that are idle, merged and clean,
// and with --yes removes them (docs/design.md, "One binary, a dozen commands, one JSON file per
// pool", the `prune` row; "Disk: 0.4 GB per persistent slot; disk is not what limits the count":
// dry run unless --yes, with no override flags; docs/contracts.md (f) transition 15; A31; #100).
// The disk budget, max_disk, and agent worktrees join it in later issues.
var pruneCommand = command{
	usage: pruneUsage,
	setup: func(fs *flag.FlagSet) handler {
		all := fs.Bool("all", false, "prune every registered pool")
		yes := fs.Bool("yes", false, "remove the slots listed, rather than only listing them")
		return func(inv *invocation) (any, []string, error) { return prune(inv, *all, *yes) }
	},
}

// pruneReport is what prune found and did: the data of its --json envelope.
type pruneReport struct {
	Pools []poolPrune `json:"pools"`
	// DryRun is whether prune only listed, without --yes.
	DryRun bool `json:"dry_run"`
}

// poolPrune is one pool's candidates, in state.json's order.
type poolPrune struct {
	Repo       string           `json:"repo"`
	Candidates []pruneCandidate `json:"candidates"`
}

// pruneCandidate is a slot prune may remove, and why.
type pruneCandidate struct {
	Slot string `json:"slot"`
	Path string `json:"path"`
	// IdleSince is its last_used or, with none, when its directory was made (A42).
	IdleSince time.Time `json:"idle_since"`
	Why       string    `json:"why"`
	// Removed is whether --yes removed it, and Error why it could not.
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// Human returns, for each pool, a line naming it, then a line per candidate: what prune would do,
// did or could not do with it, and why. A dry run that found candidates ends saying how to remove
// them.
func (r pruneReport) Human() string {
	var lines []string
	found := false
	for _, p := range r.Pools {
		if len(p.Candidates) == 0 {
			lines = append(lines, p.Repo+": nothing to prune")
			continue
		}
		lines = append(lines, p.Repo+":")
		for _, c := range p.Candidates {
			found = true
			switch {
			case r.DryRun:
				lines = append(lines, fmt.Sprintf("  would remove %s: %s", c.Slot, c.Why))
			case c.Removed:
				lines = append(lines, fmt.Sprintf("  removed %s: %s", c.Slot, c.Why))
			default:
				lines = append(lines, fmt.Sprintf("  did not remove %s: %s", c.Slot, c.Error))
			}
		}
	}
	if len(r.Pools) == 0 {
		lines = append(lines, "no pools are registered")
	}
	if r.DryRun && found {
		lines = append(lines, "this was a dry run: upool prune --yes removes them")
	}
	return strings.Join(lines, "\n")
}

// prune is prune's handler.
func prune(inv *invocation, all, yes bool) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			pruneUsage)
	}
	ctx := context.Background()
	keep := func(registry.Repo) bool { return true }
	var wd string
	if !all {
		var err error
		if wd, err = inv.getwd(); err != nil {
			return nil, nil, fmt.Errorf("reading the working directory: %w", err)
		}
		cwd, err := paths.Key(wd)
		if err != nil {
			return nil, nil, err
		}
		keep = func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) }
	}
	pools, err := readPools(ctx, inv.home, keep)
	if err != nil {
		return nil, nil, err
	}
	if !all {
		if len(pools) == 0 {
			return nil, nil, output.Fail(output.CodeCommandFailed,
				"%s is not inside a registered pool; run upool prune --all to prune every pool",
				wd)
		}
		pools = []registeredPool{innermost(pools)}
	}
	r := pruneReport{Pools: make([]poolPrune, 0, len(pools)), DryRun: !yes}
	for _, rp := range pools {
		pr := pruner{inv: inv, rp: rp, g: git.Client{Runner: git.Exec{}, Env: inv.environ()}}
		p, err := pr.candidates(ctx)
		if err != nil {
			return nil, nil, err
		}
		if yes {
			for i := range p.Candidates {
				pr.remove(ctx, &p.Candidates[i])
			}
		}
		r.Pools = append(r.Pools, p)
	}
	return r, nil, nil
}

// pruner prunes one registered pool.
type pruner struct {
	inv *invocation
	rp  registeredPool
	g   git.Client
}

// repo is the pool's main checkout.
func (pr pruner) repo() string {
	if pr.rp.pool.Repo != "" {
		return pr.rp.pool.Repo
	}
	return pr.rp.repo.Path
}

// idleSlot is a ready slot that no lease holds, with when it was last used.
type idleSlot struct {
	slot  pool.Slot
	since time.Time
}

// candidates returns the pool's slots that prune may remove, in state.json's order. The idle
// slots are the ready ones no lease holds whose directory exists and git lists; a slot missing
// from either is left to doctor, and takes no place among those kept. Of the idle slots, the
// `ready` most recently used are kept. Each of the rest is a candidate when all of these hold:
//   - it has been idle for shrink_after, since its last_used or, with none, since its directory
//     was made (A42);
//   - no Editor has its Library open, by the ledger or by the instance files ((g) step 0);
//   - it is clean, define-order noise aside ((f), the definition of clean), and holds no other
//     worktree, such as a pass-through or subagent worktree a session in it made (A13), whose
//     work git worktree remove would delete;
//   - its HEAD is merged into base, an ancestor of base's commit.
//
// Leased, parked and every other state are never candidates.
func (pr pruner) candidates(ctx context.Context) (poolPrune, error) {
	repo := pr.repo()
	r := poolPrune{Repo: repo, Candidates: []pruneCandidate{}}
	cfg, err := config.Load(config.Inputs{Getenv: pr.inv.getenv, Repo: repo, Home: pr.inv.home})
	if err != nil {
		return poolPrune{}, err
	}
	if cfg.Ready.Value < 0 {
		return poolPrune{}, output.Fail(output.CodeCommandFailed, "ready is %d for %s, from %s; "+
			"it must be 0 or more", cfg.Ready.Value, repo, cfg.Ready.Source)
	}
	listed, err := worktreeKeys(ctx, pr.g, repo)
	if err != nil {
		return poolPrune{}, err
	}
	var idle []idleSlot
	for _, s := range pr.rp.pool.Slots {
		if s.State != pool.StateReady || s.Lease != nil {
			continue
		}
		key, err := paths.Key(s.Path)
		if err != nil {
			return poolPrune{}, err
		}
		if info, err := os.Stat(s.Path); err != nil || !info.IsDir() || !listed[key] {
			continue
		}
		since := s.LastUsed
		if since.IsZero() {
			if since, err = clone.Made(s.Path); err != nil {
				return poolPrune{}, err
			}
		}
		idle = append(idle, idleSlot{s, since})
	}
	slices.SortStableFunc(idle, func(a, b idleSlot) int { return b.since.Compare(a.since) })
	if len(idle) <= cfg.Ready.Value {
		return r, nil
	}
	beyond := idle[cfg.Ready.Value:]

	base, err := pr.g.Commit(ctx, repo, pr.rp.pool.Base)
	if err != nil {
		return poolPrune{}, err
	}
	editors, err := ledger.Load(pr.inv.home)
	if err != nil {
		return poolPrune{}, err
	}
	now, shrink := pr.inv.now(), cfg.ShrinkAfter.Value
	for _, is := range beyond {
		s := is.slot
		idleFor := now.Sub(is.since)
		if idleFor < shrink {
			continue
		}
		why, err := pr.kept(ctx, editors, listed, s.Path)
		if err != nil {
			return poolPrune{}, err
		}
		if why != "" {
			continue
		}
		head, err := pr.g.Head(ctx, s.Path)
		if err != nil {
			return poolPrune{}, err
		}
		merged, err := pr.g.IsAncestor(ctx, repo, head, base)
		if err != nil {
			return poolPrune{}, err
		}
		if !merged {
			continue
		}
		r.Candidates = append(r.Candidates, pruneCandidate{Slot: s.Name, Path: s.Path,
			IdleSince: is.since.UTC(), Why: fmt.Sprintf("idle %s, past shrink_after %s; beyond "+
				"ready = %d; clean, and merged into %s", leaseAge(idleFor), leaseAge(shrink),
				cfg.Ready.Value, pr.rp.pool.Base)})
	}
	order := map[string]int{}
	for i, s := range pr.rp.pool.Slots {
		order[s.Name] = i
	}
	slices.SortStableFunc(r.Candidates, func(a, b pruneCandidate) int {
		return order[a.Slot] - order[b.Slot]
	})
	return r, nil
}

// kept returns why the slot at dir must stay whatever its age, or "": an Editor has its Library
// open, it is dirty, define-order noise aside, or it holds another worktree git lists, in listed
// by key. remove asks again once the slot is out of circulation.
func (pr pruner) kept(ctx context.Context, editors ledger.Ledger, listed map[string]bool,
	dir string) (string, error) {
	live, err := pr.editorIn(editors, dir)
	if err != nil || live {
		return "an Editor has it open", err
	}
	dirty, err := pool.DirtyBesideNoise(ctx, pr.g, dir)
	if err != nil || len(dirty) > 0 {
		return "it is dirty: " + strings.Join(dirty, "; "), err
	}
	key, err := paths.Key(dir)
	if err != nil {
		return "", err
	}
	for k := range listed {
		if k != key && paths.Within(k, key) {
			return "it holds the worktree " + k, nil
		}
	}
	return "", nil
}

// editorIn reports whether an Editor has the Library at dir open: one the ledger records with a
// running process, or any Editor, by the instance files it keeps there.
func (pr pruner) editorIn(editors ledger.Ledger, dir string) (bool, error) {
	live, err := editors.LiveIn(pr.inv.checker(), dir)
	if err != nil || live {
		return live, err
	}
	name, err := clone.InstanceFile(dir)
	return name != "", err
}

// remove removes the candidate c ((f) transition 15). Under the lock it takes the slot's entry out
// of state.json, if the slot is still ready, unleased and as prune read it, so that no take hands
// it out while it goes; a clone meanwhile does not reuse its name, which nextName keeps while the
// directory is on disk. Then it reverts define-order noise, asks again whether the slot must stay
// (kept), and runs `git worktree remove` without --force, which refuses a worktree with changes.
// If that fails before git has let go of the worktree, the entry is put back as it was and c says
// why. If git no longer lists it, only part of its directory went, and the entry stays out, since
// a slot git does not list cannot be handed out: c names what is left for a person to delete.
// Otherwise the slot's kept scene list goes too (A8).
func (pr pruner) remove(ctx context.Context, c *pruneCandidate) {
	home, key := pr.inv.home, pr.rp.repo.Key
	var taken pool.Slot
	err := pool.Update(ctx, home, key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(s pool.Slot) bool { return s.Name == c.Slot })
		if i < 0 {
			return fmt.Errorf("it left state.json meanwhile")
		}
		s := p.Slots[i]
		used := !s.LastUsed.IsZero() && !s.LastUsed.Equal(c.IdleSince)
		if s.Path != c.Path || s.Lease != nil || used {
			return fmt.Errorf("it changed in state.json meanwhile: it is %s", s.State)
		}
		if _, err := pool.Next(s, pool.Prune{}); err != nil {
			return fmt.Errorf("it is %s now", s.State)
		}
		taken = s
		p.Slots = slices.Delete(p.Slots, i, i+1)
		return nil
	})
	if err != nil {
		c.Error = err.Error()
		return
	}
	if err := pr.removeTaken(ctx, taken); err != nil {
		c.Error = err.Error()
		listed, lerr := worktreeKeys(ctx, pr.g, pr.repo())
		slotKey, kerr := paths.Key(taken.Path)
		if lerr == nil && kerr == nil && !listed[slotKey] {
			c.Error += "; git no longer lists it, so it is out of the pool: delete what is left " +
				"of " + taken.Path + " by hand"
			return
		}
		if perr := pool.Update(ctx, home, key, func(p *pool.Pool) error {
			if slices.ContainsFunc(p.Slots, func(s pool.Slot) bool {
				return s.Name == taken.Name
			}) {
				return fmt.Errorf("%s was added again meanwhile", taken.Name)
			}
			p.Slots = append(p.Slots, taken)
			return nil
		}); perr != nil {
			c.Error += "; putting its entry back: " + perr.Error()
		} else {
			c.Error += "; it stays ready"
		}
		return
	}
	c.Removed = true
	if err := pool.DropSceneList(ctx, home, key, taken.Name); err != nil {
		c.Error = "removed, but its kept scene list stays: " + err.Error()
	}
}

// removeTaken is remove's work once the slot s is out of state.json.
func (pr pruner) removeTaken(ctx context.Context, s pool.Slot) error {
	if _, err := pool.RevertNoise(ctx, pr.g, s.Path); err != nil {
		return err
	}
	editors, err := ledger.Load(pr.inv.home)
	if err != nil {
		return err
	}
	listed, err := worktreeKeys(ctx, pr.g, pr.repo())
	if err != nil {
		return err
	}
	why, err := pr.kept(ctx, editors, listed, s.Path)
	if err != nil {
		return err
	}
	if why != "" {
		return errors.New(why + " now")
	}
	return pr.g.WorktreeRemoveClean(ctx, pr.repo(), s.Path)
}
