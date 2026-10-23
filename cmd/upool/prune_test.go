package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// The tests of `upool prune` (#100), on the harness pool: slot-1 and slot-2, both ready at base,
// with ready the default, 1, and shrink_after the default, 14 days. slot-1 was used at the test
// clock; slot-2, unless a test says otherwise, 30 days before it.

// monthAgo is 30 days before the test clock, past shrink_after.
var monthAgo = clock.Add(-30 * 24 * time.Hour)

// prunePool is a harness pool whose slot-2 was last used monthAgo.
func prunePool(t *testing.T) harnessPool {
	t.Helper()
	p := newHarnessPool(t)
	p.update(t, func(pl *pool.Pool) { pl.Slots[1].LastUsed = monthAgo })
	return p
}

// slot2Why is why slot-2 is a candidate, as prune says it.
const slot2Why = "idle 30d, past shrink_after 14d; beyond ready = 1; clean, and merged into main"

// runPrune runs `upool prune args…` in p's main checkout, with this process alive in the process
// table, and fails the test unless it exits 0.
func runPrune(t *testing.T, p harnessPool, args ...string) result {
	t.Helper()
	r := runProcs(t, p.Env, p.Repo, procsAlive(os.Getpid()), func() time.Time { return clock },
		append([]string{"prune"}, args...)...)
	if r.code != output.ExitOK {
		t.Fatalf("prune %v: exit %d, stdout %q, stderr %q; want 0", args, r.code, r.stdout,
			r.stderr)
	}
	return r
}

// The acceptance test of #100. Without --yes nothing changes and the candidate, slot-2, is listed
// with why; with --yes it alone goes: its entry, git's record of it and its directory, while
// slot-1, the one ready = 1 keeps, stays.
func TestPrune(t *testing.T) {
	p := prunePool(t)
	before, _ := json.Marshal(p.loadPool(t))
	r := runPrune(t, p)
	want := p.Repo + ":\n  would remove slot-2: " + slot2Why + "\nthis was a dry run: upool " +
		"prune --yes removes them\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if after, _ := json.Marshal(p.loadPool(t)); string(after) != string(before) {
		t.Errorf("state.json = %s after a dry run, want it unchanged: %s", after, before)
	}
	if !p.listed(p.slot("slot-2")) {
		t.Error("slot-2 left git's list after a dry run")
	}

	r = runPrune(t, p, "--yes")
	if want := p.Repo + ":\n  removed slot-2: " + slot2Why + "\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if got := strings.Join(p.slotNames(t), " "); got != "slot-1" {
		t.Errorf("slots = %s, want slot-1 alone", got)
	}
	if p.listed(p.slot("slot-2")) {
		t.Error("git still lists slot-2")
	}
	if _, err := os.Stat(p.slot("slot-2")); !os.IsNotExist(err) {
		t.Errorf("slot-2's directory: stat error = %v, want it gone", err)
	}
	if !p.listed(p.slot("slot-1")) {
		t.Error("slot-1 left git's list")
	}
	r = runPrune(t, p)
	if want := p.Repo + ": nothing to prune\n"; r.stdout != want {
		t.Errorf("stdout after the removal = %q, want %q", r.stdout, want)
	}
}

