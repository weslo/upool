package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// statusUsage is status's usage line, which its handler also names when it refuses arguments.
// --du arrives with disk reporting in M4.
const statusUsage = "status [--all] [--json]"

// statusCommand is `upool status`: the slots of the pool containing the working directory, their
// states and their leases, or with --all those of every registered pool (docs/design.md, "One
// binary, a dozen commands, one JSON file per pool", the `status` row). It reports what state.json
// records, and announces the pool's refresh job while it runs (#79). It changes only what
// docs/contracts.md (f) has every read of the pool settle: an unconfirmed lease past
// confirm_timeout ends (#75), and a warming slot whose owner died is checked again (#97). Agent
// worktrees join it in M2, Editors in M3 and disk in M4.
var statusCommand = command{
	usage: statusUsage,
	setup: func(fs *flag.FlagSet) handler {
		all := fs.Bool("all", false, "show every registered pool")
		return func(inv *invocation) (any, []string, error) { return status(inv, *all) }
	},
}

// statusReport is the data of status's --json envelope: one entry per pool, so that one pool and
// --all have the same shape.
type statusReport struct {
	Pools []poolStatus `json:"pools"`
	// now is when status ran, from which Human reckons each lease's age.
	now time.Time
}

// poolStatus is one pool: its main checkout, its base and its slots, in state.json's order.
type poolStatus struct {
	Repo  string       `json:"repo"`
	Base  string       `json:"base"`
	Slots []slotStatus `json:"slots"`
	// RefreshJob is the pool's detached `upool refresh` while its process is alive, and nil
	// otherwise (docs/contracts.md (e) `refresh_job`).
	RefreshJob *pool.RefreshJob `json:"refresh_job"`
}

// slotStatus is one slot, with its state as docs/contracts.md (f) names it and its lease as which
// reports it.
type slotStatus struct {
	Name  string     `json:"name"`
	Path  string     `json:"path"`
	State pool.State `json:"state"`
	// Lease is the slot's current lease, or nil when it has none.
	Lease *leaseReport `json:"lease"`
}

// Human returns each pool as a line naming its main checkout and base, then a table of its slots:
// name, state, lease lane, lease holder, lease age and whether the lease is confirmed, with "-"
// for a slot that has no lease, after a line announcing the refresh job while it runs. Pools are
// separated by a blank line.
func (r statusReport) Human() string {
	if len(r.Pools) == 0 {
		return "no pools are registered"
	}
	var b strings.Builder
	for i, p := range r.Pools {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s (base %s)\n", p.Repo, p.Base)
		if j := p.RefreshJob; j != nil {
			fmt.Fprintf(&b, "refreshing: upool refresh is running as process %d, since %s\n",
				j.PID, j.PIDStart.Local().Format(time.DateTime))
		}
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		// tw writes to b, a strings.Builder, which never fails, so neither can tw.
		_, _ = fmt.Fprintln(tw, "SLOT\tSTATE\tLANE\tHOLDER\tAGE\tCONFIRMED")
		for _, s := range p.Slots {
			lane, holder, age, confirmed := "-", "-", "-", "-"
			if l := s.Lease; l != nil {
				if l.Lane != "" {
					lane = l.Lane
				}
				if l.Holder != "" {
					holder = l.Holder
				}
				age = leaseAge(r.now.Sub(l.AcquiredAt))
				confirmed = "no"
				if l.Confirmed {
					confirmed = "yes"
				}
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.State, lane, holder,
				age, confirmed)
		}
		_ = tw.Flush()
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// leaseAge is d in its two largest units, such as 45s, 1m30s, 1h or 2d3h: fine enough to tell an
// unconfirmed lease inside confirm_timeout (60 s) from one past it, and short enough for a table.
// A negative d, a lease stamped by a clock ahead of this one, is 0s.
func leaseAge(d time.Duration) string {
	units := []struct {
		size   time.Duration
		suffix string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}}
	i := 0
	for i < len(units)-1 && d < units[i].size {
		i++
	}
	u := units[i]
	s := fmt.Sprintf("%d%s", max(d, 0)/u.size, u.suffix)
	if i+1 < len(units) {
		next := units[i+1]
		if rest := d % u.size / next.size; rest > 0 {
			s += fmt.Sprintf("%d%s", rest, next.suffix)
		}
	}
	return s
}

// status is status's handler. Without --all it reports the registered pool whose main checkout
// contains the working directory, which a slot's does too, since slots live under
// <repo>/.claude/worktrees; anywhere else it fails. Paths are compared as canonical keys, as which
// compares them (docs/design.md, "Repository, CI and Homebrew distribution", the first of the five
// rules after the layout).
func status(inv *invocation, all bool) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			statusUsage)
	}
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
	notes, err := expireLeases(context.Background(), inv, keep)
	if err != nil {
		return nil, nil, err
	}
	recovered, err := recoverWarming(context.Background(), inv, keep)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, recovered...)
	pools, err := readPools(context.Background(), inv.home, keep)
	if err != nil {
		return nil, nil, err
	}
	if !all {
		if len(pools) == 0 {
			return nil, nil, output.Fail(output.CodeCommandFailed,
				"%s is not inside a registered pool; run upool status --all to see every pool", wd)
		}
		pools = []registeredPool{innermost(pools)}
	}
	r := statusReport{Pools: make([]poolStatus, 0, len(pools)), now: inv.now()}
	for _, rp := range pools {
		ps := newPoolStatus(rp)
		if ps.RefreshJob, err = refreshJobLive(rp.pool); err != nil {
			return nil, nil, err
		}
		r.Pools = append(r.Pools, ps)
	}
	return r, notes, nil
}

// innermost is the pool among pools, which all contain one directory, whose main checkout lies
// deepest, so that a pool registered inside another's checkout answers for its own directory.
func innermost(pools []registeredPool) registeredPool {
	in := pools[0]
	for _, rp := range pools[1:] {
		if paths.Within(rp.repo.Path, in.repo.Path) {
			in = rp
		}
	}
	return in
}

// newPoolStatus is what status reports for rp. The main checkout is state.json's spelling of it,
// as which reports it, or the registry's canonical one if state.json has none.
func newPoolStatus(rp registeredPool) poolStatus {
	p := poolStatus{Repo: rp.pool.Repo, Base: rp.pool.Base,
		Slots: make([]slotStatus, 0, len(rp.pool.Slots))}
	if p.Repo == "" {
		p.Repo = rp.repo.Path
	}
	for _, s := range rp.pool.Slots {
		p.Slots = append(p.Slots, slotStatus{Name: s.Name, Path: s.Path, State: s.State,
			Lease: newLeaseReport(s.Lease)})
	}
	return p
}
