package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
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
			p.WriteFile(".claude/worktrees/slot-2/Library/EditorInstance.json",
				fmt.Sprintf(`{"process_id": %d}`, os.Getpid()))
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
	decodeData(t, r.stdout, &data)
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

// decodeData decodes the data of the envelope on stdout into v.
func decodeData(t *testing.T, stdout string, v any) {
	t.Helper()
	raw, err := json.Marshal(decode(t, stdout).Data)
	if err == nil {
		err = json.Unmarshal(raw, v)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// prunerFor is the pruner prune would run over p's pool, as this process, at the test clock, with
// procs as the process table.
func prunerFor(t *testing.T, p harnessPool, procs proc.Checker) pruner {
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

// maxDiskPool is a harness pool with a third slot, slot-3, every slot ready at base: slot-1 used at
// the test clock, slot-2 two days before it and slot-3 five, so that none is past shrink_after,
// and slot-3 is the least recently used.
func maxDiskPool(t *testing.T) harnessPool {
	t.Helper()
	p := newHarnessPool(t)
	p.Git("worktree", "add", "-q", "--detach", p.slot("slot-3"), testenv.Base)
	day := 24 * time.Hour
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[1].LastUsed = clock.Add(-2 * day)
		pl.Slots = append(pl.Slots, pool.Slot{Name: "slot-3", Path: p.slot("slot-3"),
			State: pool.StateReady, SeedHead: p.Head, LastUsed: clock.Add(-5 * day)})
	})
	return p
}

// gigabyteEach is a measure that gives every slot 1 GB private.
func gigabyteEach(string) (clone.Disk, error) {
	return clone.Disk{Private: 1 << 30, Apparent: 8 << 30}, nil
}

// slotsOf returns the slot names of candidates, in order.
func slotsOf(candidates []pruneCandidate) []string {
	var names []string
	for _, c := range candidates {
		names = append(names, c.Slot)
	}
	return names
}

// The acceptance test of #101, with injected disk measures of 1 GB a slot, 3 GB in all. With
// max_disk at 2.5 GB, the least recently used idle ready slot, slot-3, is listed, though no slot is
// past shrink_after, and is removed with --yes; at 1.5 GB slot-2 follows it; at 0, slot-1 still
// stays, since prune never goes below ready; unset, nothing is listed.
func TestPruneMaxDisk(t *testing.T) {
	for _, tt := range []struct {
		maxDisk string
		want    []string
	}{
		{"", nil},
		{"2.5", []string{"slot-3"}},
		{"1.5", []string{"slot-3", "slot-2"}},
		{"0", []string{"slot-3", "slot-2"}},
	} {
		t.Run("max_disk "+tt.maxDisk, func(t *testing.T) {
			p := maxDiskPool(t)
			if tt.maxDisk != "" {
				p.Setenv(config.EnvName("max_disk"), tt.maxDisk)
			}
			pr := prunerFor(t, p, procsAlive(os.Getpid()))
			pr.measure = gigabyteEach
			got, err := pr.candidates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if names := slotsOf(got.Candidates); !slices.Equal(names, tt.want) {
				t.Fatalf("candidates = %q, want %q", names, tt.want)
			}
			if len(tt.want) == 0 {
				return
			}
			why := got.Candidates[0].Why
			want := "the least recently used, idle 5d, while the pool's 3.0 GB private is over " +
				"max_disk"
			if !strings.HasPrefix(why, want) {
				t.Errorf("slot-3's why = %q, want it to start %q", why, want)
			}
			pr.remove(t.Context(), &got.Candidates[0])
			if !got.Candidates[0].Removed {
				t.Fatalf("removing slot-3: %s", got.Candidates[0].Error)
			}
			if names := p.slotNames(t); slices.Contains(names, "slot-3") ||
				p.listed(p.slot("slot-3")) {
				t.Errorf("slots = %q, want slot-3 gone from state.json and git's list", names)
			}
		})
	}
}

// max_disk never takes a slot that is not idle and ready, nor one an Editor holds: with slot-3 so
// held, slot-2, the next least recently used, is listed instead. A slot past shrink_after is listed
// once, for its age, and its size counts toward the budget.
func TestPruneMaxDiskTakesOnlyIdleReadySlots(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, p harnessPool)
		want  []string
	}{
		{"slot-3 leased", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) {
				pl.Slots[2].State = pool.StateLeased
				pl.Slots[2].Lease = &pool.Lease{LeaseID: "id", AcquiredAt: clock, Confirmed: true,
					Lane: pool.LaneScript}
			})
		}, []string{"slot-2"}},
		{"slot-3 parked", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) { pl.Slots[2].State = pool.StateParked })
		}, []string{"slot-2"}},
		{"slot-3 needs-reset", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) { pl.Slots[2].State = pool.StateNeedsReset })
		}, []string{"slot-2"}},
		{"slot-3 Editor-held", func(t *testing.T, p harnessPool) {
			writeLedger(t, p, ledger.Editor{Project: p.slot("slot-3"), PID: os.Getpid(),
				Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
		}, []string{"slot-2"}},
		{"slot-3 dirty", func(t *testing.T, p harnessPool) {
			p.WriteFile(".claude/worktrees/slot-3/left-behind.txt", "x\n")
		}, []string{"slot-2"}},
		{"slot-3 not merged into base", func(t *testing.T, p harnessPool) {
			slot := p.slot("slot-3")
			p.Git("-C", slot, "checkout", "-q", "-b", "elsewhere")
			p.WriteFile(".claude/worktrees/slot-3/elsewhere.txt", "x\n")
			p.Git("-C", slot, "add", "elsewhere.txt")
			p.Git("-C", slot, "commit", "-q", "-m", "a commit base does not have")
			p.Git("-C", slot, "checkout", "-q", "--detach")
		}, []string{"slot-2"}},
		{"slot-3 holding a worktree", func(t *testing.T, p harnessPool) {
			p.WriteFile(".git/info/exclude", ".claude/worktrees/\n")
			p.Git("-C", p.slot("slot-3"), "worktree", "add", "-q", "--detach",
				filepath.Join(p.slot("slot-3"), ".claude", "worktrees", "inner"))
		}, []string{"slot-2"}},
		{"slot-3 past shrink_after", func(t *testing.T, p harnessPool) {
			p.update(t, func(pl *pool.Pool) { pl.Slots[2].LastUsed = monthAgo })
		}, []string{"slot-3"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := maxDiskPool(t)
			p.Setenv(config.EnvName("max_disk"), "2.5")
			tt.setup(t, p)
			pr := prunerFor(t, p, procsAlive(os.Getpid()))
			pr.measure = gigabyteEach
			got, err := pr.candidates(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if names := slotsOf(got.Candidates); !slices.Equal(names, tt.want) {
				t.Errorf("candidates = %q, want %q", names, tt.want)
			}
		})
	}
}

// A max_disk that is no budget, negative, nan or inf, is refused, saying where it came from, before
// prune looks at a slot: on a pool with no idle slot past ready, too, where there is nothing to
// measure.
func TestPruneRefusesAMaxDiskThatIsNoBudget(t *testing.T) {
	for _, value := range []string{"-1", "nan", "inf"} {
		t.Run(value, func(t *testing.T) {
			p := prunePool(t)
			p.Setenv(config.EnvName("ready"), "2")
			p.Setenv(config.EnvName("max_disk"), value)
			r := runIn(t, p.Env, p.Repo, "prune")
			want := "; it must be a number of 0 or more\n"
			if r.code != output.ExitFailure ||
				!strings.HasPrefix(r.stderr, "upool: max_disk is ") ||
				!strings.HasSuffix(r.stderr, ", from env"+want) {
				t.Errorf("exit %d, stderr %q; want 1 and a refusal of max_disk %s", r.code,
					r.stderr, value)
			}
		})
	}
}

// agentPool is a harness pool, its slots both used at the test clock, with one subagent worktree,
// detached at base, whose entry has parent and createdAt.
func agentPool(t *testing.T, parent int, createdAt time.Time) (harnessPool, string) {
	t.Helper()
	p := newHarnessPool(t)
	agent := p.Path(".claude/worktrees/agent-0123456789abcdef0")
	p.Git("worktree", "add", "-q", "--detach", agent, testenv.Base)
	p.update(t, func(pl *pool.Pool) {
		pl.AgentWorktrees = []pool.AgentWorktree{{Path: agent, ParentSessionID: "session-1",
			ParentPID: parent, CreatedAt: createdAt}}
	})
	return p, agent
}

// goneParent is a PID no process in the tests' process table has.
const goneParent = 4321

// The acceptance test of #102. A clean agent worktree whose parent is gone is listed, and removed
// with --yes, its entry and its worktree; a dirty one is kept and listed, as is one whose subagent
// committed on its detached HEAD; a young one with a live parent is not listed; and one older than
// agent_ttl is listed though its parent lives.
func TestPruneAgents(t *testing.T) {
	t.Run("clean, parent gone", func(t *testing.T) {
		p, agent := agentPool(t, goneParent, clock)
		r := runPrune(t, p)
		want := p.Repo + ":\n  would remove agent worktree " + agent + ": its parent, process " +
			"4321, is gone; clean\nthis was a dry run: upool prune --yes removes them\n"
		if r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
		r = runPrune(t, p, "--yes")
		if want := p.Repo + ":\n  removed agent worktree " + agent + ": its parent, process " +
			"4321, is gone; clean\n"; r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
		if got := p.loadPool(t).AgentWorktrees; len(got) != 0 {
			t.Errorf("agent_worktrees = %+v, want none", got)
		}
		if p.listed(agent) {
			t.Error("git still lists the agent worktree")
		}
		if _, err := os.Stat(agent); !os.IsNotExist(err) {
			t.Errorf("the agent worktree's directory: stat error = %v, want it gone", err)
		}
	})
	for _, tt := range []struct {
		name  string
		dirty func(t *testing.T, p harnessPool, agent string)
		why   string
	}{
		{"dirty", func(t *testing.T, p harnessPool, agent string) {
			if err := os.WriteFile(filepath.Join(agent, "left.txt"), []byte("x\n"),
				0o644); err != nil {
				t.Fatal(err)
			}
		}, "it is dirty: ?? left.txt"},
		{"a commit on its detached HEAD", func(t *testing.T, p harnessPool, agent string) {
			if err := os.WriteFile(filepath.Join(agent, "work.txt"), []byte("x\n"),
				0o644); err != nil {
				t.Fatal(err)
			}
			p.Git("-C", agent, "add", "work.txt")
			p.Git("-C", agent, "commit", "-q", "-m", "the subagent's work")
		}, "it is dirty: HEAD is on no branch"},
	} {
		t.Run(tt.name+", parent gone", func(t *testing.T) {
			p, agent := agentPool(t, goneParent, clock)
			tt.dirty(t, p, agent)
			want := p.Repo + ":\n  keeping agent worktree " + agent + ": its parent, process " +
				"4321, is gone, but " + tt.why + "\n"
			for _, args := range [][]string{nil, {"--yes"}} {
				if r := runPrune(t, p, args...); r.stdout != want {
					t.Errorf("prune %v: stdout = %q, want %q", args, r.stdout, want)
				}
			}
			if len(p.loadPool(t).AgentWorktrees) != 1 || !p.listed(agent) {
				t.Error("the dirty agent worktree went, want it kept")
			}
		})
	}
	t.Run("young, parent alive", func(t *testing.T) {
		p, _ := agentPool(t, os.Getpid(), clock)
		if r := runPrune(t, p, "--yes"); r.stdout != p.Repo+": nothing to prune\n" {
			t.Errorf("stdout = %q, want nothing to prune", r.stdout)
		}
		if len(p.loadPool(t).AgentWorktrees) != 1 {
			t.Error("the young agent worktree went, want it kept")
		}
	})
	t.Run("past agent_ttl, parent alive", func(t *testing.T) {
		p, agent := agentPool(t, os.Getpid(), clock.Add(-25*time.Hour))
		want := p.Repo + ":\n  would remove agent worktree " + agent + ": made 1d1h ago, past " +
			"agent_ttl 1d; clean\nthis was a dry run: upool prune --yes removes them\n"
		if r := runPrune(t, p); r.stdout != want {
			t.Errorf("stdout = %q, want %q", r.stdout, want)
		}
	})
}

// removeAgent asks again before it removes: an agent worktree an Editor opened between the
// listing and the removal is put back, and one git refuses, here a locked one, is put back too.
func TestPruneAgentRemovalThatFails(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, p harnessPool, agent string)
		want   string
	}{
		{"an Editor opened meanwhile", func(t *testing.T, p harnessPool, agent string) {
			writeLedger(t, p, ledger.Editor{Project: agent, PID: os.Getpid(),
				Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
		}, "an Editor has it open now; it stays"},
		{"locked", func(t *testing.T, p harnessPool, agent string) {
			p.Git("worktree", "lock", agent)
		}, "; it stays"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, agent := agentPool(t, goneParent, clock)
			pr := prunerFor(t, p, procsAlive(os.Getpid()))
			var r poolPrune
			if err := pr.agents(t.Context(), &r); err != nil || len(r.Agents) != 1 {
				t.Fatalf("agents = %+v, %v; want the agent worktree", r.Agents, err)
			}
			tt.change(t, p, agent)
			pr.removeAgent(t.Context(), &r.Agents[0])
			if c := r.Agents[0]; c.Removed || !strings.HasSuffix(c.Error, tt.want) {
				t.Errorf("candidate = %+v, want not removed and an error ending %q", c, tt.want)
			}
			if got := p.loadPool(t).AgentWorktrees; len(got) != 1 || got[0].Path != agent ||
				!p.listed(agent) {
				t.Errorf("agent_worktrees = %+v, want it back and in git's list", got)
			}
		})
	}
}

