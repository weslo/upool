package main

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/statefile"
)

// whichUsage is which's usage line, which its handler also names when it refuses arguments.
const whichUsage = "which [--json]"

// whichCommand is `upool which`: it names the slot containing the working directory, for agents
// whose working directory resets between tool calls, and has no side effects (docs/design.md, "One
// binary, a dozen commands, one JSON file per pool", the `which`/`path` row). Outside every slot
// it exits 1 with NOT_IN_SLOT (docs/contracts.md (d), "upool's own exit codes").
var whichCommand = command{
	usage: whichUsage,
	setup: func(*flag.FlagSet) handler { return which },
}

// whichReport is the slot which found: the data of its --json envelope, with the slot's name,
// path, state and lease as state.json records them (docs/contracts.md (e)), and its pool's repo.
type whichReport struct {
	Name  string     `json:"name"`
	Path  string     `json:"path"`
	State pool.State `json:"state"`
	// Lease is the slot's current lease, or nil when it has none.
	Lease *whichLease `json:"lease"`
	// Repo is the pool's main checkout.
	Repo string `json:"repo"`
}

// whichLease is the part of a lease which reports: who holds it, its lease_id for `release
// --if-lease-id`, whether it is confirmed, and when it was taken.
type whichLease struct {
	Holder     string    `json:"holder"`
	LeaseID    string    `json:"lease_id"`
	Confirmed  bool      `json:"confirmed"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// Human returns the slot's path alone, so that `cd "$(upool which)"` works.
func (r whichReport) Human() string { return r.Path }

// which is which's handler. It compares canonical keys, never the paths as spelled, so that a
// working directory under /var finds a slot recorded under /private/var (docs/design.md,
// "Repository, CI and Homebrew distribution", the first of the five rules after the layout).
func which(inv *invocation) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			whichUsage)
	}
	wd, err := inv.getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the working directory: %w", err)
	}
	cwd, err := paths.Key(wd)
	if err != nil {
		return nil, nil, err
	}
	r, err := findSlot(context.Background(), inv.home, cwd)
	if err != nil {
		return nil, nil, err
	}
	if r == nil {
		return nil, nil, output.Fail(output.CodeNotInSlot,
			"%s is not inside a slot of any registered pool", wd)
	}
	return *r, nil, nil
}

// findSlot returns the slot containing the key cwd as which reports it, or nil when no slot does.
// It reads registry.json and the pools' state.json files under ~/.upool/lock, so that they agree,
// and writes nothing. Only a pool whose main checkout contains cwd is read, since its slots are
// all under <repo>/.claude/worktrees (docs/design.md, "One binary, a dozen commands, one JSON file
// per pool"); the main checkout itself is in none of them.
//
// Taking the lock creates ~/.upool and its lock file, so findSlot first reads registry.json alone
// and takes the lock only when a registered main checkout contains cwd. Otherwise that one file,
// which every write replaces whole, is the answer, and which leaves a machine with no ~/.upool
// without one.
func findSlot(ctx context.Context, home, cwd string) (r *whichReport, err error) {
	repos, err := registry.List(home)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(repos, func(repo registry.Repo) bool {
		return paths.Within(cwd, repo.Path)
	}) {
		return nil, nil
	}
	l, err := statefile.Lock(ctx, home)
	if err != nil {
		return nil, err
	}
	defer func() {
		if uerr := l.Unlock(); uerr != nil && err == nil {
			r, err = nil, uerr
		}
	}()
	if repos, err = registry.List(home); err != nil {
		return nil, err
	}
	for _, repo := range repos {
		if !paths.Within(cwd, repo.Path) {
			continue
		}
		p, err := pool.Load(home, repo.Key)
		if err != nil {
			return nil, err
		}
		for _, s := range p.Slots {
			key, err := paths.Key(s.Path)
			if err != nil {
				return nil, err
			}
			if paths.Within(cwd, key) {
				return newWhichReport(s, p.Repo), nil
			}
		}
	}
	return nil, nil
}

// newWhichReport is what which reports for slot s of the pool whose main checkout is repo.
func newWhichReport(s pool.Slot, repo string) *whichReport {
	r := &whichReport{Name: s.Name, Path: s.Path, State: s.State, Repo: repo}
	if l := s.Lease; l != nil {
		r.Lease = &whichLease{Holder: l.Holder, LeaseID: l.LeaseID, Confirmed: l.Confirmed,
			AcquiredAt: l.AcquiredAt.UTC()}
	}
	return r
}