// A leased, parked or Editor-held slot is never listed, however long idle; nor is one idle for
// less than shrink_after, one that is dirty, one whose HEAD is not merged into base, or one of the
// ready slots ready keeps.
func TestPruneListsOnlyCandidates(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, p harnessPool)
	}{
		{"leased", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[1].State = pool.StateLeased
				pl.Slots[1].Lease = &pool.Lease{LeaseID: "id", AcquiredAt: monthAgo,
					Confirmed: true, Lane: pool.LaneScript}
			})
		}},
		{"parked", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) { pl.Slots[1].State = pool.StateParked })
		}},
		{"an Editor upool opened", func(t *testing.T, p harnessPool) {
			writeLedger(t, p, ledger.Editor{Project: p.slot("slot-2"), PID: os.Getpid(),
				Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
		}},
		{"an Editor upool did not open", func(t *testing.T, p harnessPool) {
			p.WriteFile(".claude/worktrees/slot-2/Library/EditorInstance.json", "{}\n")
		}},
		{"idle less than shrink_after", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[1].LastUsed = clock.Add(-13 * 24 * time.Hour)
			})
		}},
		{"dirty", func(t *testing.T, p harnessPool) {
			p.WriteFile(".claude/worktrees/slot-2/left-behind.txt", "x\n")
		}},
		{"not merged into base", func(t *testing.T, p harnessPool) {
			p.inSlot2("checkout", "-q", "-b", "elsewhere")
			p.WriteFile(".claude/worktrees/slot-2/elsewhere.txt", "x\n")
			p.inSlot2("add", "elsewhere.txt")
			p.inSlot2("commit", "-q", "-m", "a commit base does not have")
			p.inSlot2("checkout", "-q", "--detach")
		}},
		{"kept by ready", func(t *testing.T, p harnessPool) {
			p.Setenv(config.EnvName("ready"), "2")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := prunePool(t)
			tt.setup(t, p)
			if r := runPrune(t, p); r.stdout != p.Repo+": nothing to prune\n" {
				t.Errorf("stdout = %q, want nothing to prune", r.stdout)
			}
			if r := runPrune(t, p, "--yes"); r.stdout != p.Repo+": nothing to prune\n" {
				t.Errorf("stdout with --yes = %q, want nothing to prune", r.stdout)
			}
			if got := strings.Join(p.slotNames(t), " "); got != "slot-1 slot-2" {
				t.Errorf("slots = %s, want both kept", got)
			}
		})
	}
}

// inSlot2 runs git in the harness pool's slot-2.
func (p harnessPool) inSlot2(args ...string) string {
	return p.Git(append([]string{"-C", p.slot("slot-2")}, args...)...)
}

// A slot that was never used, cloned before the recipe recorded last_used, counts as idle since
// its directory was made (A42): a slot made a month ago is a candidate, and one made today is the
// slot ready keeps.
func TestPruneReckonsANeverUsedSlotFromItsDirectory(t *testing.T) {
	for _, old := range []bool{true, false} {
		t.Run(map[bool]string{true: "made a month ago", false: "made today"}[old],
			func(t *testing.T) {
				p := prunePool(t)
				p.update(t, func(pl *pool.Pool) { pl.Slots[1].LastUsed = time.Time{} })
				want := p.Repo + ": nothing to prune\n"
				if old {
					if err := os.Chtimes(p.slot("slot-2"), monthAgo, monthAgo); err != nil {
						t.Fatal(err)
					}
					want = p.Repo + ":\n  would remove slot-2: " + slot2Why + "\nthis was a " +
						"dry run: upool prune --yes removes them\n"
				}
				if r := runPrune(t, p); r.stdout != want {
					t.Errorf("stdout = %q, want %q", r.stdout, want)
				}
			})
	}
}

// --all prunes every registered pool, wherever it runs; without it, prune needs to run in a pool,
// and it takes no arguments. --json reports the dry run and each candidate.
func TestPruneAllAndItsRefusals(t *testing.T) {
	p := prunePool(t)
	elsewhere := tempDir(t)
	r := runIn(t, p.Env, elsewhere, "prune", "--all", "--json")
	var data pruneReport
	raw, err := json.Marshal(decode(t, r.stdout).Data)
	if err == nil {
		err = json.Unmarshal(raw, &data)
	}
	if err != nil {
		t.Fatal(err)
	}
	if r.code != output.ExitOK || !data.DryRun || len(data.Pools) != 1 ||
		len(data.Pools[0].Candidates) != 1 || data.Pools[0].Candidates[0].Slot != "slot-2" ||
		!data.Pools[0].Candidates[0].IdleSince.Equal(monthAgo) {
		t.Errorf("exit %d, data %+v; want a dry run listing slot-2, idle since %s", r.code, data,
			monthAgo)
	}
	r = runIn(t, p.Env, elsewhere, "prune")
	if want := "is not inside a registered pool"; r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, want) {
		t.Errorf("prune outside a pool: exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if r := runIn(t, p.Env, filepath.Join(p.Repo), "prune", "now"); r.code != output.ExitFailure {
		t.Errorf("prune now: exit %d, want 1", r.code)
	}
}

