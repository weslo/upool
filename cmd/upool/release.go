package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/unity"
)

// releaseUsage is release's usage line, which its handler also names when it refuses its
// arguments.
const releaseUsage = "release [path|name] [--if-lease-id ID] [--force] [--json]"

// releaseCommand is `upool release`: it returns a leased slot to the pool, reset and ready when it
// is clean and parked when it is dirty (docs/design.md, "One binary, a dozen commands, one JSON
// file per pool", the `release` row; docs/contracts.md (f) transitions 11 and 12), after saving
// and closing any Editor upool opened in it and after `unity projects verify --strict` has
// checked it (A34).
var releaseCommand = command{
	usage: releaseUsage,
	setup: func(fs *flag.FlagSet) handler {
		ifLeaseID := fs.String("if-lease-id", "", "release only if the slot's lease has this ID")
		force := fs.Bool("force", false, "release even a slot whose holder is alive")
		return func(inv *invocation) (any, []string, error) {
			return release(inv, *ifLeaseID, *force)
		}
	},
}

// releaseReport is what release did: the data of its --json envelope.
type releaseReport struct {
	Slot string `json:"slot"`
	Path string `json:"path"`
	// State is ready for a slot release reset, or parked for one it kept as it was.
	State pool.State `json:"state"`
	Repo  string     `json:"repo"`
	// Dirty is why a parked slot is dirty: its `git status --porcelain` lines, or that its HEAD
	// is on no branch.
	Dirty []string `json:"dirty,omitempty"`

	// Editor is the Editor upool opened in the slot, which release saved and closed first, or nil.
	Editor *closeReport `json:"editor,omitempty"`

	// base is what a ready slot was reset to, for the human form.
	base string
}

// Human returns one line saying what became of the slot, after one saying which Editor release
// closed, if it closed one.
func (r releaseReport) Human() string {
	line := fmt.Sprintf("released %s: ready, detached at %s", r.Slot, r.base)
	if r.State == pool.StateParked {
		line = fmt.Sprintf("parked %s, which is dirty; nothing in it was changed", r.Slot)
	}
	if r.Editor != nil {
		line = r.Editor.Human() + "\n" + line
	}
	return line
}

// release is release's handler. It finds the slot, refuses a live holder's lease and a lease
// --if-lease-id does not name, saves and closes the Editor upool opened in it (releaseEditor),
// verifies the slot (verifySlot), reverts define-order noise, and then resets a clean slot to base
// or parks a dirty one. Either way it unlocks a worktree an
// earlier upool locked (pool.Unlock) and clears the lease, writing state.json last and only if the
// lease is still the one it read.
func release(inv *invocation, ifLeaseID string, force bool) (any, []string, error) {
	if len(inv.args) > 1 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args[1:],
			releaseUsage)
	}
	ctx := context.Background()
	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
	rp, s, err := releaseTarget(ctx, inv, g)
	if err != nil {
		return nil, nil, err
	}
	repo := rp.pool.Repo
	if repo == "" {
		repo = rp.repo.Path
	}
	if err := checkReleasable(s, ifLeaseID, force, proc.System{}); err != nil {
		return nil, nil, err
	}

	editor, warnings, err := releaseEditor(ctx, inv, rp, s)
	if err != nil {
		return nil, warnings, err
	}
	warnings = append(warnings, verifySlot(ctx, inv, s)...)
	if _, err := pool.RevertNoise(ctx, g, s.Path); err != nil {
		return nil, nil, err
	}
	dirty, err := pool.Dirty(ctx, g, s.Path)
	if err != nil {
		return nil, nil, err
	}
	clean := len(dirty) == 0
	reimport := false
	if clean {
		keys := clone.KeyCheck{Env: inv.environ()}
		changed, note, err := keys.Around(ctx, s.Path, func() error {
			return pool.ResetToBase(ctx, g, s.Path, rp.pool.Base)
		})
		if err != nil {
			return nil, nil, err
		}
		if note != "" {
			warnings = append(warnings, note)
		}
		if changed {
			reimport = true
			warnings = append(warnings, clone.ReimportWarning(s.Name))
		}
	}
	if err := pool.Unlock(ctx, g, repo, s.Path); err != nil {
		return nil, nil, err
	}
	state, err := clearLease(ctx, inv, rp.repo.Key, s, clean, reimport)
	if err != nil {
		return nil, nil, err
	}
	r := releaseReport{Slot: s.Name, Path: s.Path, State: state, Repo: repo, Editor: editor,
		base: rp.pool.Base}
	if !clean {
		r.Dirty = dirty
		for _, d := range dirty {
			warnings = append(warnings, fmt.Sprintf("%s is parked, not reset: %s", s.Name, d))
		}
	}
	return r, warnings, nil
}

