package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// refreshUsage is refresh's usage line, which its handler also names when it refuses arguments.
const refreshUsage = "refresh [--fetch] [--json]"

// refreshCommand is `upool refresh [--fetch]`, run in the pool's main checkout; without --fetch it
// is also the top-up that the session-start hook spawns detached after each handout
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `refresh` row;
// "Environment, confirmation and release"; "Build in four phases, with the clone recipe first",
// the one detached job). It resets each needs-reset slot ((f) transition 20), then clones, as
// (g) step 0 seeds, until the pool has `ready` slots ready, counting creating and warming slots as
// ready (A4), or max_slots slots. Then it moves each idle ready slot that is not at base to base,
// `origin/<base>` after a `git fetch` with --fetch, and re-warms its Library with a headless
// Editor, so that the next open imports nothing ((f) transitions 3 and 18; docs/design.md, "Slot
// lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without `-x`";
// #97). While it runs it is the pool's refresh_job, which status announces; a refresh that finds
// another live one leaves the work to it and exits 0 ((e)).
var refreshCommand = command{
	usage: refreshUsage,
	setup: func(fs *flag.FlagSet) handler {
		fetch := fs.Bool("fetch", false, "fetch the base branch from origin first, and move "+
			"the ready slots to origin/<base>")
		return func(inv *invocation) (any, []string, error) { return refreshPool(inv, *fetch) }
	},
}