// prunerFor is the pruner prune would run over p's pool, as this process, at the test clock, with
// procs as the process table.
func prunerFor(t *testing.T, p harnessPool, procs *editorProcs) pruner {
	t.Helper()
	inv := &invocation{home: p.Home, getenv: p.Getenv, environ: p.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid(), procs: procs,
		stderr: io.Discard}
	pools, err := readPools(t.Context(), p.Home, func(registry.Repo) bool { return true })
	if err != nil || len(pools) != 1 {
		t.Fatalf("readPools = %+v, %v; want the harness pool", pools, err)
	}
	return pruner{inv: inv, rp: pools[0], g: git.Client{Runner: git.Exec{}, Env: p.Environ()}}
}

// slot2Candidate returns slot-2 as candidates finds it, the one candidate.
func slot2Candidate(t *testing.T, pr pruner) pruneCandidate {
	t.Helper()
	got, err := pr.candidates(t.Context())
	if err != nil || len(got.Candidates) != 1 || got.Candidates[0].Slot != "slot-2" {
		t.Fatalf("candidates = %+v, %v; want slot-2", got, err)
	}
	return got.Candidates[0]
}

// A worktree inside a slot, as the hook's pass-through and subagent branches make one for a
// session in the slot (A13), keeps the slot, with the worktree and its work: git worktree remove
// would delete them, and a slot's git status does not show an ignored .claude/worktrees.
func TestPruneKeepsASlotHoldingAWorktree(t *testing.T) {
	p := prunePool(t)
	p.WriteFile(".git/info/exclude", ".claude/worktrees/\n")
	inner := filepath.Join(p.slot("slot-2"), ".claude", "worktrees", "inner")
	p.inSlot2("worktree", "add", "-q", "--detach", inner)
	work := filepath.Join(inner, "work.txt")
	if err := os.WriteFile(work, []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := runPrune(t, p, "--yes"); r.stdout != p.Repo+": nothing to prune\n" {
		t.Errorf("stdout = %q, want nothing to prune", r.stdout)
	}
	if _, err := os.Stat(work); err != nil {
		t.Errorf("%s is gone: %v", work, err)
	}
}

// A ready slot whose directory is gone, or that git does not list, is left to doctor: prune
// neither fails on it nor gives it a place among those ready keeps, so a healthy idle slot is
// kept in its stead.
func TestPruneLeavesAMissingSlotToDoctor(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, p harnessPool)
	}{
		{"slot-2's directory gone", func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.slot("slot-2")); err != nil {
				t.Fatal(err)
			}
		}},
		{"slot-2 not in git's list", func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.Path(".git/worktrees/slot-2")); err != nil {
				t.Fatal(err)
			}
		}},
		{"slot-1, the most recently used, gone", func(t *testing.T, p harnessPool) {
			if err := os.RemoveAll(p.slot("slot-1")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := prunePool(t)
			tt.setup(t, p)
			if r := runPrune(t, p, "--yes"); r.stdout != p.Repo+": nothing to prune\n" {
				t.Errorf("stdout = %q, want nothing to prune", r.stdout)
			}
			if got := strings.Join(p.slotNames(t), " "); got != "slot-1 slot-2" {
				t.Errorf("slots = %s, want both left for doctor or kept", got)
			}
		})
	}
}

// A removal that git refuses before it lets go of the worktree, here a locked one, puts the entry
// back as it was; one that fails after git has dropped the worktree from its list, here on a
// directory it cannot delete, leaves the entry out, since git no longer lists the slot, and names
// what is left to delete.
func TestPruneRemovalThatFails(t *testing.T) {
	t.Run("git refuses", func(t *testing.T) {
		p := prunePool(t)
		pr := prunerFor(t, p, procsAlive(os.Getpid()))
		c := slot2Candidate(t, pr)
		p.Git("worktree", "lock", p.slot("slot-2"))
		pr.remove(t.Context(), &c)
		if c.Removed || !strings.HasSuffix(c.Error, "; it stays ready") {
			t.Errorf("candidate = %+v, want not removed and its entry put back", c)
		}
		if got := strings.Join(p.slotNames(t), " "); got != "slot-1 slot-2" {
			t.Errorf("slots = %s, want slot-2 back", got)
		}
		if s := p.loadPool(t).Slots[1]; s.State != pool.StateReady || !s.LastUsed.Equal(monthAgo) {
			t.Errorf("slot-2 = %s, last used %s; want it ready as it was", s.State, s.LastUsed)
		}
	})
	t.Run("git lets go part way", func(t *testing.T) {
		p := prunePool(t)
		pr := prunerFor(t, p, procsAlive(os.Getpid()))
		c := slot2Candidate(t, pr)
		stuck := filepath.Join(p.slot("slot-2"), "Library", "stuck")
		if err := os.MkdirAll(stuck, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stuck, "f"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stuck, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) }) // so that the test can clean up
		pr.remove(t.Context(), &c)
		want := "; git no longer lists it, so it is out of the pool: delete what is left of " +
			p.slot("slot-2") + " by hand"
		if c.Removed || !strings.HasSuffix(c.Error, want) {
			t.Errorf("candidate = %+v, want an error ending %q", c, want)
		}
		if p.listed(p.slot("slot-2")) {
			t.Fatal("git still lists slot-2, so the test did not reach the case")
		}
		if got := strings.Join(p.slotNames(t), " "); got != "slot-1" {
			t.Errorf("slots = %s, want slot-2's entry left out", got)
		}
	})
}

