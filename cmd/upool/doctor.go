package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// doctorUsage is doctor's usage line, which its handler also names when it refuses arguments.
const doctorUsage = "doctor [--fix] [--json]"

// doctorCommand is `upool doctor`: it checks the preconditions of the pool containing the working
// directory, as status finds it, and reports each check (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the `doctor` row). Without --fix it changes nothing, beyond
// the scratch files the clone-sharing check writes and removes and, as take and status do, the
// end of unconfirmed leases that outlived confirm_timeout (#75) and the return of warming slots
// whose owner died, to ready or parked ((f) transition 22; #97); with --fix it also repairs what
// is safe to repair, interrupted clones and slots whose directory is gone (#56). It also checks
// the hook registration `upool init` makes in the project (#69; A26), checks the ledger's Editors
// against the process table, reports the Unity licence, and reports the repository's VCS set-up
// as `unity vcs doctor` sees it, which init leaves to the repository's owner (A35). With --fix it
// also takes a dead Editor out of the ledger and, when no Editor has its slot open, deletes the
// slot's Temp/ (#93). It exits 1 if any check fails.
var doctorCommand = command{
	usage: doctorUsage,
	setup: func(fs *flag.FlagSet) handler {
		fix := fs.Bool("fix", false, "remove interrupted clones, drop missing ready slots, and "+
			"clear dead Editors and their Temp/")
		return func(inv *invocation) (any, []string, error) { return doctor(inv, *fix) }
	},
}

// checkResult is how a check came out.
type checkResult string

// The results a check can have. Only a failure makes doctor exit 1.
const (
	checkPass checkResult = "pass"
	checkWarn checkResult = "warn"
	checkFail checkResult = "fail"
)

// doctorCheck is one check: its name, its result, and a one-line reason.
type doctorCheck struct {
	Name   string      `json:"name"`
	Result checkResult `json:"result"`
	Reason string      `json:"reason"`
}

// doctorReport is the data of doctor's --json envelope: the pool's main checkout and its checks,
// in the order doctor ran them.
type doctorReport struct {
	Repo   string        `json:"repo"`
	Checks []doctorCheck `json:"checks"`
}

// Human returns the main checkout on a line of its own, then a line per check: its result, its
// name and its reason.
func (r doctorReport) Human() string {
	var b strings.Builder
	b.WriteString(r.Repo + "\n")
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, c := range r.Checks {
		// tw writes to b, a strings.Builder, which never fails, so neither can tw.
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Result, c.Name, c.Reason)
	}
	_ = tw.Flush()
	return strings.TrimSuffix(b.String(), "\n")
}

// The checks' names, as the report gives them. A slot's check is named after the slot.
const (
	checkGitignore    = "gitignore"
	checkLibrary      = "library"
	checkCloneSharing = "clone-sharing"
	checkStateVolume  = "state-volume"
	checkLeases       = "leases"
	// checkDesktopRecord reports the desktop app's record of its worktrees, when upool cannot read
	// it (A50).
	checkDesktopRecord = "desktop-record"
	// checkDeadHolders reports only a failure to look for leases whose holder died; each such
	// lease is reported on its slot's line.
	checkDeadHolders = "dead-holders"
)

// cloudFolders are where macOS keeps cloud-synced files, relative to the home directory: File
// Provider storage (iCloud Drive's successors, Dropbox, OneDrive, Google Drive) and iCloud Drive.
var cloudFolders = []string{"Library/CloudStorage", "Library/Mobile Documents"}

