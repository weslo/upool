package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// take, doctor and status with the desktop app's record of its sessions' worktrees, and with a
// quarantined slot (#192; A50). macOS only, as dead holders are (reclaim_darwin_test.go).

// writeAppRecord writes the desktop app's record under p's home, naming each path in worktrees as
// the worktree its key names, for session local_3abad311-…, or content as it is when worktrees is
// nil.
func (p harnessPool) writeAppRecord(t *testing.T, worktrees map[string]string, content string) {
	t.Helper()
	writeAppRecordIn(t, p.Home, worktrees, content)
}

// writeAppRecordIn is writeAppRecord for the home directory home.
func writeAppRecordIn(t *testing.T, home string, worktrees map[string]string, content string) {
	t.Helper()
	if worktrees != nil {
		entries := map[string]any{}
		for name, path := range worktrees {
			entries[name] = map[string]any{"name": name, "path": path, "leasedBy": appSession}
		}
		b, err := json.Marshal(map[string]any{"schemaVersion": 2, "worktrees": entries})
		if err != nil {
			t.Fatal(err)
		}
		content = string(b)
	}
	path := filepath.Join(home, filepath.FromSlash(desktop.RecordFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// appSession is the session writeAppRecord records each worktree for.
const appSession = "local_3abad311-c5b5-4182-8699-21309c37e642"

// keptWant is the line that says the reclaim leaves the dead holder's slot-1 of deadHolderPool,
// recorded by the app as pwd-c2e29a.
func keptWant() string {
	return "not reclaiming slot-1, though its holder, process " + strconv.Itoa(os.Getpid()) +
		", is gone: the desktop app still records it for session " + appSession + ", as its " +
		"worktree pwd-c2e29a, and can resume that session into it. Open that session, " +
		"unarchiving it if it is archived, and archive it with the app's Archive button to give " +
		"slot-1 back to the pool"
}

// take leaves a dead holder's slot that the app still records for a session, saying so once, and
// hands out none of it: the app resumes the session into the slot after a quit or a crash, and a
// reclaimed slot was refused to new sessions and shared with a resumed one in #192's step 1.
func TestTakeKeepsADeadHoldersSlotTheAppRecords(t *testing.T) {
	p := deadHolderPool(t)
	p.writeAppRecord(t, map[string]string{"pwd-c2e29a": p.slot("slot-1")}, "")
	r := runIn(t, p.Env, p.Repo, "take", "feature-z", "--no-create")
	if r.code != output.ExitFailure || strings.Count(r.stderr, keptWant()) != 1 ||
		strings.Contains(r.stderr, "reclaimed slot-1") {
		t.Errorf("exit %d, stderr %q; want 1, slot-1 kept once, and nothing reclaimed", r.code,
			r.stderr)
	}
	s := p.loadPool(t).Slots[0]
	if s.State != pool.StateLeased || s.Lease == nil || s.Lease.HolderPID == nil {
		t.Errorf("slot-1 = %s, lease %+v; want the dead holder's lease kept", s.State, s.Lease)
	}
	if got := p.inSlot("symbolic-ref", "--short", "HEAD"); got != "feature-x" {
		t.Errorf("slot-1 HEAD = %s, want feature-x, untouched", got)
	}
}

// doctor reports a lease the reclaim keeps, with how to free its slot, and doctor --fix keeps it
// too.
func TestDoctorReportsAKeptLeaseAndFixKeepsIt(t *testing.T) {
	p := deadHolderPool(t)
	p.writeAppRecord(t, map[string]string{"pwd-c2e29a": p.slot("slot-1")}, "")
	for _, args := range [][]string{{"doctor", "--json"}, {"doctor", "--fix", "--json"}} {
		r := runIn(t, p.Env, p.Repo, args...)
		c := findCheck(t, doctorChecks(t, r), "slot-1")
		if r.code != output.ExitOK || c.Result != checkWarn || c.Reason != keptWant() {
			t.Errorf("%s: exit %d, slot-1 %s %q; want 0 and warn %q", args, r.code, c.Result,
				c.Reason, keptWant())
		}
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want the dead holder's lease kept", s.State, s.Lease)
	}
}

// A ready slot the app records for a session is skipped by take as recorded-by-desktop-app, and
// doctor warns that no other session is handed it.
func TestTakeAndDoctorPassOverAReadySlotTheAppRecords(t *testing.T) {
	p := newHarnessPool(t)
	p.writeAppRecord(t, map[string]string{"pwd-03f0cb": p.slot("slot-1")}, "")
	r := runIn(t, p.Env, p.Repo, "take", "feature-z")
	if r.code != output.ExitOK || r.stdout != p.slot("slot-2")+"\n" {
		t.Errorf("take: exit %d, stdout %q, stderr %q; want slot-2", r.code, r.stdout, r.stderr)
	}
	r = runIn(t, p.Env, p.Repo, "take", "feature-w", "--no-create")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		"skipping slot-1: recorded-by-desktop-app") {
		t.Errorf("take: exit %d, stderr %q; want 1 and slot-1 skipped as recorded", r.code,
			r.stderr)
	}
	r = runIn(t, p.Env, p.Repo, "doctor", "--json")
	c := findCheck(t, doctorChecks(t, r), "slot-1")
	want := "ready, but the desktop app still records it for session " + appSession + ", as its " +
		"worktree pwd-03f0cb, and can resume that session into it, so no other session is handed " +
		"it. Open that session, unarchiving it if it is archived, and archive it with the app's " +
		"Archive button"
	if c.Result != checkWarn || c.Reason != want {
		t.Errorf("doctor: slot-1 %s %q; want warn %q", c.Result, c.Reason, want)
	}
}

// A quarantined slot is skipped by take, reported by status and doctor with how to clear it, and
// doctor --fix clears it, so that take hands it out again (A50).
func TestAQuarantinedSlotIsReportedAndCleared(t *testing.T) {
	p := newHarnessPool(t)
	p.update(t, func(pl *pool.Pool) { pl.Slots[0].UnconfirmedExpiries = pool.QuarantineAfter })
	reason := pool.QuarantineReason("slot-1", pool.QuarantineAfter)
	if !strings.HasPrefix(reason, "2 sessions in a row were handed slot-1 and none confirmed it, "+
		"while the desktop app's record could not be read") {
		t.Fatalf("QuarantineReason = %q", reason)
	}
	r := runIn(t, p.Env, p.Repo, "status")
	if r.code != output.ExitOK || !strings.Contains(r.stdout, "slot-1 is quarantined: "+reason) {
		t.Errorf("status: exit %d, stdout %q; want slot-1's quarantine", r.code, r.stdout)
	}
	r = runIn(t, p.Env, p.Repo, "status", "--json")
	var env struct {
		Data statusReport `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || len(env.Data.Pools) != 1 ||
		!env.Data.Pools[0].Slots[0].Quarantined || env.Data.Pools[0].Slots[1].Quarantined {
		t.Errorf("status --json = %s, %v; want slot-1 alone quarantined", r.stdout, err)
	}
	r = runIn(t, p.Env, p.Repo, "doctor", "--json")
	if c := findCheck(t, doctorChecks(t, r), "slot-1"); c.Result != checkWarn ||
		c.Reason != "quarantined: "+reason {
		t.Errorf("doctor: slot-1 %s %q; want warn quarantined", c.Result, c.Reason)
	}
	r = runIn(t, p.Env, p.Repo, "take", "feature-z")
	if r.stdout != p.slot("slot-2")+"\n" {
		t.Errorf("take: stdout %q, stderr %q; want slot-2, slot-1 skipped", r.stdout, r.stderr)
	}
	r = runIn(t, p.Env, p.Repo, "doctor", "--fix", "--json")
	if c := findCheck(t, doctorChecks(t, r), "slot-1"); c.Result != checkPass ||
		c.Reason != "fixed: cleared its quarantine, after 2 unconfirmed leases in a row, so it "+
			"is handed out again" {
		t.Errorf("doctor --fix: slot-1 %s %q; want it cleared", c.Result, c.Reason)
	}
	if got := p.loadPool(t).Slots[0].UnconfirmedExpiries; got != 0 {
		t.Errorf("slot-1's unconfirmed_expiries = %d, want 0", got)
	}
	if r := runIn(t, p.Env, p.Repo, "take", "feature-w"); r.stdout != p.slot("slot-1")+"\n" {
		t.Errorf("take: stdout %q, stderr %q; want slot-1 again", r.stdout, r.stderr)
	}
}

// doctor warns of an app record it cannot read, and says nothing of one that is missing, as on a
// Mac without the app.
func TestDoctorWarnsOfAnAppRecordItCannotRead(t *testing.T) {
	p := newHarnessPool(t)
	r := runIn(t, p.Env, p.Repo, "doctor", "--json")
	for _, c := range doctorChecks(t, r) {
		if c.Name == checkDesktopRecord {
			t.Errorf("doctor with no record: %+v, want no %s check", c, checkDesktopRecord)
		}
	}
	p.writeAppRecord(t, nil, "{")
	r = runIn(t, p.Env, p.Repo, "doctor", "--json")
	c := findCheck(t, doctorChecks(t, r), checkDesktopRecord)
	if c.Result != checkWarn || !strings.Contains(c.Reason, "unexpected end of JSON input; a "+
		"slot the app still records for a session may be handed out regardless, until it is "+
		"quarantined") {
		t.Errorf("doctor: %s %s %q; want a warning", checkDesktopRecord, c.Result, c.Reason)
	}
}

// The top-up counts no ready slot that no session is handed, recorded by the app or quarantined, so
// it clones around it, as a refused session's error says to run refresh for (A50).
func TestRefreshClonesAroundASlotNoSessionIsHanded(t *testing.T) {
	for name, keep := range map[string]func(t *testing.T, e *testenv.Env){
		"recorded by the app": func(t *testing.T, e *testenv.Env) {
			writeAppRecordIn(t, e.Home, map[string]string{"pwd-03f0cb": slotPath(e)}, "")
		},
		"quarantined": func(t *testing.T, e *testenv.Env) {
			setSlot(t, e, func(s *pool.Slot) { s.UnconfirmedExpiries = pool.QuarantineAfter })
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newInitEnv(t)
			mustInit(t, e)
			keep(t, e)
			r := refresh(t, e)
			if want := "cloned slot-2\nthe pool has 1 ready, as ready = 1 asks\n"; r.stdout != want {
				t.Errorf("stdout = %q, want %q; stderr %q", r.stdout, want, r.stderr)
			}
			if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=ready slot-2=ready" {
				t.Errorf("slots = %s, want slot-2 cloned beside slot-1", got)
			}
		})
	}
}

// status, doctor and take end an unconfirmed lease past confirm_timeout, and count the expiry
// towards the slot's quarantine only while the app's record cannot be read (A50).
func TestExpiriesCountOnlyWithoutTheAppRecord(t *testing.T) {
	for _, cmd := range [][]string{{"status"}, {"doctor"}, {"take", "feature-z", "--no-create"}} {
		for _, record := range []string{"", `{"worktrees": {}}`} {
			t.Run(strings.Join(cmd, " ")+" with record "+strconv.Quote(record), func(t *testing.T) {
				p := newHarnessPool(t)
				if record != "" {
					p.writeAppRecord(t, nil, record)
				}
				p.update(t, func(pl *pool.Pool) {
					s := &pl.Slots[0]
					s.State = pool.StateLeasedUnconfirmed
					s.Lease = &pool.Lease{LeaseID: "hook", Holder: appSession,
						WorktreePath: s.Path, AcquiredAt: clock.Add(-time.Hour), Lane: "script"}
				})
				runIn(t, p.Env, p.Repo, cmd...)
				want := 1
				if record != "" {
					want = 0
				}
				if got := p.loadPool(t).Slots[0].UnconfirmedExpiries; got != want {
					t.Errorf("unconfirmed_expiries = %d, want %d", got, want)
				}
			})
		}
	}
}