// remove asks again, once the slot is out of circulation, what candidates asked: a slot leased
// meanwhile, used and released meanwhile, or held meanwhile as a clone's seed or by refresh's move
// is refused and left as it is, and one an Editor opened meanwhile is put back.
func TestPruneChecksAgainBeforeItRemoves(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(s *pool.Slot)
		state  pool.State
	}{
		{"used and released meanwhile", func(s *pool.Slot) { s.LastUsed = clock },
			pool.StateReady},
		{"held as a seed meanwhile", func(s *pool.Slot) {
			pid := os.Getpid()
			s.State, s.OwnerPID, s.OwnerPIDStart = pool.StateWarming, &pid, &clock
		}, pool.StateWarming},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := prunePool(t)
			pr := prunerFor(t, p, procsAlive(os.Getpid()))
			c := slot2Candidate(t, pr)
			p.update(t, func(pl *pool.Pool) { tt.change(&pl.Slots[1]) })
			pr.remove(t.Context(), &c)
			if c.Removed || c.Error == "" {
				t.Errorf("candidate = %+v, want it refused", c)
			}
			s := p.loadPool(t).Slots[1]
			if s.Name != "slot-2" || s.State != tt.state || !p.listed(s.Path) {
				t.Errorf("slot-2 = %+v, want it %s and in git's list", s, tt.state)
			}
		})
	}
	t.Run("leased meanwhile", func(t *testing.T) {
		p := prunePool(t)
		pr := prunerFor(t, p, procsAlive(os.Getpid()))
		c := slot2Candidate(t, pr)
		p.update(t, func(pl *pool.Pool) {
			pl.Slots[1].State = pool.StateLeased
			pl.Slots[1].Lease = &pool.Lease{LeaseID: "id", AcquiredAt: clock, Confirmed: true,
				Lane: pool.LaneScript}
		})
		pr.remove(t.Context(), &c)
		if c.Removed || !strings.Contains(c.Error, "changed in state.json meanwhile") {
			t.Errorf("candidate = %+v, want it refused as changed", c)
		}
		if s := p.loadPool(t).Slots[1]; s.State != pool.StateLeased || !p.listed(s.Path) {
			t.Errorf("slot-2 = %s, want it still leased and in git's list", s.State)
		}
	})
	t.Run("an Editor opened meanwhile", func(t *testing.T) {
		p := prunePool(t)
		pr := prunerFor(t, p, procsAlive(os.Getpid()))
		c := slot2Candidate(t, pr)
		writeLedger(t, p, ledger.Editor{Project: p.slot("slot-2"), PID: os.Getpid(),
			Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
		pr.remove(t.Context(), &c)
		if c.Removed || !strings.HasPrefix(c.Error, "an Editor has it open now") {
			t.Errorf("candidate = %+v, want it refused for the Editor", c)
		}
		if got := strings.Join(p.slotNames(t), " "); got != "slot-1 slot-2" ||
			!p.listed(p.slot("slot-2")) {
			t.Errorf("slots = %s, want slot-2 back and in git's list", got)
		}
	})
}

// A negative ready is no count: prune fails, saying where it came from, rather than panicking.
func TestPruneRefusesANegativeReady(t *testing.T) {
	p := prunePool(t)
	p.Setenv(config.EnvName("ready"), "-1")
	r := runIn(t, p.Env, p.Repo, "prune")
	if want := "upool: ready is -1 for " + p.Repo + ", from "; r.code != output.ExitFailure ||
		!strings.HasPrefix(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
	}
}