// doctor is doctor's handler. It reads the pool as status does, under ~/.upool/lock, then runs
// every check without the lock, so that a check that fails to run is reported as a failure rather
// than ending the run. A repair takes the lock again only to write state.json.
func doctor(inv *invocation, fix bool) (any, []string, error) {
	if len(inv.args) > 0 {
		return nil, nil, fmt.Errorf("unexpected arguments %q; usage: upool %s", inv.args,
			doctorUsage)
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
	keep := func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) }
	notes, err := expireLeases(ctx, inv, keep)
	if err != nil {
		return nil, nil, err
	}
	recovered, err := recoverWarming(ctx, inv, keep)
	if err != nil {
		return nil, nil, err
	}
	notes = append(notes, recovered...)
	pools, err := readPools(ctx, inv.home, keep)
	if err != nil {
		return nil, nil, err
	}
	if len(pools) == 0 {
		return nil, nil, output.Fail(output.CodeCommandFailed,
			"%s is not inside a registered pool; run upool doctor in a pool's checkout or slot", wd)
	}
	rp := innermost(pools)
	repo := rp.pool.Repo
	if repo == "" {
		repo = rp.repo.Path
	}
	cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: repo, Home: inv.home})
	if err != nil {
		return nil, nil, err
	}

	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
	r := doctorReport{Repo: repo}
	r.Checks = append(r.Checks, gitignoreCheck(ctx, g, repo), libraryCheck(ctx, g, repo))
	rec := desktop.Load(inv.home)
	sd := slotDoctor{g: g, proc: proc.System{}, home: inv.home, key: rp.repo.Key, repo: repo,
		fix: fix, record: rec, reclaimer: pool.Reclaimer{Git: g, Proc: proc.System{},
			Home: inv.home, Key: rp.repo.Key, Now: inv.now,
			Held: func(s pool.Slot) (string, bool) {
				w, held := rec.Holds(s.Path)
				return w.String(), held
			}}}
	if c, ok := desktopRecordCheck(rec); ok {
		r.Checks = append(r.Checks, c)
	}
	r.Checks = append(r.Checks, sd.checks(ctx, rp.pool.Slots)...)
	if c, ok := cloneSharingCheck(ctx, repo); ok {
		r.Checks = append(r.Checks, c)
	}
	r.Checks = append(r.Checks, stateVolumeCheck(inv.home),
		leaseCheck(rp.pool.Slots, inv.now(), cfg.AgentTTL.Value))
	r.Checks = append(r.Checks, hookChecks(inv.home, repo)...)
	r.Checks = append(r.Checks, versionChecks(ctx, inv)...)
	r.Checks = append(r.Checks, editorChecks(ctx, inv, repo, rp.pool.Slots, fix)...)
	r.Checks = append(r.Checks, licenceCheck(ctx, inv, repo))
	r.Checks = append(r.Checks, vcsChecks(ctx, inv, repo)...)

	var failed []string
	for _, c := range r.Checks {
		if c.Result == checkFail {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) > 0 {
		return r, notes, output.Fail(output.CodeCommandFailed, "%d of %d checks failed: %s",
			len(failed), len(r.Checks), strings.Join(failed, ", "))
	}
	return r, notes, nil
}

// failedCheck is a check that failed for err.
func failedCheck(name string, err error) doctorCheck {
	return doctorCheck{Name: name, Result: checkFail, Reason: err.Error()}
}

// gitignoreCheck checks that git ignores .claude/worktrees, where the slots are, as init makes it
// (docs/contracts.md (g) Clone recipe, the preconditions).
func gitignoreCheck(ctx context.Context, g git.Client, repo string) doctorCheck {
	ignored, err := g.CheckIgnore(ctx, repo, worktreesProbe)
	switch {
	case err != nil:
		return failedCheck(checkGitignore, err)
	case !ignored:
		return doctorCheck{checkGitignore, checkFail, "git does not ignore .claude/worktrees: " +
			"add " + worktreesLine + " to .gitignore"}
	}
	return doctorCheck{checkGitignore, checkPass, "git ignores .claude/worktrees"}
}

// libraryCheck checks that git tracks nothing under Library/, which `git clean -fd` keeps only
// because it is ignored (docs/contracts.md (g), the preconditions), with init's own check.
func libraryCheck(ctx context.Context, g git.Client, repo string) doctorCheck {
	if err := checkLibraryUntracked(ctx, g, repo); err != nil {
		return failedCheck(checkLibrary, err)
	}
	return doctorCheck{checkLibrary, checkPass, "git tracks nothing under Library/"}
}

