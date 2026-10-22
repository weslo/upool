package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the check of warming slots whose owner died ((f) transition 22; A4; #97), with
// liveness injected through fakeProcs (reclaim_test.go). The slot is a real git worktree at base,
// detached, as refresh and a clone's seed hold it.

// The owner of the warming slot in the tests, and when it started.
const ownerPID = 4300

var ownerStart = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

// warmingEnv is a testenv repository whose pool has one slot, slot-1, warming under ownerPID.
type warmingEnv struct {
	*testenv.Env
	key, slot string
	r         Recoverer
}

func newWarmingEnv(t *testing.T, procs fakeProcs) warmingEnv {
	t.Helper()
	e := testenv.New(t)
	slot := e.Path(".claude/worktrees/slot-1")
	e.Git("worktree", "add", "-q", "--detach", slot, testenv.Base)
	const key = "project-00000000"
	owner, start := ownerPID, ownerStart
	err := Update(t.Context(), e.Home, key, func(p *Pool) error {
		p.Repo, p.Base = e.Repo, testenv.Base
		p.Slots = []Slot{{Name: "slot-1", Path: slot, State: StateWarming, SeedHead: e.Head,
			OwnerPID: &owner, OwnerPIDStart: &start}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return warmingEnv{Env: e, key: key, slot: slot, r: Recoverer{
		Git: git.Client{Runner: git.Exec{}, Env: e.Environ()}, Proc: procs, Home: e.Home, Key: key,
	}}
}

// slot1 returns slot-1 as state.json has it.
func (e warmingEnv) slot1(t *testing.T) Slot {
	t.Helper()
	p, err := Load(e.Home, e.key)
	if err != nil {
		t.Fatal(err)
	}
	return p.Slots[0]
}

// recover runs Recover and fails the test on its own error.
func (e warmingEnv) recover(t *testing.T) []Recovered {
	t.Helper()
	done, err := e.r.Recover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return done
}

// A dead owner's clean slot goes back to ready, its owner cleared; a dirty one is parked, its
// files kept. An owner whose PID is now another process's, started at another time, is dead.
func TestRecoverReturnsADeadOwnersSlot(t *testing.T) {
	for _, tt := range []struct {
		name  string
		procs fakeProcs
		dirty bool
		want  State
	}{
		{"owner gone, clean", fakeProcs{}, false, StateReady},
		{"owner's PID reused, clean", fakeProcs{ownerPID: ownerStart.Add(time.Hour)}, false,
			StateReady},
		{"owner gone, dirty", fakeProcs{}, true, StateParked},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newWarmingEnv(t, tt.procs)
			kept := filepath.Join(e.slot, "left-behind.txt")
			if tt.dirty {
				if err := os.WriteFile(kept, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			done := e.recover(t)
			if len(done) != 1 || done[0].Slot != "slot-1" || done[0].OwnerPID != ownerPID ||
				done[0].State != tt.want || done[0].Err != nil {
				t.Fatalf("Recover = %+v, want slot-1 %s", done, tt.want)
			}
			if tt.dirty && (len(done[0].Dirty) != 1 || done[0].Dirty[0] != "?? left-behind.txt") {
				t.Errorf("dirty = %q, want the untracked file", done[0].Dirty)
			}
			s := e.slot1(t)
			if s.State != tt.want || s.OwnerPID != nil || s.OwnerPIDStart != nil {
				t.Errorf("slot-1 = %s, owner %v; want %s and no owner", s.State, s.OwnerPID,
					tt.want)
			}
			if _, err := os.Stat(kept); tt.dirty && err != nil {
				t.Errorf("%s is gone: %v", kept, err)
			}
		})
	}
}

// A warming slot is left as it is while its owner is alive at its start time, or while the ledger
// shows a live Editor in it, as a refresh's re-warm Editor that outlived the refresh.
func TestRecoverLeavesAWarmingSlotInUse(t *testing.T) {
	const editorPID = 4400
	for _, tt := range []struct {
		name   string
		procs  fakeProcs
		editor bool
	}{
		{"owner alive", fakeProcs{ownerPID: ownerStart}, false},
		{"owner gone, Editor live", fakeProcs{editorPID: ownerStart}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newWarmingEnv(t, tt.procs)
			if tt.editor {
				err := ledger.Update(t.Context(), e.Home, func(l *ledger.Ledger) error {
					l.Editors = []ledger.Editor{{Project: e.slot, PID: editorPID,
						Mode: "headless", OpenedByUpool: true, ChargeGB: 2.5}}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if done := e.recover(t); len(done) != 0 {
				t.Errorf("Recover = %+v, want nothing checked again", done)
			}
			if s := e.slot1(t); s.State != StateWarming || s.OwnerPID == nil ||
				*s.OwnerPID != ownerPID {
				t.Errorf("slot-1 = %s, owner %v; want still warming under %d", s.State,
					s.OwnerPID, ownerPID)
			}
		})
	}
}

// A slot that left the disk is reported in its Recovered's Err and left warming.
func TestRecoverReportsASlotItCannotCheck(t *testing.T) {
	e := newWarmingEnv(t, fakeProcs{})
	if err := os.RemoveAll(e.slot); err != nil {
		t.Fatal(err)
	}
	done := e.recover(t)
	if len(done) != 1 || done[0].Err == nil || done[0].State != "" {
		t.Fatalf("Recover = %+v, want slot-1 with an error", done)
	}
	if s := e.slot1(t); s.State != StateWarming {
		t.Errorf("slot-1 = %s, want still warming", s.State)
	}
}
