package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// doctorUsage is doctor's usage line, which its handler also names when it refuses arguments.
// --fix arrives with #56.
const doctorUsage = "doctor [--json]"

// doctorCommand is `upool doctor`: it checks the preconditions of the pool containing the working
// directory, as status finds it, and reports each check (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the `doctor` row). It changes nothing, beyond the scratch
// files the clone-sharing check writes and removes, and exits 1 if any check fails. The version
// checks arrive with #57, and the hook and Editor checks in M2 and M3.
var doctorCommand = command{
	usage: doctorUsage,
	setup: func(*flag.FlagSet) handler { return doctor },
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
)

// cloudFolders are where macOS keeps cloud-synced files, relative to the home directory: File
// Provider storage (iCloud Drive's successors, Dropbox, OneDrive, Google Drive) and iCloud Drive.
var cloudFolders = []string{"Library/CloudStorage", "Library/Mobile Documents"}

// doctor is doctor's handler. It reads the pool as status does, under ~/.upool/lock, then runs
// every check without the lock, so that a check that fails to run is reported as a failure rather
// than ending the run.
func doctor(inv *invocation) (any, []string, error) {
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
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(cwd, repo.Path) })
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
	r.Checks = append(r.Checks, slotChecks(ctx, g, repo, rp.pool.Slots)...)
	if c, ok := cloneSharingCheck(ctx, repo); ok {
		r.Checks = append(r.Checks, c)
	}
	r.Checks = append(r.Checks, stateVolumeCheck(inv.home),
		leaseCheck(rp.pool.Slots, inv.now(), cfg.AgentTTL.Value))

	var failed []string
	for _, c := range r.Checks {
		if c.Result == checkFail {
			failed = append(failed, c.Name)
		}
	}
	if len(failed) > 0 {
		return r, nil, output.Fail(output.CodeCommandFailed, "%d of %d checks failed: %s",
			len(failed), len(r.Checks), strings.Join(failed, ", "))
	}
	return r, nil, nil
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

// slotChecks checks each slot: it is a directory on disk, `git worktree list` lists it, and its
// .git file points back into <repo>/.git/worktrees/ (docs/design.md, "Route Claude Code's
// worktrees through the pool: three branches in one hook": doctor checks that each slot is still
// on disk and in the list; docs/contracts.md (f) transition 17). A creating slot is skipped: it is
// mid-clone, or its owner died, which `doctor --fix` handles (#56).
func slotChecks(ctx context.Context, g git.Client, repo string, slots []pool.Slot) []doctorCheck {
	listed, listErr := worktreeKeys(ctx, g, repo)
	var checks []doctorCheck
	for _, s := range slots {
		if s.State == pool.StateCreating {
			continue
		}
		var problems []string
		info, err := os.Stat(s.Path)
		onDisk := err == nil && info.IsDir()
		switch {
		case errors.Is(err, os.ErrNotExist):
			problems = append(problems, s.Path+" is not on disk")
		case err != nil:
			problems = append(problems, fmt.Sprintf("checking %s: %v", s.Path, err))
		case !onDisk:
			problems = append(problems, s.Path+" is not a directory")
		}
		if listErr != nil {
			problems = append(problems, listErr.Error())
		} else if key, err := paths.Key(s.Path); err != nil {
			problems = append(problems, err.Error())
		} else if !listed[key] {
			problems = append(problems, "git worktree list does not list it")
		}
		if onDisk {
			if err := clone.CheckBacklink(repo, s.Path); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if len(problems) > 0 {
			checks = append(checks, doctorCheck{s.Name, checkFail, strings.Join(problems, "; ")})
			continue
		}
		checks = append(checks, doctorCheck{s.Name, checkPass,
			"on disk, in git worktree list, and its .git points back into the main checkout"})
	}
	return checks
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