// slotDoctor checks a pool's slots and, with fix, repairs those that are safe to repair.
type slotDoctor struct {
	g    git.Client
	proc proc.Checker
	// home and key name the pool's state.json, and repo is its main checkout.
	home, key, repo string
	fix             bool
	// listed are the keys of the worktrees `git worktree list` lists, or listErr why it could not
	// be read.
	listed  map[string]bool
	listErr error
	// reclaimer finds and, with fix, reclaims the leases whose holder died (#61). dead names the
	// slots it found without fix, reclaimed what it did with fix, and kept the leases it leaves
	// because the desktop app still records their slots (A50), by slot name.
	reclaimer pool.Reclaimer
	dead      map[string]bool
	reclaimed map[string]pool.Reclaimed
	kept      map[string]pool.Kept
	// record is the desktop app's record of its sessions' worktrees (A50).
	record desktop.Record
}

// checks checks each slot in slots, the pool as doctor read it: a creating slot by its owner, and
// every other slot by its presence.
func (d *slotDoctor) checks(ctx context.Context, slots []pool.Slot) []doctorCheck {
	d.listed, d.listErr = worktreeKeys(ctx, d.g, d.repo)
	checks := make([]doctorCheck, 0, len(slots)+1)
	if err := d.findDeadHolders(ctx, slots); err != nil {
		checks = append(checks, failedCheck(checkDeadHolders, err))
	}
	for _, s := range slots {
		if s.State == pool.StateCreating {
			checks = append(checks, d.creating(ctx, s))
			continue
		}
		c := d.present(ctx, s)
		if c.Result == checkPass {
			c = d.holder(s, c)
		}
		if c.Result == checkPass {
			c = d.free(ctx, s, c)
		}
		checks = append(checks, c)
	}
	return checks
}

// findDeadHolders finds the leases whose holder died, and with fix reclaims them before any slot
// is checked: the release reset for a clean slot, parked for a dirty one ((f) transitions 13 and
// 14). Without fix it only finds them.
func (d *slotDoctor) findDeadHolders(ctx context.Context, slots []pool.Slot) error {
	kept, err := d.reclaimer.Kept(pool.Pool{Slots: slots})
	if err != nil {
		return err
	}
	d.kept = make(map[string]pool.Kept, len(kept))
	for _, k := range kept {
		d.kept[k.Slot] = k
	}
	if d.fix {
		done, err := d.reclaimer.Reclaim(ctx)
		d.reclaimed = make(map[string]pool.Reclaimed, len(done))
		for _, rc := range done {
			d.reclaimed[rc.Slot] = rc
		}
		return err
	}
	dead, err := d.reclaimer.Candidates(pool.Pool{Slots: slots})
	d.dead = make(map[string]bool, len(dead))
	for _, s := range dead {
		d.dead[s.Name] = true
	}
	return err
}

// holder turns the passing presence check c of the leased slot s into what became of a lease
// whose holder died: a warning without fix, since the next take reclaims it anyway, and with fix
// what the reclaim did. A lease the reclaim keeps, since the desktop app still records the slot,
// is a warning either way, saying how to free the slot (A50).
func (d *slotDoctor) holder(s pool.Slot, c doctorCheck) doctorCheck {
	if k, ok := d.kept[s.Name]; ok {
		return doctorCheck{s.Name, checkWarn, keptLine(k)}
	}
	if rc, ok := d.reclaimed[s.Name]; ok {
		gone := fmt.Sprintf("its holder, process %d, was gone", rc.HolderPID)
		switch {
		case rc.Err != nil:
			return doctorCheck{s.Name, checkFail, fmt.Sprintf("%s; reclaiming it: %v", gone,
				rc.Err)}
		case rc.State == pool.StateParked:
			return doctorCheck{s.Name, checkPass, fmt.Sprintf("fixed: parked it, since %s and "+
				"it is dirty: %s", gone, strings.Join(rc.Dirty, "; "))}
		}
		return doctorCheck{s.Name, checkPass, "fixed: reset it to ready, since " + gone}
	}
	if d.dead[s.Name] {
		return doctorCheck{s.Name, checkWarn, fmt.Sprintf("leased by process %d, which is gone; "+
			"upool doctor --fix or the next upool take reclaims it", *s.Lease.HolderPID)}
	}
	return c
}