// refreshReport is what refresh did: the data of its --json envelope and, in human form, a line
// for each slot it reset or cloned, one for why it stopped cloning, and one for each slot it moved
// to base.
type refreshReport struct {
	Repo string `json:"repo"`
	// Running is the process ID of the refresh already running, which this one left the work to,
	// or 0 when this one did it.
	Running int `json:"running,omitempty"`
	// Reset and Cloned are the slots this refresh reset to ready and cloned, by name.
	Reset  []string `json:"reset"`
	Cloned []string `json:"cloned"`
	// Stopped is why it cloned no more.
	Stopped string `json:"stopped,omitempty"`
	// Base is where the ready slots belong, the pool's base or, with --fetch, origin/<base>, and
	// Commit the commit it named.
	Base   string `json:"base,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Moved are the slots this refresh moved to Commit and returned to ready.
	Moved []movedSlot `json:"moved"`
}

// movedSlot is a ready slot refresh moved to base.
type movedSlot struct {
	Slot string `json:"slot"`
	// Rewarmed is whether its Editor was opened headless, made ready and closed after the move. A
	// slot whose re-warm failed is ready all the same, and its next open does the import.
	Rewarmed bool `json:"rewarmed"`
	// RewarmSeconds is how long the re-warm took, from the open to the close.
	RewarmSeconds float64 `json:"rewarm_seconds,omitempty"`
}

// Human returns a line for each slot reset and each slot cloned, then why the refresh stopped
// cloning, then a line for each slot moved to base.
func (r refreshReport) Human() string {
	if r.Running != 0 {
		return fmt.Sprintf("a refresh of %s is already running as process %d", r.Repo, r.Running)
	}
	var lines []string
	for _, s := range r.Reset {
		lines = append(lines, "reset "+s)
	}
	for _, s := range r.Cloned {
		lines = append(lines, "cloned "+s)
	}
	lines = append(lines, r.Stopped)
	for _, m := range r.Moved {
		line := fmt.Sprintf("moved %s to %s (%s)", m.Slot, r.Base, shortCommit(r.Commit))
		if m.Rewarmed {
			line += fmt.Sprintf(" and re-warmed it in %.1f s", m.RewarmSeconds)
		} else {
			line += " but did not re-warm it"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// shortCommit is commit's first seven characters, as git abbreviates it.
func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// refreshPool is refresh's handler.
func refreshPool(inv *invocation, fetch bool) (_ any, warnings []string, err error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			refreshUsage)
	}
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
			"%s is not inside a registered pool; run upool refresh in a pool's checkout", wd)
	}
	rp := innermost(pools)
	repo, key := rp.pool.Repo, rp.repo.Key
	if repo == "" {
		repo = rp.repo.Path
	}
	cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: repo, Home: inv.home})
	if err != nil {
		return nil, nil, err
	}
	r := refreshReport{Repo: repo, Reset: []string{}, Cloned: []string{}, Moved: []movedSlot{}}
	job, running, err := claimRefreshJob(ctx, inv, key)
	if err != nil || running != 0 {
		r.Running = running
		return r, nil, err
	}
	defer func() { err = errors.Join(err, releaseRefreshJob(ctx, inv, key, job)) }()

	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
	keys := clone.KeyCheck{Env: inv.environ()}
	warn := func(line string) { warnings = append(warnings, line) }
	p, err := pool.Load(inv.home, key)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range p.Slots {
		if s.State != pool.StateNeedsReset || s.Lease != nil {
			continue
		}
		reset, why, notes, err := clone.ResetNeedsReset(ctx, g, keys, inv.home, key, s.Name)
		if err != nil {
			return nil, warnings, err
		}
		warnings = append(warnings, notes...)
		if !reset {
			warn(fmt.Sprintf("did not reset %s: %s", s.Name, why))
			continue
		}
		r.Reset = append(r.Reset, s.Name)
	}

	refresher := seedRefresher(inv, cfg)
	recipe := clone.Recipe{Git: g, Copier: clone.CP{}, Proc: proc.System{}, OwnerPID: inv.pid,
		MaxSlots: cfg.MaxSlots.Value, Keys: keys, Warn: warn, Refresher: refresher, Now: inv.now}
	seeder := clone.Seeder{Git: g, Proc: proc.System{}, OwnerPID: inv.pid, Home: inv.home,
		Key: key, Keys: keys, Warn: warn, Refresher: refresher}
	// topUp clones until the pool has ready slots ready, or max_slots slots, or no seed
	// qualifies, and says in r.Stopped why it stopped.
	topUp := func() error {
		for {
			p, err := pool.Load(inv.home, key)
			if err != nil {
				return err
			}
			if n := readyCount(p); n >= cfg.Ready.Value {
				r.Stopped = fmt.Sprintf("the pool has %d ready, as ready = %d asks", n,
					cfg.Ready.Value)
				return nil
			}
			if len(p.Slots) >= cfg.MaxSlots.Value {
				r.Stopped = fmt.Sprintf("the pool has %d slots, its max_slots", len(p.Slots))
				return nil
			}
			seed, why, err := seeder.Choose(ctx)
			if errors.Is(err, clone.ErrNoSeed) {
				r.Stopped = "no seed qualifies: " + strings.Join(why, "; ")
				return nil
			}
			if err != nil {
				return err
			}
			s, err := recipe.Clone(ctx, inv.home, key, seed.Path)
			err = errors.Join(err, seeder.Done(ctx, seed))
			if errors.Is(err, clone.ErrPoolFull) {
				r.Stopped = fmt.Sprintf("the pool has %d slots, its max_slots",
					cfg.MaxSlots.Value)
				return nil
			}
			if err != nil {
				return err
			}
			r.Cloned = append(r.Cloned, s.Name)
		}
	}
	if err := topUp(); err != nil {
		return nil, warnings, err
	}

	// The moves to base come after the top-up. A move can take minutes, and a session handed a
	// slot meanwhile leaves its top-up to this refresh, the pool's refresh_job, so the top-up runs
	// again after each round of moves, and moves what it cloned, until a round tries no slot.
	mv := mover{inv: inv, rp: rp, repo: repo, g: g, keys: keys, warn: warn,
		tried: map[string]bool{}}
	if err := mv.resolve(ctx, fetch); err != nil {
		return nil, warnings, err
	}
	for {
		tried, err := mv.moveReady(ctx, &r)
		if err != nil {
			return nil, warnings, err
		}
		if tried == 0 {
			break
		}
		if err := topUp(); err != nil {
			return nil, warnings, err
		}
	}
	return r, warnings, nil
}

// readyCount is how many of p's slots the top-up counts as ready: the ready ones no lease holds,
// and the creating and warming ones, which become ready when their clone or re-warm finishes (A4).
func readyCount(p pool.Pool) int {
	n := 0
	for _, s := range p.Slots {
		switch {
		case s.State == pool.StateReady && s.Lease == nil, s.State == pool.StateCreating,
			s.State == pool.StateWarming:
			n++
		}
	}
	return n
}

// claimRefreshJob records this process as the pool's refresh_job under the lock, unless a live
// one is recorded, whose PID it returns instead ((e) `refresh_job`). Off macOS, where
// internal/proc reads no start times, no job can be recorded, and none is.
func claimRefreshJob(ctx context.Context, inv *invocation, key string) (*pool.RefreshJob, int,
	error) {
	procs := proc.System{}
	start, err := procs.Start(inv.pid)
	if errors.Is(err, errors.ErrUnsupported) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("reading the start time of process %d, upool itself: %w",
			inv.pid, err)
	}
	job := &pool.RefreshJob{PID: inv.pid, PIDStart: start}
	running := 0
	err = pool.Update(ctx, inv.home, key, func(p *pool.Pool) error {
		if j := p.RefreshJob; j != nil {
			alive, err := procs.Alive(j.PID, j.PIDStart)
			if err != nil {
				return fmt.Errorf("checking the refresh job, process %d: %w", j.PID, err)
			}
			if alive {
				running = j.PID
				return nil
			}
		}
		p.RefreshJob = job
		return nil
	})
	if err != nil || running != 0 {
		return nil, running, err
	}
	return job, 0, nil
}

// releaseRefreshJob clears the pool's refresh_job if it is still job, the one this process
// recorded; nil records nothing to clear.
func releaseRefreshJob(ctx context.Context, inv *invocation, key string,
	job *pool.RefreshJob) error {
	if job == nil {
		return nil
	}
	return pool.Update(ctx, inv.home, key, func(p *pool.Pool) error {
		if j := p.RefreshJob; j != nil && j.PID == job.PID && j.PIDStart.Equal(job.PIDStart) {
			p.RefreshJob = nil
		}
		return nil
	})
}

// refreshJobLive returns p's refresh_job if its process is alive, for status to announce, and nil
// otherwise.
func refreshJobLive(p pool.Pool) (*pool.RefreshJob, error) {
	j := p.RefreshJob
	if j == nil {
		return nil, nil
	}
	alive, err := (proc.System{}).Alive(j.PID, j.PIDStart)
	if errors.Is(err, errors.ErrUnsupported) || (err == nil && !alive) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking the refresh job, process %d: %w", j.PID, err)
	}
	return j, nil
}