// An agent worktree whose entry changed between the listing and the removal, as when the hook
// made a new subagent worktree at the same path, is refused and left as it is.
func TestPruneAgentChangedMeanwhile(t *testing.T) {
	p, agent := agentPool(t, goneParent, clock)
	pr := prunerFor(t, p, procsAlive(os.Getpid()))
	var r poolPrune
	if err := pr.agents(t.Context(), &r); err != nil || len(r.Agents) != 1 {
		t.Fatalf("agents = %+v, %v; want the agent worktree", r.Agents, err)
	}
	p.update(t, func(pl *pool.Pool) {
		pl.AgentWorktrees[0].ParentPID, pl.AgentWorktrees[0].CreatedAt = os.Getpid(), clock.Add(
			time.Minute)
	})
	pr.removeAgent(t.Context(), &r.Agents[0])
	if c := r.Agents[0]; c.Removed || c.Error != "it changed in state.json meanwhile" {
		t.Errorf("candidate = %+v, want it refused as changed", c)
	}
	if got := p.loadPool(t).AgentWorktrees; len(got) != 1 || got[0].ParentPID != os.Getpid() ||
		!p.listed(agent) {
		t.Errorf("agent_worktrees = %+v, want the new entry kept and the worktree in git's list",
			got)
	}
}

