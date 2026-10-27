package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the dead-holder reclaim, with liveness injected: fakeProcs answers as internal/proc
// does, a process alive only at its own start time, so a reused PID reads as dead. The slots are
// real git worktrees in a testenv repository.

// fakeProcs is a proc.Checker over the processes it lists: PID to start time.
type fakeProcs map[int]time.Time

func (f fakeProcs) Start(pid int) (time.Time, error) {
	start, ok := f[pid]
	if !ok {
		return time.Time{}, proc.ErrNoProcess
	}
	return start, nil
}

// Name names every process it lists a Unity Editor.
func (f fakeProcs) Name(pid int) (string, error) {
	if _, ok := f[pid]; !ok {
		return "", proc.ErrNoProcess
	}
	return "Unity", nil
}

// EditorsAt finds no Editor by its arguments or its lockfile: the tests here have none.
func (fakeProcs) EditorsAt(string) ([]int, error) { return nil, nil }

func (f fakeProcs) Alive(pid int, start time.Time) (bool, error) {
	got, ok := f[pid]
	return ok && got.Equal(start), nil
}

// The holder and an Editor in the tests, and when each process started.
const (
	holderPID = 4100
	editorPID = 4200
)

var (
	holderStart = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	reclaimNow  = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
)

// reclaimEnv is a testenv repository whose pool has one slot, slot-1, taken on the branch feature
// and locked, as `upool take feature` leaves it, by a lease whose holder is lease's.
type reclaimEnv struct {
	*testenv.Env
	key, slot string
	r         Reclaimer
}

