package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// statusUsage is status's usage line, which its handler also names when it refuses arguments.
const statusUsage = "status [--all] [--du] [--json]"

// statusCommand is `upool status`: the slots of the pool containing the working directory, their
// states and their leases, or with --all those of every registered pool (docs/design.md, "One
// binary, a dozen commands, one JSON file per pool", the `status` row). It reports what state.json
// records, but for a leased slot whose holder died and whose directory is gone, which it shows
// missing until the next take or refresh, or `doctor --fix`, drops it ((f) transition 24; #277),
// and announces the pool's refresh job while it runs (#79). It changes only what
// docs/contracts.md (f) has every read of the pool settle: an unconfirmed lease past
// confirm_timeout ends (#75), and a warming slot whose owner died is checked again (#97). With
// --du it reports each slot's disk use, its private size beside its apparent size, and each
// pool's sum (docs/contracts.md A32; #99). With --all or without, it lists every slot, creating
// and warming ones included, and the refresh job, and it marks the slot the working directory is
// in, since a lease's holder, for a desktop session the app's ID, is not the session's own
// (docs/contracts.md (a); (e), the lease's `holder` and `session_id`; #283).
var statusCommand = command{
	usage: statusUsage,
	setup: func(fs *flag.FlagSet) handler {
		all := fs.Bool("all", false, "show every registered pool")
		du := fs.Bool("du", false, "add each slot's disk use: the bytes no other clone shares, "+
			"which deleting it frees, and its apparent size")
		return func(inv *invocation) (any, []string, error) { return status(inv, *all, *du) }
	},
}

// statusReport is the data of status's --json envelope: one entry per pool, so that one pool and
// --all have the same shape.
type statusReport struct {
	Pools []poolStatus `json:"pools"`
	// now is when status ran, from which Human reckons each lease's age.
	now time.Time
	// du is whether the slots' disk use was measured, which Human adds to its table.
	du bool
}

// poolStatus is one pool: its main checkout, its base and its slots, in state.json's order.
type poolStatus struct {
	Repo  string       `json:"repo"`
	Base  string       `json:"base"`
	Slots []slotStatus `json:"slots"`
	// RefreshJob is the pool's detached `upool refresh` while its process is alive, and nil
	// otherwise (docs/contracts.md (e) `refresh_job`).
	RefreshJob *pool.RefreshJob `json:"refresh_job"`
	// Disk is the sum of its slots' disk use, with --du.
	Disk *diskReport `json:"disk,omitempty"`
}

// diskReport is disk use as status --du reports it (A32): the private size, the bytes no other
// clone shares, which deleting the slot frees, and the apparent size, the sum of its files'
// sizes, which `du` and Finder show and which says nothing of what a clone costs.
type diskReport struct {
	PrivateBytes  int64 `json:"private_bytes"`
	ApparentBytes int64 `json:"apparent_bytes"`
}

// slotStatus is one slot, with its state as docs/contracts.md (f) names it and its lease as which
// reports it.
type slotStatus struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// State is the slot's state, or missing for a leased slot whose holder died and whose
	// directory is gone, which its lease no longer holds ((f) transition 24).
	State pool.State `json:"state"`
	// Lease is the slot's current lease, or nil when it has none.
	Lease *leaseReport `json:"lease"`
	// Disk is the slot's disk use, with --du; nil without it, or when its directory is gone.
	Disk *diskReport `json:"disk,omitempty"`
	// Quarantined is set on a slot whose leases were left unconfirmed too often in a row, which is
	// not handed out until `doctor --fix` clears it (A50); expiries is how often.
	Quarantined bool `json:"quarantined,omitempty"`
	expiries    int
	// Here is set on the slot the working directory is in, as which finds it, so that a session
	// can tell its own slot and lease (#283).
	Here bool `json:"here,omitempty"`
}

