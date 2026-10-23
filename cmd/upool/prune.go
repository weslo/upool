package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
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
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// pruneUsage is prune's usage line, which its handler also names when it refuses arguments.
const pruneUsage = "prune [--all] [--yes] [--json]"

// pruneCommand is `upool prune`: in the pool containing the working directory, or with --all in
// every registered pool, it lists the ready slots beyond `ready` that are idle, merged and clean,
// and the subagent worktrees whose parent is gone or that agent_ttl has passed, and with --yes
// removes them (docs/design.md, "One binary, a dozen commands, one JSON file per
// pool", the `prune` row; "Disk: 0.4 GB per persistent slot; disk is not what limits the count":
// dry run unless --yes, with no override flags; docs/contracts.md (f) transition 15; A31; #100).
// With max_disk set it also lists, least recently used first, idle ready slots past those
// `ready` keeps while the pool's disk use is over max_disk (#101), and collects agent worktrees
// (#102).
var pruneCommand = command{
	usage: pruneUsage,
	setup: func(fs *flag.FlagSet) handler {
		all := fs.Bool("all", false, "prune every registered pool")
		yes := fs.Bool("yes", false, "remove the slots and agent worktrees listed, rather than "+
			"only listing them")
		return func(inv *invocation) (any, []string, error) { return prune(inv, *all, *yes) }
	},
}

// pruneReport is what prune found and did: the data of its --json envelope.
type pruneReport struct {
	Pools []poolPrune `json:"pools"`
	// DryRun is whether prune only listed, without --yes.
	DryRun bool `json:"dry_run"`
}

// poolPrune is one pool's candidates, least recently used first, then its agent worktrees.
type poolPrune struct {
	Repo       string           `json:"repo"`
	Candidates []pruneCandidate `json:"candidates"`
	// Agents are the subagent worktrees prune may collect, and KeptAgents those it keeps though
	// their parent is gone or agent_ttl has passed, with why (#102).
	Agents     []agentCandidate `json:"agents"`
	KeptAgents []keptAgent      `json:"kept_agents"`
}