// newReclaimEnv makes a reclaimEnv whose slot-1 lease has holder, nil for a bare take's.
func newReclaimEnv(t *testing.T, holder *int, procs fakeProcs) reclaimEnv {
	t.Helper()
	e := testenv.New(t)
	slot := e.Path(".claude/worktrees/slot-1")
	e.Git("worktree", "add", "-q", "--detach", slot, testenv.Base)
	e.Git("-C", slot, "checkout", "-q", "-b", "feature")
	e.Git("worktree", "lock", "--reason", "upool lease id-1", slot)
	start := holderStart
	lease := &Lease{LeaseID: "id-1", Holder: "agent", WorktreePath: slot, HolderPID: holder,
		AcquiredAt: holderStart, Confirmed: true, Lane: "script"}
	if holder != nil {
		lease.HolderPIDStart = &start
	}
	const key = "project-00000000"
	err := Update(t.Context(), e.Home, key, func(p *Pool) error {
		p.Repo, p.Base = e.Repo, testenv.Base
		p.Slots = []Slot{{Name: "slot-1", Path: slot, State: StateLeased, SeedHead: e.Head,
			Lease: lease}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return reclaimEnv{Env: e, key: key, slot: slot, r: Reclaimer{
		Git: git.Client{Runner: git.Exec{}, Env: e.Environ()}, Proc: procs, Home: e.Home, Key: key,
		Now: func() time.Time { return reclaimNow },
	}}
}

// slot1 returns slot-1 as state.json has it.
func (e reclaimEnv) slot1(t *testing.T) Slot {
	t.Helper()
	p, err := Load(e.Home, e.key)
	if err != nil {
		t.Fatal(err)
	}
	return p.Slots[0]
}

// checkUntouched fails the test unless slot-1 is still leased, locked and on feature.
func (e reclaimEnv) checkUntouched(t *testing.T, done []Reclaimed) {
	t.Helper()
	if len(done) != 0 {
		t.Errorf("Reclaim = %+v, want nothing reclaimed", done)
	}
	if s := e.slot1(t); s.State != StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want it still leased", s.State, s.Lease)
	}
	if !strings.Contains(e.Git("worktree", "list", "--porcelain"), "locked upool lease id-1") {
		t.Error("slot-1 is unlocked, want it still locked")
	}
	if got := e.Git("-C", e.slot, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Errorf("slot-1 HEAD = %s, want feature, untouched", got)
	}
}

// A dead holder's clean slot gets the release reset (t13): ready, at base detached, unlocked, the
// branch kept, last_used set.
func TestReclaimResetsCleanSlot(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{})
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Slot != "slot-1" || done[0].HolderPID != holderPID ||
		done[0].State != StateReady || done[0].Err != nil || done[0].Dirty != nil {
		t.Fatalf("Reclaim = %+v, want slot-1 reclaimed ready", done)
	}
	s := e.slot1(t)
	if s.State != StateReady || s.Lease != nil || !s.LastUsed.Equal(reclaimNow) {
		t.Errorf("slot-1 = %s, lease %+v, last_used %s; want ready, none, %s", s.State, s.Lease,
			s.LastUsed, reclaimNow)
	}
	if strings.Contains(e.Git("worktree", "list", "--porcelain"), "locked") {
		t.Error("slot-1 is still locked")
	}
	if got := e.Git("-C", e.slot, "rev-parse", "HEAD"); got != e.BaseCommit {
		t.Errorf("slot-1 HEAD = %s, want base, %s", got, e.BaseCommit)
	}
	if out := e.Git("-C", e.slot, "status", "--porcelain", "--branch"); out != "## HEAD (no branch)" {
		t.Errorf("slot-1 status = %q, want detached and clean", out)
	}
	if got := e.Git("branch", "--list", "feature"); got == "" {
		t.Error("the branch feature is gone, want it kept")
	}
}

// A dead holder's dirty slot is parked (t14), unlocked and otherwise as it was.
func TestReclaimParksDirtyTree(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{})
	e.WriteFile(".claude/worktrees/slot-1/spike-reuse.txt", "left behind\n")
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].State != StateParked ||
		strings.Join(done[0].Dirty, "|") != "?? spike-reuse.txt" {
		t.Fatalf("Reclaim = %+v, want slot-1 parked for ?? spike-reuse.txt", done)
	}
	if s := e.slot1(t); s.State != StateParked || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want parked with no lease", s.State, s.Lease)
	}
	if strings.Contains(e.Git("worktree", "list", "--porcelain"), "locked") {
		t.Error("slot-1 is still locked")
	}
	if _, err := os.Stat(filepath.Join(e.slot, "spike-reuse.txt")); err != nil {
		t.Errorf("spike-reuse.txt: %v, want it kept", err)
	}
	if got := e.Git("-C", e.slot, "symbolic-ref", "--short", "HEAD"); got != "feature" {
		t.Errorf("slot-1 HEAD = %s, want feature, untouched", got)
	}
}

// A bare take's lease has no holder PID and is never reclaimed (A6), however old.
func TestReclaimSkipsLeaseWithoutHolderPID(t *testing.T) {
	e := newReclaimEnv(t, nil, fakeProcs{})
	if dead, err := e.r.Candidates(e.mustLoad(t)); err != nil || len(dead) != 0 {
		t.Errorf("Candidates = %+v, %v; want none", dead, err)
	}
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e.checkUntouched(t, done)
}

// mustLoad returns the pool's state.json.
func (e reclaimEnv) mustLoad(t *testing.T) Pool {
	t.Helper()
	p, err := Load(e.Home, e.key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A live holder keeps its lease; the same PID with another start time is another process, so the
// holder is dead and the lease is reclaimed.
func TestReclaimTreatsReusedPIDAsDead(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{holderPID: holderStart})
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e.checkUntouched(t, done)

	e = newReclaimEnv(t, &pid, fakeProcs{holderPID: holderStart.Add(time.Hour)})
	done, err = e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].State != StateReady {
		t.Errorf("Reclaim with the PID reused = %+v, want slot-1 reclaimed", done)
	}
}

