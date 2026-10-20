package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// takeUsage is take's usage line, which its handler also names when it refuses its arguments.
// --lane, --no-create and --confirm arrive with their own issues; until a take can clone a slot
// (#62), every take behaves as with --no-create.
const takeUsage = "take <name> [--base REF] [--holder ID] [--timeout S] [--json]"

// takeCommand is `upool take <name>`: it leases a ready slot of the pool containing the working
// directory, checks the branch <name> out in it, and prints only the slot's path (docs/design.md,
// "One binary, a dozen commands, one JSON file per pool", the `take` row; docs/contracts.md (f)
// transition 5; A18).
var takeCommand = command{
	usage: takeUsage,
	setup: func(fs *flag.FlagSet) handler {
		base := fs.String("base", "", "the ref to create the branch from, if it does not exist; "+
			"default the pool's base")
		holder := fs.String("holder", "", "who holds the lease, recorded with it")
		timeout := fs.Int("timeout", 0, "seconds to wait for a ready slot, retrying once a second")
		return func(inv *invocation) (any, []string, error) {
			return take(inv, takeOptions{base: *base, holder: *holder, timeout: *timeout})
		}
	},
}

// takeOptions are take's flags.
type takeOptions struct {
	base, holder string
	// timeout is how many seconds take waits for a ready slot.
	timeout int
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

// leaseLockReason is the reason a leased slot's `git worktree lock` carries.
const leaseLockReason = "upool lease "

// retryInterval is how often take looks again for a ready slot while it waits (docs/design.md,
// "Leases that survive crashes and never reclaim a live tree": pool-full is a wait, not a failure).
const retryInterval = time.Second

// take is take's handler. It checks the branch before it leases anything, leases a ready slot
// under ~/.upool/lock, waiting for one up to the timeout, and then, with the lock dropped, locks
// the slot's worktree and checks the branch out. If either fails, the lease is undone.
func take(inv *invocation, opts takeOptions) (any, []string, error) {
	if len(inv.args) != 1 || inv.args[0] == "" {
		return nil, nil, fmt.Errorf("take needs one branch name; usage: upool %s", takeUsage)
	}
	if opts.timeout < 0 {
		return nil, nil, fmt.Errorf("--timeout %d is negative; usage: upool %s", opts.timeout,
			takeUsage)
	}
	name := inv.args[0]
	ctx := context.Background()
	wd, err := inv.getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the working directory: %w", err)
	}
	cwd, err := paths.Key(wd)
	if err != nil {
		return nil, nil, err
	}
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) })
	if err != nil {
		return nil, nil, err
	}
	if len(pools) == 0 {
		return nil, nil, output.Fail(output.CodeCommandFailed,
			"%s is not inside a registered pool; run upool take in a pool's checkout", wd)
	}
	rp := innermost(pools)
	repo, key := rp.pool.Repo, rp.repo.Key
	if repo == "" {
		repo = rp.repo.Path
	}
	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}

	if err := checkBranchFree(ctx, g, repo, name); err != nil {
		return nil, nil, err
	}
	l := leaser{g: g, home: inv.home, key: key, repo: repo, base: rp.pool.Base,
		holder: opts.holder, now: inv.now, reclaimer: pool.Reclaimer{Git: g, Proc: proc.System{},
			Home: inv.home, Key: key, Now: inv.now}}
	s, leaseID, err := l.waitForSlot(ctx, inv, time.Duration(opts.timeout)*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if err := l.prepare(ctx, s, leaseID, name, opts.base); err != nil {
		return nil, nil, errors.Join(err, l.undo(ctx, s, leaseID))
	}
	return takeReport{Path: s.Path, Slot: s.Name, Branch: name, LeaseID: leaseID, Repo: repo},
		nil, nil
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
	now        func() time.Time
	// reclaimer reclaims the leases whose holder died before each try at choosing a slot (#61).
	reclaimer pool.Reclaimer
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
	for {
		done, err := l.reclaimer.Reclaim(ctx)
		if err != nil {
			return pool.Slot{}, "", err
		}
		for _, rc := range done {
			sayReclaimed(inv, rc, failed)
		}
		s, leaseID, skipped, err := l.lease(ctx)
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
// ((f) transition 17).
func (l leaser) lease(ctx context.Context) (taken pool.Slot, leaseID string, skipped []string,
	err error) {
	id, err := newLeaseID()
	if err != nil {
		return pool.Slot{}, "", nil, err
	}
	err = pool.Update(ctx, l.home, l.key, func(p *pool.Pool) error {
		skipped = nil
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
				AcquiredAt: l.now().UTC(), Confirmed: true, Lane: "script"}
			taken, leaseID = *s, id
			return nil
		}
		return nil
	})
	if err != nil {
		return pool.Slot{}, "", nil, err
	}
	return taken, leaseID, skipped, nil
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

// newLeaseID returns a random lease_id, 16 bytes in hex (docs/contracts.md (e) Lease).
func newLeaseID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("making a lease ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// prepare finishes the lease of s with the lock dropped: it locks the worktree for the lease's
// lifetime, then checks the branch name out, creating it from base, or the pool's base, when it
// does not exist (A18).
func (l leaser) prepare(ctx context.Context, s pool.Slot, leaseID, name, base string) error {
	if err := l.g.WorktreeLock(ctx, l.repo, s.Path, leaseLockReason+leaseID); err != nil {
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
			wt.LockReason == leaseLockReason+leaseID {
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
