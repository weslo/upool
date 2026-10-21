package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// takeUsage is take's usage line, which its handler also names when it refuses its arguments.
// --lane arrives with its own issue.
const takeUsage = "take <name> [--base REF] [--holder ID] [--timeout S] [--no-create] [--json] " +
	"| take --confirm <path> [--json]"

// takeCommand is `upool take <name>`: it leases a ready slot of the pool containing the working
// directory, or with none ready clones one up to max_slots unless --no-create, checks the branch
// <name> out in it, and prints only the slot's path (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the `take` row; docs/contracts.md (f) transitions 1 and 5;
// A18; A19). `take --confirm <path>` instead confirms the create hook's lease on the slot at path
// (confirm.go).
var takeCommand = command{
	usage: takeUsage,
	setup: func(fs *flag.FlagSet) handler {
		base := fs.String("base", "", "the ref to create the branch from, if it does not exist; "+
			"default the pool's base")
		holder := fs.String("holder", "", "who holds the lease, recorded with it")
		timeout := fs.Int("timeout", 0, "seconds to wait for a ready slot, retrying once a second")
		noCreate := fs.Bool("no-create", false, "never clone a slot; only wait for a ready one")
		confirmPath := fs.String("confirm", "", "confirm the create hook's lease on the slot at "+
			"this path, run its deferred reset and refresh its agent files; with no branch name")
		return func(inv *invocation) (any, []string, error) {
			if *confirmPath != "" {
				return confirm(inv, *confirmPath, *base != "" || *holder != "" ||
					*timeout != 0 || *noCreate)
			}
			if len(inv.args) != 1 || inv.args[0] == "" {
				return nil, nil, fmt.Errorf("take needs one branch name; usage: upool %s",
					takeUsage)
			}
			r, err := takeSlot(inv, inv.args[0], takeOptions{base: *base, holder: *holder,
				timeout: *timeout, noCreate: *noCreate})
			if err != nil {
				return nil, nil, err
			}
			return r, nil, nil
		}
	},
}

// takeOptions are take's flags, and the holder process `upool run` records.
type takeOptions struct {
	base, holder string
	// timeout is how many seconds take waits for a ready slot.
	timeout int
	// noCreate keeps take from cloning a slot when none is ready.
	noCreate bool
	// holderPID and holderStart are the process whose life the lease follows, for dead-PID
	// reclaim, and its start time: `upool run`'s own (A6). Both are nil for a bare take, which
	// records none.
	holderPID   *int
	holderStart *time.Time
}

// takeReport is what take leased: the data of its --json envelope, whose human form is the path
// alone, so that `cd "$(upool take x)"` works (docs/design.md, "One binary, a dozen commands, one
// JSON file per pool": the acquire verb prints only the absolute path on stdout).
type takeReport struct {
	Path    string `json:"path"`
	Slot    string `json:"slot"`
	Branch  string `json:"branch"`
	LeaseID string `json:"lease_id"`
	Repo    string `json:"repo"`
}

// Human returns the slot's path.
func (r takeReport) Human() string { return r.Path }

// retryInterval is how often take looks again for a ready slot while it waits (docs/design.md,
// "Leases that survive crashes and never reclaim a live tree": pool-full is a wait, not a failure).
const retryInterval = time.Second