// agentCandidate is a subagent worktree prune may collect, and why.
type agentCandidate struct {
	Path      string    `json:"path"`
	ParentPID int       `json:"parent_pid"`
	CreatedAt time.Time `json:"created_at"`
	Why       string    `json:"why"`
	// Removed is whether --yes removed it, and Error why it could not.
	Removed bool   `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// keptAgent is a subagent worktree prune would collect but keeps, and why.
type keptAgent struct {
	Path string `json:"path"`
	Why  string `json:"why"`
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
	// say adds the line for something prune would remove, removed or could not remove.
	say := func(what, why, failed string, removed bool) {
		found = true
		switch {
		case r.DryRun:
			lines = append(lines, fmt.Sprintf("  would remove %s: %s", what, why))
		case removed:
			lines = append(lines, fmt.Sprintf("  removed %s: %s", what, why))
		default:
			lines = append(lines, fmt.Sprintf("  did not remove %s: %s", what, failed))
		}
	}
	for _, p := range r.Pools {
		if len(p.Candidates)+len(p.Agents)+len(p.KeptAgents) == 0 {
			lines = append(lines, p.Repo+": nothing to prune")
			continue
		}
		lines = append(lines, p.Repo+":")
		for _, c := range p.Candidates {
			say(c.Slot, c.Why, c.Error, c.Removed)
		}
		for _, a := range p.Agents {
			say("agent worktree "+a.Path, a.Why, a.Error, a.Removed)
		}
		for _, k := range p.KeptAgents {
			lines = append(lines, fmt.Sprintf("  keeping agent worktree %s: %s", k.Path, k.Why))
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
		if err := pr.agents(ctx, &p); err != nil {
			return nil, nil, err
		}
		if yes {
			for i := range p.Candidates {
				pr.remove(ctx, &p.Candidates[i])
			}
			for i := range p.Agents {
				pr.removeAgent(ctx, &p.Agents[i])
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
	// measure measures a slot's disk use for max_disk; nil is clone.DiskUse, status --du's.
	measure func(dir string) (clone.Disk, error)
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

// candidates returns the pool's slots that prune may remove, least recently used first. The idle
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
// With max_disk set, overDisk then adds the least recently used of the rest, whatever their age,
// while the pool's disk use is over it. Leased, parked and every other state are never candidates.
func (pr pruner) candidates(ctx context.Context) (poolPrune, error) {
	repo := pr.repo()
	r := poolPrune{Repo: repo, Candidates: []pruneCandidate{}, Agents: []agentCandidate{},
		KeptAgents: []keptAgent{}}
	cfg, err := config.Load(config.Inputs{Getenv: pr.inv.getenv, Repo: repo, Home: pr.inv.home})
	if err != nil {
		return poolPrune{}, err
	}
	if cfg.Ready.Value < 0 {
		return poolPrune{}, output.Fail(output.CodeCommandFailed, "ready is %d for %s, from %s; "+
			"it must be 0 or more", cfg.Ready.Value, repo, cfg.Ready.Source)
	}
	if err := pr.checkMaxDisk(cfg); err != nil {
		return poolPrune{}, err
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
		idleFor := now.Sub(is.since)
		if idleFor < shrink {
			continue
		}
		ok, err := pr.removable(ctx, editors, listed, base, is.slot.Path)
		if err != nil {
			return poolPrune{}, err
		}
		if ok {
			r.Candidates = append(r.Candidates, pruneCandidate{Slot: is.slot.Name,
				Path: is.slot.Path, IdleSince: is.since.UTC(), Why: fmt.Sprintf("idle %s, past "+
					"shrink_after %s; beyond ready = %d; clean, and merged into %s",
					leaseAge(idleFor), leaseAge(shrink), cfg.Ready.Value, pr.rp.pool.Base)})
		}
	}
	if cfg.MaxDisk.Set() {
		if err := pr.overDisk(ctx, cfg, editors, listed, base, beyond, &r); err != nil {
			return poolPrune{}, err
		}
	}
	slices.SortStableFunc(r.Candidates, func(a, b pruneCandidate) int {
		return a.IdleSince.Compare(b.IdleSince)
	})
	return r, nil
}

// gigabyte is a gigabyte as max_disk counts one, 2^30 bytes, as the RAM settings do.
const gigabyte = 1 << 30

// overDisk adds to r's candidates while the pool's disk use is over max_disk: the slots of beyond,
// those past the `ready` kept, least recently used first, that removable allows (#101; docs/
// design.md, "Disk: 0.4 GB per persistent slot; disk is not what limits the count": an optional
// max_disk with LRU removal of idle ready slots). The pool's disk use is status --du's: the sum of
// its slots' private sizes, every slot whose directory exists counted (A32), less those r already
// lists. It never goes below `ready` ready slots, since beyond leaves them out.
func (pr pruner) overDisk(ctx context.Context, cfg config.Config, editors ledger.Ledger,
	listed map[string]bool, base string, beyond []idleSlot, r *poolPrune) error {
	measure := pr.measure
	if measure == nil {
		measure = clone.DiskUse
	}
	private := map[string]int64{}
	var total int64
	for _, s := range pr.rp.pool.Slots {
		d, err := measure(s.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("measuring %s: %w", s.Name, err)
		}
		private[s.Name] = d.Private
		total += d.Private
	}
	listedAlready := map[string]bool{}
	for _, c := range r.Candidates {
		listedAlready[c.Slot] = true
		total -= private[c.Slot]
	}
	limit := int64(math.MaxInt64) // a budget past what an int64 counts is no limit
	if b := cfg.MaxDisk.Value * gigabyte; b < math.MaxInt64 {
		limit = int64(b)
	}
	for i := len(beyond) - 1; i >= 0 && total > limit; i-- {
		is := beyond[i]
		if listedAlready[is.slot.Name] {
			continue
		}
		ok, err := pr.removable(ctx, editors, listed, base, is.slot.Path)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		r.Candidates = append(r.Candidates, pruneCandidate{Slot: is.slot.Name,
			Path: is.slot.Path, IdleSince: is.since.UTC(), Why: fmt.Sprintf("the least recently "+
				"used, idle %s, while the pool's %s private is over max_disk %s; beyond ready = "+
				"%d; clean, and merged into %s", leaseAge(pr.inv.now().Sub(is.since)),
				bytesSize(total), bytesSize(limit), cfg.Ready.Value, pr.rp.pool.Base)})
		total -= private[is.slot.Name]
	}
	return nil
}

// checkMaxDisk refuses a max_disk that is no budget, before prune looks at any slot, so that
// every pool's prune refuses it alike: one that is not a finite number of 0 or more, such as nan
// or inf, which a float parses, and, off macOS, any max_disk at all, since no private size can be
// read there to measure the pool by (A32).
func (pr pruner) checkMaxDisk(cfg config.Config) error {
	if !cfg.MaxDisk.Set() {
		return nil
	}
	if v := cfg.MaxDisk.Value; !(v >= 0) || math.IsInf(v, 0) {
		return output.Fail(output.CodeCommandFailed, "max_disk is %v GB, from %s; it must be a "+
			"number of 0 or more", v, cfg.MaxDisk.Source)
	}
	if pr.measure == nil && !clone.PrivateSizes {
		return output.Fail(output.CodeCommandFailed, "max_disk is set, from %s, but a pool's "+
			"disk use is measured in APFS private sizes, as status --du measures it, which only "+
			"macOS has", cfg.MaxDisk.Source)
	}
	return nil
}

// removable reports whether the slot at dir may go once its age or the disk calls for it: kept
// gives no reason to keep it, and its HEAD is merged into base, the commit base names.
func (pr pruner) removable(ctx context.Context, editors ledger.Ledger, listed map[string]bool,
	base, dir string) (bool, error) {
	why, err := pr.kept(ctx, editors, listed, dir)
	if err != nil || why != "" {
		return false, err
	}
	head, err := pr.g.Head(ctx, dir)
	if err != nil {
		return false, err
	}
	return pr.g.IsAncestor(ctx, pr.repo(), head, base)
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

// agents adds to r the pool's subagent worktrees that prune may collect, and those it keeps
// (docs/design.md, "Route Claude Code's worktrees through the pool: three branches in one hook":
// nothing releases a subagent's worktree, so prune collects it; docs/contracts.md (e), Agent
// worktree; (f): agent worktrees are not slots; #102). One is due once its parent, the session's
// CLI, is gone, or it is older than agent_ttl. The parent's start time is not recorded, so any
// process at its PID keeps it alive, the safe mistake, and off macOS, where that cannot be read,
// the parent counts as alive. A due worktree is a candidate when kept gives no reason to keep it:
// it is clean as (f) defines it, so no commit of the subagent's on a detached HEAD is lost, no
// Editor has it open, and it holds no other worktree. One kept is listed, with why. One whose
// directory is gone, or that git does not list, is left out of both.
func (pr pruner) agents(ctx context.Context, r *poolPrune) error {
	cfg, err := config.Load(config.Inputs{Getenv: pr.inv.getenv, Repo: pr.repo(),
		Home: pr.inv.home})
	if err != nil {
		return err
	}
	if len(pr.rp.pool.AgentWorktrees) == 0 {
		return nil
	}
	listed, err := worktreeKeys(ctx, pr.g, pr.repo())
	if err != nil {
		return err
	}
	editors, err := ledger.Load(pr.inv.home)
	if err != nil {
		return err
	}
	now, ttl := pr.inv.now(), cfg.AgentTTL.Value
	for _, a := range pr.rp.pool.AgentWorktrees {
		key, err := paths.Key(a.Path)
		if err != nil {
			return err
		}
		if info, err := os.Stat(a.Path); err != nil || !info.IsDir() || !listed[key] {
			continue
		}
		var due string
		_, err = pr.inv.checker().Start(a.ParentPID)
		switch {
		case errors.Is(err, proc.ErrNoProcess):
			due = fmt.Sprintf("its parent, process %d, is gone", a.ParentPID)
		case err != nil && !errors.Is(err, errors.ErrUnsupported):
			return fmt.Errorf("checking the parent of %s, process %d: %w", a.Path, a.ParentPID,
				err)
		case now.Sub(a.CreatedAt) >= ttl:
			due = fmt.Sprintf("made %s ago, past agent_ttl %s", leaseAge(now.Sub(a.CreatedAt)),
				leaseAge(ttl))
		default:
			continue
		}
		why, err := pr.kept(ctx, editors, listed, a.Path)
		if err != nil {
			return err
		}
		if why != "" {
			r.KeptAgents = append(r.KeptAgents, keptAgent{Path: a.Path,
				Why: due + ", but " + why})
			continue
		}
		r.Agents = append(r.Agents, agentCandidate{Path: a.Path, ParentPID: a.ParentPID,
			CreatedAt: a.CreatedAt.UTC(), Why: due + "; clean"})
	}
	return nil
}

// removeAgent removes the agent worktree c as remove removes a slot: its entry out of state.json
// first, under the lock and only if it is still the one prune read, then define-order noise
// reverted, kept asked again, and `git worktree remove` without --force. If that fails while git
// still lists the worktree, the entry is put back; if git no longer lists it, the entry stays out,
// and c names what is left to delete.
func (pr pruner) removeAgent(ctx context.Context, c *agentCandidate) {
	home, key := pr.inv.home, pr.rp.repo.Key
	var taken pool.AgentWorktree
	err := pool.Update(ctx, home, key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.AgentWorktrees, func(a pool.AgentWorktree) bool {
			return a.Path == c.Path
		})
		if i < 0 {
			return fmt.Errorf("it left state.json meanwhile")
		}
		if a := p.AgentWorktrees[i]; a.ParentPID != c.ParentPID || !a.CreatedAt.Equal(c.CreatedAt) {
			return fmt.Errorf("it changed in state.json meanwhile")
		}
		taken = p.AgentWorktrees[i]
		p.AgentWorktrees = slices.Delete(p.AgentWorktrees, i, i+1)
		return nil
	})
	if err != nil {
		c.Error = err.Error()
		return
	}
	if err := pr.removeTaken(ctx, pool.Slot{Path: taken.Path}); err != nil {
		c.Error = err.Error()
		listed, lerr := worktreeKeys(ctx, pr.g, pr.repo())
		k, kerr := paths.Key(taken.Path)
		if lerr == nil && kerr == nil && !listed[k] {
			c.Error += "; git no longer lists it: delete what is left of " + taken.Path +
				" by hand"
			return
		}
		if perr := pool.Update(ctx, home, key, func(p *pool.Pool) error {
			p.AgentWorktrees = append(p.AgentWorktrees, taken)
			return nil
		}); perr != nil {
			c.Error += "; putting its entry back: " + perr.Error()
		} else {
			c.Error += "; it stays"
		}
		return
	}
	c.Removed = true
}