// free turns the passing check c of a slot with no lease into a warning when no session can be
// handed it: the desktop app still records it for a session, whose path the app would refuse to
// any other, or it is quarantined. With fix, a quarantined slot's count is cleared, so that it is
// handed out again (A50).
func (d *slotDoctor) free(ctx context.Context, s pool.Slot, c doctorCheck) doctorCheck {
	if s.Lease != nil || (s.State != pool.StateReady && s.State != pool.StateNeedsReset) {
		return c
	}
	if w, held := d.record.Holds(s.Path); held {
		return doctorCheck{s.Name, checkWarn, fmt.Sprintf("%s, but the desktop app still records "+
			"it for %s, and can resume that session into it, so no other session is handed it. "+
			"Open that session, unarchiving it if it is archived, and archive it with the app's "+
			"Archive button", s.State, w)}
	}
	if !s.Quarantined() {
		return c
	}
	if !d.fix {
		return doctorCheck{s.Name, checkWarn, "quarantined: " +
			pool.QuarantineReason(s.Name, s.UnconfirmedExpiries)}
	}
	err := pool.Update(ctx, d.home, d.key, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == s.Name {
				p.Slots[i].UnconfirmedExpiries = 0
			}
		}
		return nil
	})
	if err != nil {
		return failedCheck(s.Name, err)
	}
	return doctorCheck{s.Name, checkPass, fmt.Sprintf("fixed: cleared its quarantine, after %d "+
		"unconfirmed leases in a row, so it is handed out again", s.UnconfirmedExpiries)}
}

// desktopRecordCheck reports a desktop app record that exists but that upool cannot read: a slot
// the app still records may then be handed out, with quarantine the only fallback (A50). With no
// record, as without the app, there is nothing to report.
func desktopRecordCheck(rec desktop.Record) (doctorCheck, bool) {
	if rec.Known || strings.HasSuffix(rec.Why, " does not exist") {
		return doctorCheck{}, false
	}
	return doctorCheck{checkDesktopRecord, checkWarn, rec.Why + "; a slot the app still records " +
		"for a session may be handed out regardless, until it is quarantined"}, true
}

// creating checks a creating slot: it passes while its owner is alive, and fails once the owner is
// gone, the clone interrupted. With fix, an interrupted clone is removed: its worktree
// registration if `git worktree add` got that far, then its directory and its entry
// (docs/contracts.md (f) transition 21; A4). A slot with no owner recorded counts as one whose
// owner is gone.
func (d *slotDoctor) creating(ctx context.Context, s pool.Slot) doctorCheck {
	alive, err := ownerAlive(d.proc, s)
	if err != nil {
		return failedCheck(s.Name, err)
	}
	if alive {
		return doctorCheck{s.Name, checkPass, fmt.Sprintf("being cloned by process %d",
			*s.OwnerPID)}
	}
	reason := "an interrupted clone: its owner is gone"
	if s.OwnerPID != nil {
		reason = fmt.Sprintf("an interrupted clone: its owner, process %d, is gone", *s.OwnerPID)
	}
	if !d.fix {
		return doctorCheck{s.Name, checkFail, reason + "; upool doctor --fix removes it"}
	}
	if err := d.removeClone(ctx, s); err != nil {
		return doctorCheck{s.Name, checkFail, fmt.Sprintf("%s; removing it: %v", reason, err)}
	}
	return doctorCheck{s.Name, checkPass, "fixed: removed " + reason}
}

// ownerAlive reports whether a slot's owner, the process cloning it, is alive at the start time
// recorded with it, so that a reused PID reads as dead (internal/proc).
func ownerAlive(c proc.Checker, s pool.Slot) (bool, error) {
	if s.OwnerPID == nil || s.OwnerPIDStart == nil {
		return false, nil
	}
	alive, err := c.Alive(*s.OwnerPID, *s.OwnerPIDStart)
	if err != nil {
		return false, fmt.Errorf("checking %s's owner, process %d: %w", s.Name, *s.OwnerPID, err)
	}
	return alive, nil
}