// takeSlot leases a slot of the pool containing the working directory with the branch name
// checked out, for take and `upool run`. It checks the branch before it leases anything, leases a
// ready slot under ~/.upool/lock, waiting for one up to the timeout, and then, with the lock
// dropped, locks the slot's worktree and checks the branch out. If either fails, the lease is
// undone.
func takeSlot(inv *invocation, name string, opts takeOptions) (takeReport, error) {
	if opts.timeout < 0 {
		return takeReport{}, fmt.Errorf("--timeout %d is negative; usage: upool %s",
			opts.timeout, takeUsage)
	}
	ctx := context.Background()
	wd, err := inv.getwd()
	if err != nil {
		return takeReport{}, fmt.Errorf("reading the working directory: %w", err)
	}
	cwd, err := paths.Key(wd)
	if err != nil {
		return takeReport{}, err
	}
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) })
	if err != nil {
		return takeReport{}, err
	}
	if len(pools) == 0 {
		return takeReport{}, output.Fail(output.CodeCommandFailed,
			"%s is not inside a registered pool; run upool take in a pool's checkout", wd)
	}
	rp := innermost(pools)
	repo, key := rp.pool.Repo, rp.repo.Key
	if repo == "" {
		repo = rp.repo.Path
	}
	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}

	if err := checkBranchFree(ctx, g, repo, name); err != nil {
		return takeReport{}, err
	}
	cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: repo, Home: inv.home})
	if err != nil {
		return takeReport{}, err
	}
	l := leaser{g: g, home: inv.home, key: key, repo: repo, base: rp.pool.Base,
		holder: opts.holder, holderPID: opts.holderPID, holderStart: opts.holderStart,
		confirmTimeout: cfg.ConfirmTimeout.Value, now: inv.now,
		reclaimer: pool.Reclaimer{Git: g, Proc: proc.System{}, Home: inv.home, Key: key,
			Now: inv.now}}
	if !opts.noCreate {
		l.cloner = &cloner{
			recipe: clone.Recipe{Git: g, Copier: clone.CP{}, Proc: proc.System{},
				OwnerPID: inv.pid, MaxSlots: cfg.MaxSlots.Value,
				Keys: clone.KeyCheck{Env: inv.environ()},
				Warn: func(line string) {
					_, _ = fmt.Fprintln(inv.stderr, "upool: warning: "+line) // progress only
				}},
			seeder: clone.Seeder{Git: g, Proc: proc.System{}, OwnerPID: inv.pid, Home: inv.home,
				Key: key, Keys: clone.KeyCheck{Env: inv.environ()}, Warn: func(line string) {
					_, _ = fmt.Fprintln(inv.stderr, "upool: warning: "+line) // progress only
				}},
			maxSlots: cfg.MaxSlots.Value,
		}
	}
	s, leaseID, err := l.waitForSlot(ctx, inv, time.Duration(opts.timeout)*time.Second)
	if err != nil {
		return takeReport{}, err
	}
	if err := l.prepare(ctx, s, leaseID, name, opts.base); err != nil {
		return takeReport{}, errors.Join(err, l.undo(ctx, s, leaseID))
	}
	return takeReport{Path: s.Path, Slot: s.Name, Branch: name, LeaseID: leaseID, Repo: repo},
		nil
}

// checkBranchFree refuses a branch name git would refuse, and a branch git already has checked
// out in a worktree, the main checkout included, naming the worktree (A18): a branch is checked
// out in one worktree at a time.
func checkBranchFree(ctx context.Context, g git.Client, repo, name string) error {
	ok, err := g.CheckBranchName(ctx, repo, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%q is not a valid branch name", name)
	}
	wts, err := g.WorktreeList(ctx, repo)
	if err != nil {
		return err
	}
	for _, wt := range wts {
		if wt.Branch == "refs/heads/"+name {
			return output.Fail(output.CodeCommandFailed, "branch %s is checked out in %s; a "+
				"branch can be checked out in one worktree at a time", name, wt.Path)
		}
	}
	return nil
}

// leaser leases the slots of one pool.
type leaser struct {
	g         git.Client
	home, key string
	// repo is the main checkout, and base the pool's base, which a new branch starts from unless
	// --base names another ref.
	repo, base string
	holder     string
	// holderPID and holderStart go into the lease, as takeOptions has them.
	holderPID   *int
	holderStart *time.Time
	// confirmTimeout is how long an unconfirmed lease lives: each try at leasing first ends the
	// pool's unconfirmed leases older than that (#75).
	confirmTimeout time.Duration
	now            func() time.Time
	// reclaimer reclaims the leases whose holder died before each try at choosing a slot (#61).
	reclaimer pool.Reclaimer
	// cloner clones a slot when none can be handed out; nil with --no-create.
	cloner *cloner
}

// cloner is how take clones a slot when none is ready (#62): the clone recipe, with max_slots,
// and the seed choice (docs/contracts.md (g) steps 0 to 8; A19).
type cloner struct {
	recipe   clone.Recipe
	seeder   clone.Seeder
	maxSlots int
}