// An Editor the ledger records in the slot, while its PID is a process, blocks the reclaim; once
// that process is gone the lease is reclaimed.
func TestReclaimWaitsForLiveEditor(t *testing.T) {
	pid := holderPID
	for _, tt := range []struct {
		name     string
		procs    fakeProcs
		reclaims bool
	}{
		{"the Editor alive", fakeProcs{editorPID: holderStart}, false},
		{"the Editor gone", fakeProcs{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newReclaimEnv(t, &pid, tt.procs)
			err := ledger.Update(t.Context(), e.Home, func(l *ledger.Ledger) error {
				l.Editors = []ledger.Editor{{Project: e.slot, PID: editorPID, Mode: "gui",
					OpenedByUpool: true, ChargeGB: 2.5}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			done, err := e.r.Reclaim(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !tt.reclaims {
				e.checkUntouched(t, done)
				return
			}
			if len(done) != 1 || done[0].State != StateReady {
				t.Errorf("Reclaim = %+v, want slot-1 reclaimed once its Editor is gone", done)
			}
		})
	}
}

// A slot that cannot be reclaimed is reported and left; the rest of the pool is unaffected.
func TestReclaimReportsASlotItCannotReclaim(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{})
	e.Git("worktree", "unlock", e.slot)
	if err := os.RemoveAll(e.slot); err != nil {
		t.Fatal(err)
	}
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Err == nil ||
		!strings.Contains(done[0].Err.Error(), "is not on disk") {
		t.Errorf("Reclaim = %+v, want slot-1 reported as not on disk", done)
	}
	if s := e.slot1(t); s.State != StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want it left leased", s.State, s.Lease)
	}
}

// Define-order noise is reverted before the slot is judged, as release does, so a reorder alone
// leaves it clean.
func TestReclaimRevertsDefineReorder(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{})
	settings := ".claude/worktrees/slot-1/" + SettingsAsset
	b, err := os.ReadFile(e.Path(settings))
	if err != nil {
		t.Fatal(err)
	}
	e.WriteFile(settings, strings.Replace(string(b), capturedBefore, capturedAfter, 1))
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].State != StateReady {
		t.Errorf("Reclaim = %+v, want slot-1 reclaimed ready with the reorder reverted", done)
	}
}

// A dead holder's lease on a slot the desktop app still records is kept, not reclaimed: the app
// resumes the session into the slot and refuses its path to any other (A50; #192). Kept names it,
// and Candidates does not.
func TestReclaimKeepsASlotTheAppStillRecords(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{})
	e.r.Held = func(s Slot) (string, bool) {
		if s.Path != e.slot {
			t.Errorf("Held asked of %s, want slot-1's path", s.Path)
		}
		return "pwd-c2e29a", true
	}
	p := e.mustLoad(t)
	if c, err := e.r.Candidates(p); err != nil || len(c) != 0 {
		t.Errorf("Candidates = %+v, %v; want none", c, err)
	}
	kept, err := e.r.Kept(p)
	if err != nil || len(kept) != 1 || kept[0] != (Kept{Slot: "slot-1", HolderPID: holderPID,
		App: "pwd-c2e29a"}) {
		t.Errorf("Kept = %+v, %v; want slot-1 under pwd-c2e29a", kept, err)
	}
	done, err := e.r.Reclaim(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e.checkUntouched(t, done)

	// Once the app no longer records it, the slot is reclaimed as before.
	e.r.Held = func(Slot) (string, bool) { return "", false }
	if kept, err := e.r.Kept(e.mustLoad(t)); err != nil || len(kept) != 0 {
		t.Errorf("Kept = %+v, %v; want none", kept, err)
	}
	if done, err := e.r.Reclaim(t.Context()); err != nil || len(done) != 1 ||
		done[0].State != StateReady {
		t.Errorf("Reclaim = %+v, %v; want slot-1 reclaimed ready", done, err)
	}
}

// A live holder's lease is neither a candidate nor kept, whatever the app records.
func TestReclaimKeepsNothingOfALiveHolder(t *testing.T) {
	pid := holderPID
	e := newReclaimEnv(t, &pid, fakeProcs{holderPID: holderStart})
	e.r.Held = func(Slot) (string, bool) { return "pwd-c2e29a", true }
	if kept, err := e.r.Kept(e.mustLoad(t)); err != nil || len(kept) != 0 {
		t.Errorf("Kept = %+v, %v; want none", kept, err)
	}
}