// removeClone removes the interrupted clone s: `git worktree remove --force` if git lists it,
// its directory, and then its entry, if it is still the same creating slot. It refuses a path
// outside the pool's .claude/worktrees, so that a damaged state.json cannot point it elsewhere.
func (d *slotDoctor) removeClone(ctx context.Context, s pool.Slot) error {
	key, err := paths.Key(s.Path)
	if err != nil {
		return err
	}
	worktrees, err := paths.Key(filepath.Join(d.repo, filepath.FromSlash(".claude/worktrees")))
	if err != nil {
		return err
	}
	if key == worktrees || !paths.Within(key, worktrees) {
		return fmt.Errorf("%s is not inside %s/.claude/worktrees", s.Path, d.repo)
	}
	if d.listErr != nil {
		return d.listErr
	}
	if d.listed[key] {
		if err := d.g.WorktreeRemove(ctx, d.repo, s.Path); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(s.Path); err != nil {
		return fmt.Errorf("removing %s: %w", s.Path, err)
	}
	return d.drop(ctx, s, pool.DeadOwner{})
}

// present checks that a slot is a directory on disk, that `git worktree list` lists it, and that
// its .git file points back into <repo>/.git/worktrees/ (docs/design.md, "Route Claude Code's
// worktrees through the pool: three branches in one hook": doctor checks that each slot is still
// on disk and in the list). A slot that has left the disk is missing ((f) transition 17); with fix,
// a ready one with no lease is dropped from the pool, after `git worktree prune` drops its
// registration, so that a later clone replaces it. A leased, parked or needs-reset slot is never
// dropped, and nor is one whose directory is still on disk, which may hold work.
func (d *slotDoctor) present(ctx context.Context, s pool.Slot) doctorCheck {
	var problems []string
	info, err := os.Stat(s.Path)
	gone := errors.Is(err, os.ErrNotExist)
	onDisk := err == nil && info.IsDir()
	switch {
	case gone:
		problems = append(problems, s.Path+" is not on disk")
	case err != nil:
		problems = append(problems, fmt.Sprintf("checking %s: %v", s.Path, err))
	case !onDisk:
		problems = append(problems, s.Path+" is not a directory")
	}
	key, keyErr := paths.Key(s.Path)
	switch {
	case d.listErr != nil:
		problems = append(problems, d.listErr.Error())
	case keyErr != nil:
		problems = append(problems, keyErr.Error())
	case !d.listed[key]:
		problems = append(problems, "git worktree list does not list it")
	}
	if onDisk {
		if err := clone.CheckBacklink(d.repo, s.Path); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) == 0 {
		return doctorCheck{s.Name, checkPass,
			"on disk, in git worktree list, and its .git points back into the main checkout"}
	}
	reason := strings.Join(problems, "; ")
	if !gone || s.State != pool.StateReady || s.Lease != nil || d.listErr != nil ||
		keyErr != nil {
		return doctorCheck{s.Name, checkFail, reason}
	}
	if !d.fix {
		return doctorCheck{s.Name, checkFail, reason + "; upool doctor --fix drops it"}
	}
	if err := d.dropMissing(ctx, s, d.listed[key]); err != nil {
		return doctorCheck{s.Name, checkFail, fmt.Sprintf("%s; dropping it: %v", reason, err)}
	}
	return doctorCheck{s.Name, checkPass, "fixed: dropped it from the pool, since " + reason}
}

// dropMissing drops the missing ready slot s from the pool, after `git worktree prune` if git still
// lists it.
func (d *slotDoctor) dropMissing(ctx context.Context, s pool.Slot, listed bool) error {
	if listed {
		if err := d.g.WorktreePrune(ctx, d.repo); err != nil {
			return err
		}
	}
	return d.drop(ctx, s, pool.Missing{})
}

// drop removes s's entry from state.json under the lock, once the pool lifecycle agrees that e
// takes it out of the pool, and only if the entry is still as doctor read it: same name, path,
// state and lease. Anything else means another process changed it meanwhile, which is an error.
func (d *slotDoctor) drop(ctx context.Context, s pool.Slot, e pool.Event) error {
	if _, err := pool.Next(s, e); err != nil {
		return err
	}
	return pool.Update(ctx, d.home, d.key, func(p *pool.Pool) error {
		i := slices.IndexFunc(p.Slots, func(cur pool.Slot) bool { return cur.Name == s.Name })
		if i < 0 {
			return fmt.Errorf("%s left state.json meanwhile", s.Name)
		}
		cur := p.Slots[i]
		if cur.Path != s.Path || cur.State != s.State || (cur.Lease == nil) != (s.Lease == nil) ||
			!samePID(cur.OwnerPID, s.OwnerPID) {
			return fmt.Errorf("%s changed in state.json meanwhile: it is now %s", s.Name,
				cur.State)
		}
		p.Slots = slices.Delete(p.Slots, i, i+1)
		return nil
	})
}

// samePID reports whether two optional PIDs are the same.
func samePID(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// worktreeKeys returns the internal/paths keys of every worktree `git worktree list` lists in
// repo, including one whose directory is gone, which git marks prunable.
func worktreeKeys(ctx context.Context, g git.Client, repo string) (map[string]bool, error) {
	wts, err := g.WorktreeList(ctx, repo)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(wts))
	for _, wt := range wts {
		key, err := paths.Key(wt.Path)
		if err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, nil
}

// stateVolumeCheck warns when ~/.upool, which holds every state file and the one lock over them,
// is in a cloud-synced folder or, on macOS, on a network volume, where flock is advisory
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the state placement
// paragraph).
func stateVolumeCheck(home string) doctorCheck {
	dir := filepath.Join(home, ".upool")
	key, err := paths.Key(dir)
	if err != nil {
		return failedCheck(checkStateVolume, err)
	}
	for _, folder := range cloudFolders {
		cloud, err := paths.Key(filepath.Join(home, folder))
		if err != nil {
			return failedCheck(checkStateVolume, err)
		}
		if paths.Within(key, cloud) {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return failedCheck(checkStateVolume, err)
			}
			return doctorCheck{checkStateVolume, checkWarn, fmt.Sprintf("~/.upool resolves to %s, "+
				"inside ~/%s, which a cloud service syncs: flock is advisory there", resolved,
				folder)}
		}
	}
	fsType, network, err := networkVolume(dir)
	switch {
	case errors.Is(err, errors.ErrUnsupported):
		return doctorCheck{checkStateVolume, checkPass,
			"~/.upool is outside cloud-synced folders; network volumes are detected on macOS"}
	case err != nil:
		return failedCheck(checkStateVolume, err)
	case network:
		return doctorCheck{checkStateVolume, checkWarn, fmt.Sprintf("~/.upool is on a network "+
			"volume (%s): flock is advisory there", fsType)}
	}
	return doctorCheck{checkStateVolume, checkPass,
		fmt.Sprintf("~/.upool is on a local %s volume, outside cloud-synced folders", fsType)}
}

// leaseCheck warns about each lease with no holder PID that is older than agent_ttl: a bare
// `take` is never reclaimed, so it lasts until someone releases it (docs/contracts.md (e),
// `acquired_at`; (f) transition 13; A6).
func leaseCheck(slots []pool.Slot, now time.Time, ttl time.Duration) doctorCheck {
	var old []string
	for _, s := range slots {
		l := s.Lease
		if l == nil || l.HolderPID != nil || now.Sub(l.AcquiredAt) <= ttl {
			continue
		}
		holder := l.Holder
		if holder == "" {
			holder = "no holder"
		}
		old = append(old, fmt.Sprintf("%s (%s, %s)", s.Name, holder,
			leaseAge(now.Sub(l.AcquiredAt))))
	}
	if len(old) > 0 {
		return doctorCheck{checkLeases, checkWarn, fmt.Sprintf("leased with no holder PID for "+
			"longer than agent_ttl (%s): %s; release each once its work is done", leaseAge(ttl),
			strings.Join(old, ", "))}
	}
	return doctorCheck{checkLeases, checkPass, fmt.Sprintf("no lease without a holder PID is "+
		"older than agent_ttl (%s)", leaseAge(ttl))}
}
