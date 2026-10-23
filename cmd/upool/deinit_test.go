package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// repoFiles returns every file under root but those under .git, by its path relative to root: a
// regular file's mode and contents, or a link's target. Directories are not in it, since git does
// not track them.
func repoFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && d.Name() == ".git":
			return filepath.SkipDir
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			files[rel] = "-> " + target
			return err
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			b, err := os.ReadFile(path)
			files[rel] = info.Mode().String() + " " + string(b)
			return err
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return files
	}
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// untouched is what deinit must leave as it was when it refuses or only lists: the project's files,
// everything under ~/.upool but the lock, and the worktrees git lists.
func untouched(t *testing.T, p harnessPool) map[string]string {
	t.Helper()
	files := repoFiles(t, p.Repo)
	for rel, v := range repoFiles(t, filepath.Join(p.Home, ".upool")) {
		if rel != "lock" {
			files["~/.upool/"+rel] = v
		}
	}
	files["git worktree list"] = p.Git("worktree", "list", "--porcelain")
	return files
}

// checkUnchanged fails the test unless what untouched reads for p is still before.
func checkUnchanged(t *testing.T, p harnessPool, before map[string]string) {
	t.Helper()
	after := untouched(t, p)
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if after[k] != before[k] {
			t.Errorf("%s changed:\n%s\nwant\n%s", k, after[k], before[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(after)) {
		if _, ok := before[k]; !ok {
			t.Errorf("%s appeared", k)
		}
	}
}

// runDeinit runs `upool deinit args...` in p's main checkout with procs as the process table, so
// that no lease or owner reads as alive unless the test says so.
func runDeinit(t *testing.T, p harnessPool, procs proc.Checker, args ...string) result {
	t.Helper()
	return runProcs(t, p.Env, p.Repo, procs, func() time.Time { return clock },
		append([]string{"deinit"}, args...)...)
}

// checkGone fails the test unless upool is out of p: no slot on disk or in git, no pool directory,
// no registry entry, no upool hook in the project's settings, and doctor reports the repository
// as not registered.
func checkGone(t *testing.T, p harnessPool) {
	t.Helper()
	for _, name := range []string{"slot-1", "slot-2"} {
		if _, err := os.Lstat(p.slot(name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still on disk: %v", name, err)
		}
		if p.listed(p.slot(name)) {
			t.Errorf("git still lists %s", name)
		}
	}
	if _, err := os.Lstat(filepath.Join(p.Home, ".upool", "pools", p.key)); !errors.Is(err,
		fs.ErrNotExist) {
		t.Errorf("~/.upool/pools/%s is still there: %v", p.key, err)
	}
	if _, ok, err := registry.Lookup(p.Home, p.Repo); err != nil || ok {
		t.Errorf("registry lookup = %v, %v; want no entry", ok, err)
	}
	if got := readFile(t, p.Env, projectSettings); strings.Contains(got, `"hook"`) {
		t.Errorf("%s = %s, want no upool hook", projectSettings, got)
	}
	r := runIn(t, p.Env, p.Repo, "doctor")
	if r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, "is not inside a registered pool") {
		t.Errorf("doctor: exit %d, stderr %q; want %d and the repository not registered", r.code,
			r.stderr, output.ExitFailure)
	}
}

// Without --yes, deinit lists what it would remove and changes nothing (#191).
func TestDeinitWithoutYesChangesNothing(t *testing.T) {
	p := newHarnessPool(t)
	before := untouched(t, p)
	r := runDeinit(t, p, procsAlive())
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	for _, want := range []string{
		"would remove slot-1, " + p.slot("slot-1"),
		"would remove slot-2, " + p.slot("slot-2"),
		"would remove upool's state, " + filepath.Join(p.Home, ".upool", "pools", p.key),
		"would remove upool's hooks from " + p.Path(projectSettings),
		"this was a dry run: upool deinit --yes removes them",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout = %q, want a line with %q", r.stdout, want)
		}
	}
	checkUnchanged(t, p, before)
}

// deinit --yes on the harness pool removes both slots, the pool's state and registry entry, and
// upool's hooks, keeping the settings' own keys, and leaves the repository unregistered (#191).
func TestDeinitTakesUpoolOutOfThePool(t *testing.T) {
	p := newHarnessPool(t)
	r := runDeinit(t, p, procsAlive(), "--yes")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if !strings.Contains(r.stdout, "removed slot-1, "+p.slot("slot-1")) {
		t.Errorf("stdout = %q, want slot-1 removed", r.stdout)
	}
	checkGone(t, p)
	if got := readFile(t, p.Env, projectSettings); got != "{}\n" {
		t.Errorf("%s = %q, want %q, as testenv wrote it", projectSettings, got, "{}\n")
	}
}

// A settings file with other tools' hooks keeps them, and every other key, in order, once
// deinit has taken upool's handlers out (#191).
func TestDeinitKeepsOtherHooks(t *testing.T) {
	e := newInitEnv(t)
	e.WriteFile(projectSettings, otherSettings)
	p := harnessPoolIn(t, e)
	if r := runDeinit(t, p, procsAlive(), "--yes"); r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	var got, want jsonObject
	if err := json.Unmarshal([]byte(readFile(t, p.Env, projectSettings)), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(otherSettings), &want); err != nil {
		t.Fatal(err)
	}
	gb, _ := marshalJSON(got)
	wb, _ := marshalJSON(want)
	if !sameJSON(gb, wb) || !slices.Equal(got.keys, want.keys) {
		t.Errorf("%s =\n%s\nwant, as before upool's hooks,\n%s", projectSettings, gb, wb)
	}
}

// deinit refuses, naming the slot and changing nothing, while a slot is in use or holds work
// (#191): leased to a holder it cannot show dead, parked, dirty, with an Editor open, or being
// cloned by a live upool; and while a top-up runs.
func TestDeinitRefusesASlotInUse(t *testing.T) {
	const live = 4242
	start := time.Unix(1, 0)
	pid := live
	for _, tc := range []struct {
		name, want string
		setup      func(t *testing.T, p harnessPool)
	}{
		{"bare take", "slot-1 is leased to an upool take with no --holder, which may still " +
			"be running",
			func(t *testing.T, p harnessPool) {
				if r := runIn(t, p.Env, p.Repo, "take", "feature"); r.code != output.ExitOK {
					t.Fatalf("take: exit = %d; stderr %q", r.code, r.stderr)
				}
			}},
		{"live holder", "slot-2 is leased to session-1, which may still be running",
			func(t *testing.T, p harnessPool) {
				p.update(t, func(pl *pool.Pool) {
					pl.Slots[1].State = pool.StateLeased
					pl.Slots[1].Lease = &pool.Lease{LeaseID: "l1", Holder: "session-1",
						HolderPID: &pid, HolderPIDStart: &start, Confirmed: true,
						AcquiredAt: clock}
				})
			}},
		{"unconfirmed", "slot-1 is leased to session-2, which may still be running",
			func(t *testing.T, p harnessPool) {
				p.update(t, func(pl *pool.Pool) {
					pl.Slots[0].State = pool.StateLeasedUnconfirmed
					pl.Slots[0].Lease = &pool.Lease{LeaseID: "l2", Holder: "session-2",
						AcquiredAt: clock}
				})
			}},
		{"parked", "slot-2 is parked, holding work",
			func(t *testing.T, p harnessPool) {
				p.update(t, func(pl *pool.Pool) { pl.Slots[1].State = pool.StateParked })
			}},
		{"dirty", "slot-1 stays, since it is dirty",
			func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/worktrees/slot-1/scratch.txt", "work\n")
			}},
		{"editor", "slot-2 stays, since an Editor has it open",
			func(t *testing.T, p harnessPool) {
				p.WriteFile(".claude/worktrees/slot-2/Library/EditorInstance.json", "{}\n")
			}},
		{"cloning", "slot-1 is warming, by upool process 4242",
			func(t *testing.T, p harnessPool) {
				p.update(t, func(pl *pool.Pool) {
					pl.Slots[0].State = pool.StateWarming
					pl.Slots[0].OwnerPID, pl.Slots[0].OwnerPIDStart = &pid, &start
				})
			}},
		{"refresh", "upool refresh, process 4242, is topping the pool up",
			func(t *testing.T, p harnessPool) {
				p.update(t, func(pl *pool.Pool) {
					pl.RefreshJob = &pool.RefreshJob{PID: pid, PIDStart: start}
				})
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newHarnessPool(t)
			tc.setup(t, p)
			before := untouched(t, p)
			r := runDeinit(t, p, procsAlive(live), "--yes")
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, tc.want) ||
				!strings.Contains(r.stderr, "which is left as it was") {
				t.Errorf("exit %d, stderr %q; want %d and %q", r.code, r.stderr,
					output.ExitFailure, tc.want)
			}
			checkUnchanged(t, p, before)
		})
	}
}

