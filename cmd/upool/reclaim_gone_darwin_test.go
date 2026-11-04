package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of a leased slot whose holder is dead and whose directory is gone, as the first
// benchmark run left three: archiving each session through the desktop app's session tool deleted
// its slot and sent upool no WorktreeRemove, and the pool, at max_slots, had nothing to hand out
// (#277). take, refresh and doctor --fix drop such a slot, and a clone replaces it ((f) transition
// 24); status shows it missing until then. The holder is this test process at a start time it did
// not start at, which internal/proc reads as dead, and the clones are real: macOS only.

// goneSlotRemovals are the two ways a test takes slot-1 off the disk: with its registration, as
// the app's archive left the slots it deleted, whose paths `git worktree list` no longer listed,
// and by deleting its directory alone, which leaves a registration the drop has to prune before a
// clone can take the name slot-1 again.
var goneSlotRemovals = []struct {
	name   string
	remove func(t *testing.T, e *testenv.Env)
	// found is what doctor finds wrong with the slot, after its path.
	found string
}{
	{"removed with its registration", func(_ *testing.T, e *testenv.Env) {
		e.Git("worktree", "remove", "--force", slotPath(e))
	}, " is not on disk; git worktree list does not list it"},
	{"its directory deleted", func(t *testing.T, e *testenv.Env) {
		if err := os.RemoveAll(slotPath(e)); err != nil {
			t.Fatal(err)
		}
	}, " is not on disk"},
}

// goneHolderPool runs init in a new environment whose max_slots is 1, leases its one slot, slot-1,
// on feature-x to a holder that is dead, and takes slot-1 off the disk with remove: a pool at
// max_slots with nothing to hand out.
func goneHolderPool(t *testing.T, remove func(t *testing.T, e *testenv.Env)) *testenv.Env {
	t.Helper()
	e := leasedPool(t)
	e.Setenv(config.EnvName("max_slots"), "1")
	pid := os.Getpid()
	setSlot(t, e, func(s *pool.Slot) {
		s.Lease.HolderPID, s.Lease.HolderPIDStart = &pid, &clock
	})
	remove(t, e)
	return e
}

// statusStates runs status in e's main checkout, in both forms, and returns each slot as
// name=state, failing the test unless the two forms agree.
func statusStates(t *testing.T, e *testenv.Env) string {
	t.Helper()
	r := runIn(t, e, e.Repo, "status", "--json")
	if r.code != output.ExitOK {
		t.Fatalf("status --json: exit %d, stderr %q", r.code, r.stderr)
	}
	data, _ := json.Marshal(decode(t, r.stdout).Data) // it was decoded from JSON, so it encodes
	var report statusReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, p := range report.Pools {
		for _, s := range p.Slots {
			states = append(states, s.Name+"="+string(s.State))
		}
	}
	r = runIn(t, e, e.Repo, "status")
	var rows []string
	for line := range strings.SplitSeq(r.stdout, "\n") {
		if f := strings.Fields(line); len(f) > 1 && strings.HasPrefix(f[0], "slot-") {
			rows = append(rows, f[0]+"="+f[1])
		}
	}
	got := strings.Join(states, " ")
	if human := strings.Join(rows, " "); r.code != output.ExitOK || human != got {
		t.Errorf("status: exit %d, slots %s; want 0 and the slots of --json, %s", r.code, human,
			got)
	}
	return got
}

// dropped is what take, refresh and doctor say of slot-1 once they have dropped it.
var dropped = "dropped slot-1 from the pool, since its holder, process " +
	strconv.Itoa(os.Getpid()) + ", is gone and its directory is not on disk"

// At max_slots, take drops the dead holder's gone slot, says so, clones a replacement from the
// main checkout under the same name and leases it; the dead holder's branch is kept. status shows
// the slot missing before.
func TestTakeDropsADeadHoldersGoneSlotAndClonesAReplacement(t *testing.T) {
	for _, tt := range goneSlotRemovals {
		t.Run(tt.name, func(t *testing.T) {
			e := goneHolderPool(t, tt.remove)
			if got := statusStates(t, e); got != "slot-1=missing" {
				t.Errorf("status before take: slots %s, want slot-1=missing", got)
			}
			r := runIn(t, e, e.Repo, "take", "feature-y")
			if r.code != output.ExitOK || r.stdout != slotPath(e)+"\n" {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and slot-1's path", r.code,
					r.stdout, r.stderr)
			}
			for _, want := range []string{"upool: " + dropped + "\n", "upool: cloned slot-1\n"} {
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
				}
			}
			if strings.Contains(r.stderr, "not cloning") {
				t.Errorf("stderr = %q, want no refusal to clone", r.stderr)
			}
			if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased" {
				t.Errorf("slots = %s, want the replacement slot-1 leased", got)
			}
			if got := gitIn(t, e, slotPath(e), "symbolic-ref", "--short", "HEAD"); got !=
				"feature-y" {
				t.Errorf("slot-1 HEAD = %s, want feature-y", got)
			}
			if got := e.Git("branch", "--list", "feature-x"); got == "" {
				t.Error("feature-x is gone, want the dead holder's branch kept")
			}
		})
	}
}