// waitForSlot leases a ready slot, trying once and then once a second until timeout has passed.
// Each try first reclaims the leases whose holder died ((f) transitions 13 and 14), saying so on
// stderr. While no slot is ready it prints each slot's skip reason to stderr, again whenever they
// change.
func (l leaser) waitForSlot(ctx context.Context, inv *invocation,
	timeout time.Duration) (pool.Slot, string, error) {
	deadline := l.now().Add(timeout)
	var said string
	failed := map[string]bool{}
	told := map[string]bool{}
	for {
		done, err := l.reclaimer.Reclaim(ctx)
		if err != nil {
			return pool.Slot{}, "", err
		}
		for _, rc := range done {
			sayReclaimed(inv, rc, failed)
		}
		s, leaseID, skipped, expired, err := l.lease(ctx)
		for _, line := range pool.EndExpired(ctx, l.g, l.repo, expired, l.confirmTimeout) {
			_, _ = fmt.Fprintln(inv.stderr, "upool: "+line) // progress only
		}
		if err != nil || leaseID != "" {
			return s, leaseID, err
		}
		if reasons := strings.Join(skipped, "\n"); reasons != said {
			said = reasons
			for _, line := range skipped {
				_, _ = fmt.Fprintf(inv.stderr, "upool: skipping %s\n", line) // progress only
			}
			if len(skipped) == 0 {
				_, _ = fmt.Fprintln(inv.stderr, "upool: the pool has no slots") // progress only
			}
		}
		cloned, err := l.cloneSlot(ctx, inv, skipped, told)
		if err != nil {
			return pool.Slot{}, "", err
		}
		if cloned {
			continue // lease the new slot at once
		}
		if !l.now().Before(deadline) {
			break
		}
		time.Sleep(retryInterval)
	}
	if timeout == 0 {
		return pool.Slot{}, "", output.Fail(output.CodeCommandFailed, "no slot of %s is ready",
			l.repo)
	}
	return pool.Slot{}, "", output.Fail(output.CodeCommandFailed,
		"no slot of %s became ready within %s", l.repo, timeout)
}