// An agent worktree whose directory is gone, or that git no longer lists, is left out: prune
// neither fails on it nor drops its entry, and goes on to remove the pool's candidate slot.
func TestPruneLeavesAMissingAgentWorktreeOut(t *testing.T) {
	for _, tt := range []struct {
		name string
		gone func(t *testing.T, p harnessPool, agent string)
	}{
		{"its directory gone", func(t *testing.T, p harnessPool, agent string) {
			if err := os.RemoveAll(agent); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed from git", func(t *testing.T, p harnessPool, agent string) {
			p.Git("worktree", "remove", agent)
		}},
		{"not in git's list, its directory still there", func(t *testing.T, p harnessPool,
			agent string) {
			if err := os.RemoveAll(p.Path(".git/worktrees/" + filepath.Base(agent))); err != nil {
				t.Fatal(err)
			}
			if p.listed(agent) {
				t.Fatal("git still lists the agent worktree")
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, agent := agentPool(t, goneParent, clock)
			p.update(t, func(pl *pool.Pool) { pl.Slots[1].LastUsed = monthAgo })
			tt.gone(t, p, agent)
			r := runPrune(t, p, "--yes")
			if want := p.Repo + ":\n  removed slot-2: " + slot2Why + "\n"; r.stdout != want {
				t.Errorf("stdout = %q, want %q", r.stdout, want)
			}
			if got := p.loadPool(t).AgentWorktrees; len(got) != 1 || got[0].Path != agent {
				t.Errorf("agent_worktrees = %+v, want the entry left alone", got)
			}
		})
	}
}

// A removal that fails after git has dropped the agent worktree from its list, here on a directory
// it cannot delete, leaves the entry out, and names what is left to delete.
func TestPruneAgentRemovalGitLetsGoOf(t *testing.T) {
	p, agent := agentPool(t, goneParent, clock)
	pr := prunerFor(t, p, procsAlive(os.Getpid()))
	var r poolPrune
	if err := pr.agents(t.Context(), &r); err != nil || len(r.Agents) != 1 {
		t.Fatalf("agents = %+v, %v; want the agent worktree", r.Agents, err)
	}
	stuck := filepath.Join(agent, "Library", "stuck")
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
	pr.removeAgent(t.Context(), &r.Agents[0])
	want := "; git no longer lists it: delete what is left of " + agent + " by hand"
	if c := r.Agents[0]; c.Removed || !strings.HasSuffix(c.Error, want) {
		t.Errorf("candidate = %+v, want an error ending %q", c, want)
	}
	if p.listed(agent) {
		t.Fatal("git still lists the agent worktree, so the test did not reach the case")
	}
	if got := p.loadPool(t).AgentWorktrees; len(got) != 0 {
		t.Errorf("agent_worktrees = %+v, want the entry left out", got)
	}
}

// unsupportedProcs answers as internal/proc does off macOS, where it reads no process: every call
// fails with errors.ErrUnsupported.
type unsupportedProcs struct{}

func (unsupportedProcs) Start(int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("reading start times: %w", errors.ErrUnsupported)
}

func (unsupportedProcs) Alive(int, time.Time) (bool, error) {
	return false, fmt.Errorf("reading start times: %w", errors.ErrUnsupported)
}

func (unsupportedProcs) Name(int) (string, error) {
	return "", fmt.Errorf("reading process names: %w", errors.ErrUnsupported)
}

// EditorsAt reads no process, as off macOS.
func (unsupportedProcs) EditorsAt(string) ([]int, error) { return nil, errors.ErrUnsupported }

// Where no process can be read, an agent worktree's parent counts as alive: a young one is not
// due, and one past agent_ttl is, for its age alone.
func TestPruneAgentsWhereNoProcessCanBeRead(t *testing.T) {
	for _, tt := range []struct {
		name    string
		created time.Time
		want    string
	}{
		{"young", clock, ""},
		{"past agent_ttl", clock.Add(-25 * time.Hour), "made 1d1h ago, past agent_ttl 1d; clean"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := agentPool(t, goneParent, tt.created)
			pr := prunerFor(t, p, unsupportedProcs{})
			var r poolPrune
			if err := pr.agents(t.Context(), &r); err != nil {
				t.Fatal(err)
			}
			got := ""
			if len(r.Agents) == 1 {
				got = r.Agents[0].Why
			}
			if len(r.Agents) > 1 || got != tt.want {
				t.Errorf("agents = %+v, want why %q", r.Agents, tt.want)
			}
		})
	}
}
