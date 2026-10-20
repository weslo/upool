package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of --fix on creating slots, which need internal/proc's start times: macOS only. The
// owner is this test process, alive at its real start time and dead at any other, which is how a
// reused PID reads (internal/proc).

// addCreating adds slot-3 to the harness pool as creating at path, owned by this process from
// start.
func (p doctorPool) addCreating(t *testing.T, path string, start time.Time) {
	t.Helper()
	pid := os.Getpid()
	p.update(t, func(pl *pool.Pool) {
		pl.Slots = append(pl.Slots, pool.Slot{Name: "slot-3", Path: path,
			State: pool.StateCreating, OwnerPID: &pid, OwnerPIDStart: &start})
	})
}

// An interrupted clone, its owner dead, is reported, and --fix removes its registration, its
// half-copied directory and its entry ((f) transition 21; A4).
func TestDoctorFixRemovesAnInterruptedClone(t *testing.T) {
	tests := []struct {
		name string
		// worktree is whether `git worktree add` got as far as making the slot.
		worktree bool
	}{
		{"registered and half-copied", true},
		{"its entry alone", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newDoctorPool(t)
			path := p.slot("slot-3")
			if tt.worktree {
				p.Git("worktree", "add", "-q", "--no-checkout", "--detach", path, testenv.Base)
				p.WriteFile(".claude/worktrees/slot-3/"+testenv.PlayerScript, "half")
			}
			p.addCreating(t, path, clock) // not this process's start time: a dead owner
			reason := "an interrupted clone: its owner, process " + strconv.Itoa(os.Getpid()) +
				", is gone"

			r := runIn(t, p.Env, p.Repo, "doctor", "--json")
			if c := findCheck(t, doctorChecks(t, r), "slot-3"); r.code != output.ExitFailure ||
				c.Result != checkFail || c.Reason != reason+"; upool doctor --fix removes it" {
				t.Errorf("without --fix: exit %d, slot-3 %s %q; want 1 and a failure saying "+
					"--fix removes it", r.code, c.Result, c.Reason)
			}
			if _, err := os.Stat(path); tt.worktree && err != nil {
				t.Errorf("slot-3 without --fix: %v, want it left", err)
			}

			r = runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
			if c := findCheck(t, doctorChecks(t, r), "slot-3"); r.code != output.ExitOK ||
				c.Result != checkPass || c.Reason != "fixed: removed "+reason {
				t.Errorf("--fix: exit %d, slot-3 %s %q; want 0 and fixed", r.code, c.Result,
					c.Reason)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("slot-3 after --fix: stat error %v, want it gone", err)
			}
			if p.listed(path) {
				t.Error("git worktree list still lists slot-3 after --fix")
			}
			if names := p.slotNames(t); strings.Join(names, " ") != "slot-1 slot-2" {
				t.Errorf("slots after --fix = %v, want slot-1 and slot-2", names)
			}
		})
	}
}

// A clone whose owner is alive is left alone, with or without --fix.
func TestDoctorFixLeavesALiveOwnersClone(t *testing.T) {
	p := newDoctorPool(t)
	path := p.slot("slot-3")
	p.Git("worktree", "add", "-q", "--no-checkout", "--detach", path, testenv.Base)
	start, err := proc.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	p.addCreating(t, path, start)
	for _, args := range [][]string{{"doctor", "--json"}, {"doctor", "--fix", "--json"}} {
		r := runIn(t, p.Env, p.Repo, args...)
		c := findCheck(t, doctorChecks(t, r), "slot-3")
		if want := "being cloned by process " + strconv.Itoa(os.Getpid()); r.code != output.ExitOK ||
			c.Result != checkPass || c.Reason != want {
			t.Errorf("%v: exit %d, slot-3 %s %q; want 0, pass, %q", args, r.code, c.Result,
				c.Reason, want)
		}
	}
	if _, err := os.Stat(path); err != nil || !p.listed(path) {
		t.Errorf("slot-3: stat error %v, listed %t; want it on disk and listed", err,
			p.listed(path))
	}
	if names := p.slotNames(t); len(names) != 3 {
		t.Errorf("slots = %v, want slot-3 kept", names)
	}
}

// A creating entry that a damaged state.json points outside .claude/worktrees is not removed,
// whatever its owner.
func TestDoctorFixRemovesNothingOutsideTheWorktreesDirectory(t *testing.T) {
	p := newDoctorPool(t)
	outside := p.Path("Assets")
	p.addCreating(t, outside, clock)
	r := runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
	c := findCheck(t, doctorChecks(t, r), "slot-3")
	if r.code != output.ExitFailure || c.Result != checkFail ||
		!strings.Contains(c.Reason, outside+" is not inside "+p.Repo+"/.claude/worktrees") {
		t.Errorf("exit %d, slot-3 %s %q; want 1 and a refusal", r.code, c.Result, c.Reason)
	}
	if _, err := os.Stat(p.Path(testenv.PlayerScript)); err != nil {
		t.Errorf("%s: %v, want it untouched", testenv.PlayerScript, err)
	}
}