// A WorktreeRemove that reaches upool once the app has deleted the slot, the captured archive of a
// desktop session in slot-1, cannot judge the slot and leaves its lease, exiting 0 as the remove
// hook always does; take still drops the slot and clones its replacement.
func TestTakeDropsAGoneSlotTheRemoveHookLeft(t *testing.T) {
	e := goneHolderPool(t, goneSlotRemovals[0].remove)
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: e.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-remove"}); code != output.ExitOK ||
		!strings.Contains(errOut.String(), "checking whether slot-1 is clean") {
		t.Errorf("remove hook: exit %d, stderr %q; want 0 and why it could not judge slot-1",
			code, errOut.String())
	}
	if got := statusStates(t, e); got != "slot-1=missing" {
		t.Errorf("status after the remove hook: slots %s, want slot-1=missing", got)
	}
	r := runIn(t, e, e.Repo, "take", "feature-y")
	if r.code != output.ExitOK || r.stdout != slotPath(e)+"\n" ||
		!strings.Contains(r.stderr, "upool: "+dropped+"\n") {
		t.Errorf("take: exit %d, stdout %q, stderr %q; want 0, slot-1's path, and %q", r.code,
			r.stdout, r.stderr, dropped)
	}
}

// At max_slots, refresh drops the dead holder's gone slot before its top-up, which clones a
// replacement ready; status shows the slot missing before.
func TestRefreshDropsADeadHoldersGoneSlotAndClonesAReplacement(t *testing.T) {
	for _, tt := range goneSlotRemovals {
		t.Run(tt.name, func(t *testing.T) {
			e := goneHolderPool(t, tt.remove)
			if got := statusStates(t, e); got != "slot-1=missing" {
				t.Errorf("status before refresh: slots %s, want slot-1=missing", got)
			}
			r := refresh(t, e)
			want := dropped + "\ncloned slot-1\nthe pool has 1 ready, as ready = 1 asks\n"
			if r.stdout != want {
				t.Errorf("stdout = %q, want %q; stderr %q", r.stdout, want, r.stderr)
			}
			if got := statusStates(t, e); got != "slot-1=ready" {
				t.Errorf("slots = %s, want the replacement slot-1 ready", got)
			}
		})
	}
}

// refresh drops nothing else: a dead holder's slot still on disk is take's and doctor --fix's to
// reclaim, and refresh leaves it leased.
func TestRefreshDropsNoSlotOnDisk(t *testing.T) {
	e := goneHolderPool(t, func(*testing.T, *testenv.Env) {})
	if got := statusStates(t, e); got != "slot-1=leased" {
		t.Errorf("status: slots %s, want slot-1=leased", got)
	}
	if r := refresh(t, e); r.stdout != "the pool has 1 slots, its max_slots\n" {
		t.Errorf("stdout = %q, want nothing dropped or cloned; stderr %q", r.stdout, r.stderr)
	}
	if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased" {
		t.Errorf("slots = %s, want slot-1 left leased", got)
	}
}

// doctor names the fix for the dead holder's gone slot, and doctor --fix drops it, after which
// refresh clones a replacement within max_slots; status shows the slot missing before.
func TestDoctorNamesAndFixDropsADeadHoldersGoneSlot(t *testing.T) {
	for _, tt := range goneSlotRemovals {
		t.Run(tt.name, func(t *testing.T) {
			e := goneHolderPool(t, tt.remove)
			if got := statusStates(t, e); got != "slot-1=missing" {
				t.Errorf("status before doctor: slots %s, want slot-1=missing", got)
			}
			found := slotPath(e) + tt.found
			pid := strconv.Itoa(os.Getpid())

			r := runIn(t, e, e.Repo, "doctor", "--json")
			want := found + "; its holder, process " + pid + ", is gone, so upool doctor --fix, " +
				"or the next upool take or upool refresh, drops it"
			if c := findCheck(t, doctorChecks(t, r), "slot-1"); r.code != output.ExitFailure ||
				c.Result != checkFail || c.Reason != want {
				t.Errorf("doctor: exit %d, slot-1 %s %q; want 1 and fail %q", r.code, c.Result,
					c.Reason, want)
			}
			if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=leased" {
				t.Errorf("slots after doctor = %s, want slot-1 kept", got)
			}

			r = runIn(t, e, e.Repo, "doctor", "--fix", "--json")
			want = "fixed: dropped it from the pool, since its holder, process " + pid +
				", was gone and " + found
			if c := findCheck(t, doctorChecks(t, r), "slot-1"); r.code != output.ExitOK ||
				c.Result != checkPass || c.Reason != want {
				t.Errorf("doctor --fix: exit %d, slot-1 %s %q; want 0 and %q", r.code, c.Result,
					c.Reason, want)
			}
			if got := statusStates(t, e); got != "" {
				t.Errorf("slots after doctor --fix = %s, want none", got)
			}

			if r := refresh(t, e); !strings.HasPrefix(r.stdout, "cloned slot-1\n") {
				t.Errorf("refresh: stdout %q, want slot-1 cloned", r.stdout)
			}
			if got := statusStates(t, e); got != "slot-1=ready" {
				t.Errorf("slots after refresh = %s, want the replacement slot-1 ready", got)
			}
		})
	}
}
