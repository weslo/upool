package main

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// doctor --fix reports from the hook log after its dead-holder reclaim, so that the lease of a
// slot it has just dropped or reset is reported as reclaimed, never as still on the slot (#278).
// The slot is the one #278 is about: a desktop session's, which the app let go with no
// WorktreeRemove, and whose holder died, its directory gone or still on disk. The holder is this
// test process at a start time it did not start at, which internal/proc reads as dead: macOS only,
// as dead holders are (reclaim_darwin_test.go).
func TestDoctorFixReportsTheHookLogAfterItsReclaim(t *testing.T) {
	for _, tt := range []struct {
		name  string
		gone  bool
		fixed string
	}{
		{"its directory gone", true, "fixed: dropped it from the pool, since its holder"},
		{"its directory on disk", false, "fixed: reset it to ready, since its holder"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			if code, _, errOut := runHook(t, p.Env, "worktree-create",
				captureStdin(t, p.Env, "desktop-session-create.json", nil)); code != output.ExitOK {
				t.Fatalf("create: exit %d, stderr %q", code, errOut)
			}
			pid := os.Getpid()
			p.update(t, func(pl *pool.Pool) {
				s := &pl.Slots[0]
				s.State, s.Lease.Confirmed = pool.StateLeased, true
				s.Lease.HolderPID, s.Lease.HolderPIDStart = &pid, &clock
			})
			if tt.gone {
				p.Git("worktree", "remove", "--force", p.slot("slot-1"))
			}
			writeDesktopRecord(t, p.Home)
			at := logTime(clock)
			still := "warn slot-1: the desktop app no longer records it, and no WorktreeRemove " +
				"is logged since a desktop session was handed it at " + at + ": the app ran no " +
				"remove hook for that session, or one that never reached upool's own code, and " +
				"its lease is still on the slot"
			if got := hookLogReport(t, p); strings.Join(got, "\n") != still {
				t.Errorf("doctor: hook-log lines = %q, want %q", got, still)
			}

			r := runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
			checks := doctorChecks(t, r)
			if c := findCheck(t, checks, "slot-1"); c.Result != checkPass ||
				!strings.HasPrefix(c.Reason, tt.fixed) {
				t.Errorf("doctor --fix: slot-1 %s %q; want pass, starting %q", c.Result, c.Reason,
					tt.fixed)
			}
			reclaimed := "pass slot-1: no WorktreeRemove is logged since a desktop session was " +
				"handed it at " + at + "; its holder, process " + strconv.Itoa(pid) + ", was " +
				"gone, and doctor --fix has just reclaimed its lease"
			var got []string
			for _, c := range checks {
				if c.Name == checkHookLog {
					got = append(got, string(c.Result)+" "+c.Reason)
				}
			}
			if strings.Join(got, "\n") != reclaimed {
				t.Errorf("doctor --fix: hook-log lines = %q, want %q", got, reclaimed)
			}

			var after []string
			if !tt.gone {
				after = []string{"pass slot-1: no WorktreeRemove is logged since a desktop " +
					"session was handed it at " + at + "; that session's lease has ended since"}
			}
			if got := hookLogReport(t, p); strings.Join(got, "\n") != strings.Join(after, "\n") {
				t.Errorf("doctor after the fix: hook-log lines = %q, want %q", got, after)
			}
		})
	}
}