// Human returns each pool as a line naming its main checkout and base, then a table of its slots:
// name, state, lease lane, lease holder, lease age and whether the lease is confirmed, with "-"
// for a slot that has no lease, after a line announcing the refresh job while it runs. With --du
// the table adds each slot's private and apparent size, "-" for a slot whose directory is gone,
// and a line after it gives the pool's sums. A line after the table says why each quarantined slot
// is quarantined and how to clear it (A50). The slot the working directory is in has "(here)"
// after its name (#283). Pools are separated by a blank line.
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
		header := "SLOT\tSTATE\tLANE\tHOLDER\tAGE\tCONFIRMED"
		if r.du {
			header += "\tPRIVATE\tAPPARENT"
		}
		_, _ = fmt.Fprintln(tw, header)
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
			name := s.Name
			if s.Here {
				name += " (here)"
			}
			row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", name, s.State, lane, holder, age,
				confirmed)
			if r.du {
				private, apparent := "-", "-"
				if d := s.Disk; d != nil {
					private, apparent = bytesSize(d.PrivateBytes), bytesSize(d.ApparentBytes)
				}
				row += "\t" + private + "\t" + apparent
			}
			_, _ = fmt.Fprintln(tw, row)
		}
		_ = tw.Flush()
		for _, s := range p.Slots {
			if s.Quarantined {
				fmt.Fprintf(&b, "%s is quarantined: %s\n", s.Name,
					pool.QuarantineReason(s.Name, s.expiries))
			}
		}
		if d := p.Disk; d != nil {
			fmt.Fprintf(&b, "disk: %s private, %s apparent\n", bytesSize(d.PrivateBytes),
				bytesSize(d.ApparentBytes))
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// bytesSize is n bytes in the largest unit it fills, GB, MB or KB counted in powers of 1024 as
// the RAM settings count a gigabyte, with one decimal, or in bytes below a kilobyte.
func bytesSize(n int64) string {
	units := []struct {
		size   int64
		suffix string
	}{{1 << 30, "GB"}, {1 << 20, "MB"}, {1 << 10, "KB"}}
	for _, u := range units {
		if n >= u.size {
			return fmt.Sprintf("%.1f %s", float64(n)/float64(u.size), u.suffix)
		}
	}
	return fmt.Sprintf("%d B", n)
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
// <repo>/.claude/worktrees; anywhere else it fails. With --all or without, it marks the slot the
// working directory is in; with --all, which the working directory does not choose, one it cannot
// read marks none. Paths are compared as canonical keys, as which compares them (docs/design.md,
// "Repository, CI and Homebrew distribution", the first of the five rules after the layout).
func status(inv *invocation, all, du bool) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			statusUsage)
	}
	wd, cwd, err := workingDirectory(inv)
	if err != nil {
		if !all {
			return nil, nil, err
		}
		cwd = ""
	}
	keep := func(registry.Repo) bool { return true }
	if !all {
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
	r := statusReport{Pools: make([]poolStatus, 0, len(pools)), now: inv.now(), du: du}
	for _, rp := range pools {
		missing, err := goneSlots(inv, rp)
		if err != nil {
			return nil, notes, err
		}
		ps := newPoolStatus(rp, missing)
		if ps.RefreshJob, err = refreshJobLive(rp.pool); err != nil {
			return nil, nil, err
		}
		if err := ps.markHere(rp.repo, cwd); err != nil {
			return nil, notes, err
		}
		if du {
			if err := ps.measure(); err != nil {
				return nil, notes, err
			}
		}
		r.Pools = append(r.Pools, ps)
	}
	return r, notes, nil
}

// workingDirectory returns the working directory and its canonical key.
func workingDirectory(inv *invocation) (wd, key string, err error) {
	if wd, err = inv.getwd(); err != nil {
		return "", "", fmt.Errorf("reading the working directory: %w", err)
	}
	if key, err = paths.Key(wd); err != nil {
		return wd, "", err
	}
	return wd, key, nil
}

// markHere marks the slot of p, repo's pool, whose directory contains the key cwd, as which finds
// it, so that a session sees which slot, and which lease, is its own (#283). An empty cwd, or one
// outside repo's main checkout, under which every slot of the pool lies, marks none.
func (p *poolStatus) markHere(repo registry.Repo, cwd string) error {
	if cwd == "" || !paths.Within(cwd, repo.Path) {
		return nil
	}
	for i := range p.Slots {
		s := &p.Slots[i]
		key, err := paths.Key(s.Path)
		if err != nil {
			return err
		}
		s.Here = paths.Within(cwd, key)
	}
	return nil
}

// measure gives each slot of p whose directory exists its disk use, and p their sum (A32). A
// slot whose directory is gone has none. Off macOS, where no file system reports a private size,
// it fails.
func (p *poolStatus) measure() error {
	sum := diskReport{}
	for i := range p.Slots {
		s := &p.Slots[i]
		d, err := clone.DiskUse(s.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, errors.ErrUnsupported):
			return output.Fail(output.CodeCommandFailed, "status --du reads APFS private sizes, "+
				"which only macOS has: %v", err)
		case err != nil:
			return fmt.Errorf("measuring %s: %w", s.Name, err)
		}
		s.Disk = &diskReport{PrivateBytes: d.Private, ApparentBytes: d.Apparent}
		sum.PrivateBytes += d.Private
		sum.ApparentBytes += d.Apparent
	}
	p.Disk = &sum
	return nil
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

// newPoolStatus is what status reports for rp, with the slots missing names shown as missing. The
// main checkout is state.json's spelling of it, as which reports it, or the registry's canonical
// one if state.json has none.
func newPoolStatus(rp registeredPool, missing map[string]bool) poolStatus {
	p := poolStatus{Repo: rp.pool.Repo, Base: rp.pool.Base,
		Slots: make([]slotStatus, 0, len(rp.pool.Slots))}
	if p.Repo == "" {
		p.Repo = rp.repo.Path
	}
	for _, s := range rp.pool.Slots {
		state := s.State
		if missing[s.Name] {
			state = pool.StateMissing
		}
		p.Slots = append(p.Slots, slotStatus{Name: s.Name, Path: s.Path, State: state,
			Lease: newLeaseReport(s.Lease), Quarantined: s.Quarantined(),
			expiries: s.UnconfirmedExpiries})
	}
	return p
}

// goneSlots names the slots of rp that status shows as missing: leased, their holder dead, no live
// Editor in them, the desktop app's record not naming them, and their directory gone, which the
// next take or refresh, or `doctor --fix`, drops from the pool ((f) transition 24; #277). Off
// macOS, where internal/proc reads no start times, no holder can be shown dead, and it names none.
func goneSlots(inv *invocation, rp registeredPool) (map[string]bool, error) {
	r := pool.Reclaimer{Proc: inv.checker(), Home: inv.home, Key: rp.repo.Key,
		Held: appHolds(inv.home)}
	gone, err := r.Gone(rp.pool)
	if errors.Is(err, errors.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(gone))
	for _, s := range gone {
		names[s.Name] = true
	}
	return names, nil
}