// cloneSlot clones a slot when take may: no slot could be handed out, none is needs-reset (A5),
// the pool is below max_slots, and --no-create was not given. It chooses the seed (A19), clones
// with the lock released during the copy, saying so on stderr, and reports whether it cloned. With
// no seed qualifying, or the pool at max_slots, it says why on stderr once, in told, and take waits
// or fails as for a full pool.
func (l leaser) cloneSlot(ctx context.Context, inv *invocation, skipped []string,
	told map[string]bool) (bool, error) {
	say := func(line string) {
		if !told[line] {
			told[line] = true
			_, _ = fmt.Fprintln(inv.stderr, "upool: "+line) // progress only
		}
	}
	if l.cloner == nil {
		return false, nil
	}
	for _, line := range skipped {
		if strings.HasSuffix(line, ": "+string(pool.StateNeedsReset)) {
			return false, nil
		}
	}
	if len(skipped) >= l.cloner.maxSlots {
		say(fmt.Sprintf("not cloning a slot: the pool has %d, its max_slots", l.cloner.maxSlots))
		return false, nil
	}
	seed, why, err := l.cloner.seeder.Choose(ctx)
	if errors.Is(err, clone.ErrNoSeed) {
		say("not cloning a slot: no seed qualifies: " + strings.Join(why, "; "))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	say(fmt.Sprintf("no slot is ready; cloning one from %s, which takes about half a minute",
		seed.Path))
	s, err := l.cloner.recipe.Clone(ctx, l.home, l.key, seed.Path)
	err = errors.Join(err, l.cloner.seeder.Done(ctx, seed))
	if errors.Is(err, clone.ErrPoolFull) {
		say(fmt.Sprintf("not cloning a slot: the pool has %d, its max_slots", l.cloner.maxSlots))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	say("cloned " + s.Name)
	return true, nil
}

// sayReclaimed tells stderr what the reclaim did with one slot; a slot it could not reclaim is
// told once, though every try reclaims again, in failed.
func sayReclaimed(inv *invocation, rc pool.Reclaimed, failed map[string]bool) {
	gone := fmt.Sprintf("its holder, process %d, is gone", rc.HolderPID)
	var line string
	switch {
	case rc.Err != nil && failed[rc.Slot]:
		return
	case rc.Err != nil:
		failed[rc.Slot] = true
		line = fmt.Sprintf("could not reclaim %s, though %s: %v", rc.Slot, gone, rc.Err)
	case rc.State == pool.StateParked:
		line = fmt.Sprintf("parked %s, since %s and it is dirty: %s", rc.Slot, gone,
			strings.Join(rc.Dirty, "; "))
	default:
		line = fmt.Sprintf("reclaimed %s, since %s", rc.Slot, gone)
	}
	_, _ = fmt.Fprintln(inv.stderr, "upool: "+line) // progress only
}

// lease leases the first ready slot of the pool under the lock, and returns it with the new
// lease's ID; with none ready, it returns an empty ID and a "<slot>: <reason>" line per slot
// (docs/design.md, "Leases that survive crashes and never reclaim a live tree": take prints why
// each slot was skipped). A ready slot qualifies only while its directory exists, git lists it
// and its backlink checks out; otherwise it is skipped as missing-from-worktree-list
// ((f) transition 17). First it ends the unconfirmed leases older than confirm_timeout, and
// returns the slots they were on, whose git worktree locks the caller releases (#75).
func (l leaser) lease(ctx context.Context) (taken pool.Slot, leaseID string, skipped []string,
	expired []pool.Slot, err error) {
	id, err := pool.NewLeaseID()
	if err != nil {
		return pool.Slot{}, "", nil, nil, err
	}
	err = pool.Update(ctx, l.home, l.key, func(p *pool.Pool) error {
		skipped = nil
		var err error
		if expired, err = p.ExpireUnconfirmed(l.now(), l.confirmTimeout); err != nil {
			return err
		}
		listed, err := worktreeKeys(ctx, l.g, l.repo)
		if err != nil {
			return err
		}
		for i := range p.Slots {
			s := &p.Slots[i]
			reason := skipReason(*s, listed, l.repo)
			if reason != "" {
				skipped = append(skipped, s.Name+": "+reason)
				continue
			}
			state, err := pool.Next(*s, pool.Take{ReadyAvailable: true})
			if err != nil {
				return err
			}
			s.State = state
			s.Lease = &pool.Lease{LeaseID: id, Holder: l.holder, WorktreePath: s.Path,
				HolderPID: l.holderPID, HolderPIDStart: l.holderStart,
				AcquiredAt: l.now().UTC(), Confirmed: true, Lane: "script"}
			taken, leaseID = *s, id
			return nil
		}
		return nil
	})
	if err != nil {
		return pool.Slot{}, "", nil, nil, err
	}
	return taken, leaseID, skipped, expired, nil
}

// skipReason is why take does not hand s out, or "" when it may: the reasons are the design's
// (docs/design.md, "Leases that survive crashes and never reclaim a live tree"), with each state
// that is not ready named as it is.
func skipReason(s pool.Slot, listed map[string]bool, repo string) string {
	switch s.State {
	case pool.StateReady:
	case pool.StateLeasedUnconfirmed:
		return "unconfirmed"
	default:
		return string(s.State)
	}
	if s.Lease != nil {
		return "leased"
	}
	info, err := os.Stat(s.Path)
	if err != nil || !info.IsDir() {
		return "missing-from-worktree-list"
	}
	key, err := paths.Key(s.Path)
	if err != nil || !listed[key] || clone.CheckBacklink(repo, s.Path) != nil {
		return "missing-from-worktree-list"
	}
	return ""
}

// prepare finishes the lease of s with the lock dropped: it locks the worktree for the lease's
// lifetime, then checks the branch name out, creating it from base, or the pool's base, when it
// does not exist (A18).
func (l leaser) prepare(ctx context.Context, s pool.Slot, leaseID, name, base string) error {
	if err := l.g.WorktreeLock(ctx, l.repo, s.Path, pool.LeaseLockReason+leaseID); err != nil {
		return err
	}
	exists, err := l.g.BranchExists(ctx, l.repo, name)
	if err != nil {
		return err
	}
	if exists {
		return l.g.CheckoutBranch(ctx, s.Path, name)
	}
	if base == "" {
		base = l.base
	}
	return l.g.CheckoutNewBranch(ctx, s.Path, name, base)
}

// undo gives up the lease leaseID on s after prepare failed: it unlocks the worktree if prepare
// locked it, and makes the slot ready again if the lease is still the one take wrote. The slot was
// ready, so clean and detached at base, and prepare changed nothing in it unless the checkout
// succeeded.
func (l leaser) undo(ctx context.Context, s pool.Slot, leaseID string) error {
	var errs []error
	wts, err := l.g.WorktreeList(ctx, l.repo)
	if err != nil {
		errs = append(errs, err)
	}
	key, kerr := paths.Key(s.Path)
	for _, wt := range wts {
		if wk, err := paths.Key(wt.Path); kerr == nil && err == nil && wk == key && wt.Locked &&
			wt.LockReason == pool.LeaseLockReason+leaseID {
			errs = append(errs, l.g.WorktreeUnlock(ctx, l.repo, s.Path))
		}
	}
	errs = append(errs, pool.Update(ctx, l.home, l.key, func(p *pool.Pool) error {
		for i := range p.Slots {
			cur := &p.Slots[i]
			if cur.Name != s.Name || cur.Lease == nil || cur.Lease.LeaseID != leaseID {
				continue
			}
			state, err := pool.Next(*cur, pool.Release{Clean: true})
			if err != nil {
				return err
			}
			cur.State, cur.Lease = state, nil
		}
		return nil
	}))
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("undoing the lease on %s: %w", s.Name, err)
	}
	return nil
}