// releaseEditor saves and closes the Editor upool opened in slot s, through close's own steps,
// before release judges the slot clean, so that the Editor's unsaved work lands in the slot and
// nothing holds it open across the reset (docs/design.md, "One binary, a dozen commands, one JSON
// file per pool", the `release` row; docs/contracts.md (e), editors[].opened_by_upool). It looks
// only at the ledger's Editors in the slot whose process is still running. One upool did not open
// fails release, which closes only Editors the tool opened; a failed close fails it too, and
// either way the slot stays leased.
func releaseEditor(ctx context.Context, inv *invocation, rp registeredPool,
	s pool.Slot) (*closeReport, []string, error) {
	l, err := ledger.Load(inv.home)
	if err != nil {
		return nil, nil, err
	}
	key, err := paths.Key(s.Path)
	if err != nil {
		return nil, nil, err
	}
	procs := inv.checker()
	ours := false
	for _, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return nil, nil, err
		}
		if ek != key {
			continue
		}
		_, err = procs.Start(e.PID)
		if errors.Is(err, proc.ErrNoProcess) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("checking the Editor of %s, process %d: %w", s.Name,
				e.PID, err)
		}
		if !e.OpenedByUpool {
			return nil, nil, output.Fail(output.CodeCommandFailed, "%s has an Editor upool did "+
				"not open, process %d, which release does not close: close it, then release "+
				"the slot", s.Name, e.PID)
		}
		ours = true
	}
	if !ours {
		return nil, nil, nil
	}
	r, warnings, err := closeSlotEditor(ctx, inv, rp, s)
	if err != nil {
		return nil, warnings, fmt.Errorf("releasing %s: %w", s.Name, err)
	}
	return &r, warnings, nil
}

// verifySlot runs `unity projects verify --strict` on the slot before release resets or parks it,
// as the GUID rule asks (docs/design.md, "Lanes: scripts in parallel, scenes one at a time,
// `.meta` never regenerated"), and returns a failure as warnings: the failure, then each finding
// with its hint. The slot is released whatever verify says (A34): a clean slot's committed work
// stays on its branch, and a dirty slot is parked anyway. Without unity on PATH it verifies nothing
// and says nothing, as the cache key comparison does; doctor reports the missing CLI.
func verifySlot(ctx context.Context, inv *invocation, s pool.Slot) []string {
	findings, err := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}.Verify(ctx, s.Path)
	if err == nil || errors.Is(err, unity.ErrNoUnity) {
		return nil
	}
	warnings := []string{fmt.Sprintf("%s failed unity projects verify --strict, and is released "+
		"anyway: %v", s.Name, err)}
	for _, f := range findings {
		warnings = append(warnings, fmt.Sprintf("%s: %s %s in %s: %s %s", s.Name, f.Severity,
			f.Code, f.Path, f.Message, f.Hint))
	}
	return warnings
}