// A slot leased to a holder that is shown dead, clean, goes like any other (#191).
func TestDeinitTakesASlotWhoseHolderIsDead(t *testing.T) {
	p := newHarnessPool(t)
	pid, start := 4242, time.Unix(1, 0)
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].State = pool.StateLeased
		pl.Slots[0].Lease = &pool.Lease{LeaseID: "l1", Holder: "session-1", HolderPID: &pid,
			HolderPIDStart: &start, Confirmed: true, AcquiredAt: clock}
	})
	if r := runDeinit(t, p, procsAlive(), "--yes"); r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	checkGone(t, p)
}

// A clean subagent worktree goes; a dirty one is left, listed with why, as the ordinary git
// worktree it is (#191).
func TestDeinitRemovesCleanAgentWorktrees(t *testing.T) {
	p := newHarnessPool(t)
	clean, dirty := p.Path(".claude/worktrees/agent-a1"), p.Path(".claude/worktrees/agent-b2")
	for _, dir := range []string{clean, dirty} {
		p.Git("worktree", "add", "-q", "--detach", dir, testenv.Base)
	}
	p.WriteFile(".claude/worktrees/agent-b2/notes.txt", "work\n")
	p.update(t, func(pl *pool.Pool) {
		for _, dir := range []string{clean, dirty} {
			pl.AgentWorktrees = append(pl.AgentWorktrees, pool.AgentWorktree{Path: dir,
				ParentSessionID: "session-1", ParentPID: 4242, CreatedAt: clock})
		}
	})
	r := runDeinit(t, p, procsAlive(4242), "--yes")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if _, err := os.Lstat(clean); !errors.Is(err, fs.ErrNotExist) || p.listed(clean) {
		t.Errorf("%s is still there (%v) or listed", clean, err)
	}
	if !p.listed(dirty) || readFile(t, p.Env, ".claude/worktrees/agent-b2/notes.txt") != "work\n" {
		t.Errorf("%s is gone, want it kept with its work", dirty)
	}
	if want := "leaving agent worktree " + dirty + ": it is dirty"; !strings.Contains(r.stdout,
		want) {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	checkGone(t, p)
}

// A slot whose directory is already gone loses only its entry; one that git does not list keeps
// its directory, which deinit says to delete by hand (#191).
func TestDeinitDropsGoneAndUnlistedSlots(t *testing.T) {
	p := newHarnessPool(t)
	p.Git("worktree", "remove", p.slot("slot-1"))
	p.Git("worktree", "remove", p.slot("slot-2"))
	p.WriteFile(".claude/worktrees/slot-2/left.txt", "left\n")
	r := runDeinit(t, p, procsAlive(), "--yes")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	for _, want := range []string{"removed slot-1's entry: its directory is already gone",
		"removed slot-2's entry, leaving " + p.slot("slot-2") + ", which git does not list"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
	}
	if readFile(t, p.Env, ".claude/worktrees/slot-2/left.txt") != "left\n" {
		t.Error("slot-2's directory is gone, want it left")
	}
	if _, ok, err := registry.Lookup(p.Home, p.Repo); err != nil || ok {
		t.Errorf("registry lookup = %v, %v; want no entry", ok, err)
	}
}

// deinit run in a slot refuses, naming the main checkout, rather than editing the slot's copies.
func TestDeinitInASlotRefuses(t *testing.T) {
	p := newHarnessPool(t)
	before := untouched(t, p)
	r := runProcs(t, p.Env, p.slot("slot-1"), procsAlive(), func() time.Time { return clock },
		"deinit", "--yes")
	repo, err := paths.Key(p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := "run upool deinit in " + repo; r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want %d and %q", r.code, r.stderr, output.ExitFailure, want)
	}
	checkUnchanged(t, p, before)
}

// In a repository upool has nothing in, deinit says so and changes nothing.
func TestDeinitWithNothingToRemove(t *testing.T) {
	e := testenv.New(t)
	r := runIn(t, e, e.Repo, "deinit", "--yes")
	if want := e.Repo + " has nothing of upool's to remove\n"; r.code != output.ExitOK ||
		r.stdout != want {
		t.Errorf("exit %d, stdout %q; want %d and %q", r.code, r.stdout, output.ExitOK, want)
	}
}

// The skill's directory goes with its SKILL.md, unless something else is in it, which is kept and
// named (#191).
func TestDeinitKeepsASkillDirectoryWithOtherFiles(t *testing.T) {
	p := newHarnessPool(t)
	for _, name := range skillFiles {
		p.WriteFile(name, "the skill\n")
	}
	p.WriteFile(".agents/skills/upool/mine.md", "mine\n")
	r := runDeinit(t, p, procsAlive(), "--yes")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	claude, agents := filepath.Dir(p.Path(skillFiles[0])), filepath.Dir(p.Path(skillFiles[1]))
	if _, err := os.Lstat(claude); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %v, want it gone", claude, err)
	}
	if _, err := os.Lstat(p.Path(skillFiles[1])); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %v, want it gone", skillFiles[1], err)
	}
	if readFile(t, p.Env, ".agents/skills/upool/mine.md") != "mine\n" {
		t.Error("mine.md is gone, want it kept")
	}
	want := "kept " + agents + ": something other than upool's SKILL.md is in it"
	if !strings.Contains(r.stdout, want) {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
}
