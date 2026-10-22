package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
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