// releaseTarget finds the slot release acts on: the slot containing the path given, the slot that
// has the branch given checked out (A18), or the slot containing the working directory. An
// argument that names an existing directory is a path; any other is a branch.
func releaseTarget(ctx context.Context, inv *invocation, g git.Client) (registeredPool,
	pool.Slot, error) {
	wd, err := inv.getwd()
	if err != nil {
		return registeredPool{}, pool.Slot{}, fmt.Errorf("reading the working directory: %w", err)
	}
	target, branch := wd, ""
	if len(inv.args) == 1 {
		arg := inv.args[0]
		if !filepath.IsAbs(arg) {
			arg = filepath.Join(wd, arg)
		}
		if info, err := os.Stat(arg); err == nil && info.IsDir() {
			target = arg
		} else {
			branch = inv.args[0]
		}
	}
	key, err := paths.Key(target)
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(key, repo.Path) })
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	if len(pools) == 0 {
		return registeredPool{}, pool.Slot{}, output.Fail(output.CodeCommandFailed,
			"%s is not inside a registered pool", target)
	}
	rp := innermost(pools)
	if branch != "" {
		repo := rp.pool.Repo
		if repo == "" {
			repo = rp.repo.Path
		}
		s, err := slotOnBranch(ctx, g, repo, rp.pool.Slots, branch)
		return rp, s, err
	}
	for _, s := range rp.pool.Slots {
		sk, err := paths.Key(s.Path)
		if err != nil {
			return registeredPool{}, pool.Slot{}, err
		}
		if paths.Within(key, sk) {
			return rp, s, nil
		}
	}
	return registeredPool{}, pool.Slot{}, output.Fail(output.CodeNotInSlot,
		"%s is not inside a slot; name the slot by its path or its branch", target)
}

// checkReleasable refuses a slot release may not act on: one that is not leased, one whose lease
// --if-lease-id does not name, and one whose holder process is alive, unless --if-lease-id names
// its lease or --force is given (docs/design.md, "Leases that survive crashes and never reclaim a
// live tree"; A6). A bare take's lease has no holder PID, so it is never refused for one.
func checkReleasable(s pool.Slot, ifLeaseID string, force bool, c proc.Checker) error {
	l := s.Lease
	if s.State != pool.StateLeased || l == nil {
		return output.Fail(output.CodeCommandFailed, "%s is %s, not leased, so there is nothing "+
			"to release", s.Name, s.State)
	}
	if ifLeaseID != "" && l.LeaseID != ifLeaseID {
		return output.Fail(output.CodeCommandFailed, "%s's lease is %s, not %s; nothing was "+
			"changed", s.Name, l.LeaseID, ifLeaseID)
	}
	if ifLeaseID != "" || force || l.HolderPID == nil {
		return nil
	}
	alive := true // a holder whose start time was not recorded cannot be shown dead
	if l.HolderPIDStart != nil {
		var err error
		if alive, err = c.Alive(*l.HolderPID, *l.HolderPIDStart); err != nil {
			return fmt.Errorf("checking %s's holder, process %d: %w", s.Name, *l.HolderPID, err)
		}
	}
	if alive {
		return output.Fail(output.CodeCommandFailed, "%s is held by process %d, which is alive; "+
			"pass --if-lease-id %s or --force to release it anyway", s.Name, *l.HolderPID,
			l.LeaseID)
	}
	return nil
}

// clearLease writes the release to state.json under the lock: the slot ready, with last_used set,
// when it was clean, and parked otherwise, its lease cleared either way, and reimport_pending set
// when the reset changed the slot's cache key (A20). The lease must still be the one release
// read; if another process changed it meanwhile, nothing is written.
func clearLease(ctx context.Context, inv *invocation, key string, s pool.Slot,
	clean, reimport bool) (pool.State, error) {
	var state pool.State
	err := pool.Update(ctx, inv.home, key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		cur := &p.Slots[i]
		if cur.Lease == nil || cur.Lease.LeaseID != s.Lease.LeaseID {
			return errors.New(s.Name + "'s lease changed in state.json meanwhile")
		}
		next, err := pool.Next(*cur, pool.Release{Clean: clean})
		if err != nil {
			return err
		}
		cur.State, cur.Lease = next, nil
		if clean {
			cur.LastUsed = inv.now().UTC()
		}
		if reimport {
			cur.ReimportPending = true
		}
		state = next
		return nil
	})
	return state, err
}
