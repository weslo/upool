//go:build darwin

package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// The tests of the SessionStart hook's startup confirm. They run on macOS only, since a lease's
// holder is recorded with its start time, which internal/proc reads on macOS alone.

// The acceptance test of #77: each captured startup in a slot, its cwd filled to a harness slot
// holding the create hook's unconfirmed lease, confirms the lease with the hook's parent, the
// session's CLI, as its holder (A6), copies no file from the main checkout (A44), and leaves
// CLAUDE_ENV_FILE holding exactly one export line, naming the slot. Each also gets upool's
// instructions as context (start; A43), the desktop app's /clear among them, which starts a new
// session with source startup (question 43).
func TestSessionStartConfirmsTheSlot(t *testing.T) {
	for _, file := range []string{"session-start.json", "session-start-reuse.json",
		"sibling-session1-start.json", "sibling-session2-start.json",
		"sibling-session3-start.json", "sibling-b4-start.json",
		"desktop-clear-session-start.json"} {
		t.Run(file, func(t *testing.T) {
			h := startHarness(t, true, "session")
			req := h.capture(t, file)
			name := filepath.Base(req.Cwd)
			h.readySlot(t, name, req.Cwd)
			s := h.hookLease(t)
			h.e.WriteFile(".claude/settings.local.json", `{"changed": true}`+"\n")
			if err := h.start(t, req); err != nil {
				t.Fatalf("SessionStart: %v, want no error", err)
			}
			got := h.slot(t, name)
			l := got.Lease
			if got.State != pool.StateLeased || l == nil || !l.Confirmed ||
				l.LeaseID != s.Lease.LeaseID {
				t.Fatalf("%s = %+v, want leased under its lease, confirmed", name, got)
			}
			checkHolder(t, l, os.Getpid())
			if list := h.e.Git("worktree", "list", "--porcelain"); strings.Contains(list,
				"\nlocked") {
				t.Errorf("git worktree list = %q, want no worktree locked (A27)", list)
			}
			if _, err := os.Lstat(filepath.Join(req.Cwd, ".claude",
				"settings.local.json")); err == nil {
				t.Errorf("the slot has the main checkout's settings.local.json, want nothing " +
					"copied (A44)")
			}
			h.checkExported(t, req.Cwd)
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", h.stderr)
			}
		})
	}
}

// A startup in a slot handed out from needs-reset runs its deferred reset as it confirms, and
// says nothing on stdout (A23).
func TestSessionStartRunsTheDeferredReset(t *testing.T) {
	h := startHarness(t, true, "session")
	req := h.capture(t, "session-start.json")
	h.readySlot(t, "slot-1", req.Cwd)
	err := pool.Update(t.Context(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots[0].State = pool.StateNeedsReset
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := h.hookLease(t); !s.Lease.ResetPending {
		t.Fatalf("lease = %+v, want reset_pending", s.Lease)
	}
	if err := h.start(t, req); err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	if l := h.slot(t, "slot-1").Lease; l == nil || !l.Confirmed || l.ResetPending {
		t.Errorf("lease = %+v, want it confirmed with its reset done", l)
	}
	h.checkExported(t, req.Cwd)
}

// The acceptance test of #79, the hook's half: a startup whose confirm succeeds starts one
// top-up, of the slot's pool in its main checkout, through the injected starter; one that fails
// to start is reported, and the slot is exported all the same.
func TestSessionStartSpawnsRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h := startHarness(t, true, "session")
		var s starts
		if fail {
			s.err = errors.New("fork failed")
		}
		h.env.StartRefresh = s.start
		req := h.capture(t, "session-start.json")
		h.readySlot(t, "slot-1", req.Cwd)
		h.hookLease(t)
		err := h.start(t, req)
		if want := []string{h.e.Repo + " " + h.key}; !slices.Equal(s.calls, want) {
			t.Errorf("top-ups started = %q, want %q", s.calls, want)
		}
		if fail != (err != nil) || (fail && !strings.Contains(err.Error(),
			"starting the top-up: fork failed")) {
			t.Errorf("fail %t: SessionStart: %v", fail, err)
		}
		h.checkExported(t, req.Cwd)
	}
}

// setLease gives the slot name in h's pool lease, as some other take left it, and the count of
// unconfirmed expiries.
func (h harness) setLease(t *testing.T, name string, state pool.State, lease *pool.Lease,
	expiries int) {
	t.Helper()
	err := pool.Update(t.Context(), h.e.Home, h.key, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == name {
				p.Slots[i].State, p.Slots[i].Lease = state, lease
				p.Slots[i].UnconfirmedExpiries = expiries
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A start found by cwd alone, in a slot whose lease is another holder's, a Terminal take's with no
// session_id, neither confirms that lease nor records itself as its holder, and says so, though it
// still gets the instructions and the export: a session resumed into a slot a dead-PID reclaim had
// handed to the Terminal take took that lease up in #192's step 1 (A50).
func TestSessionStartTakesUpNoOtherHoldersLease(t *testing.T) {
	for _, file := range []string{"session-start.json", "session-start-resume.json",
		"session-start-compact.json"} {
		t.Run(file, func(t *testing.T) {
			h := startHarness(t, true, "session")
			path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
			h.readySlot(t, "slot-1", path)
			other := os.Getppid()
			lease := &pool.Lease{LeaseID: "terminal-take", WorktreePath: path,
				HolderPID: &other, HolderPIDStart: &clock, AcquiredAt: clock, Confirmed: true,
				Lane: pool.LaneScript}
			h.setLease(t, "slot-1", pool.StateLeased, lease, 0)
			req := h.capture(t, file)
			req.Cwd = path
			err := h.start(t, req)
			if err == nil || !strings.Contains(err.Error(), "slot-1's lease terminal-take is "+
				"another holder's, not this session's") {
				t.Errorf("SessionStart = %v, want it to leave the other holder's lease", err)
			}
			got := h.slot(t, "slot-1").Lease
			if got == nil || got.LeaseID != "terminal-take" || got.SessionID != nil ||
				got.HolderPID == nil || *got.HolderPID != other {
				t.Errorf("lease = %+v, want the Terminal take's, untouched", got)
			}
			h.checkExported(t, path)
		})
	}
}

// A desktop /clear starts a new session with source startup in the same CLI process, found by
// cwd, since its new session_id names no lease: a lease its own process holds is still its own,
// and the startup records the new session_id on it (A46; A50).
func TestSessionStartAfterAClearKeepsItsOwnLease(t *testing.T) {
	h := startHarness(t, true, "session")
	req := h.capture(t, "desktop-clear-session-start.json")
	name := filepath.Base(req.Cwd)
	h.readySlot(t, name, req.Cwd)
	self, before := os.Getpid(), "0c7e6e2c-66e6-464a-a850-ecd38269346d"
	h.setLease(t, name, pool.StateLeased, &pool.Lease{LeaseID: "own", WorktreePath: req.Cwd,
		HolderPID: &self, HolderPIDStart: &clock, AcquiredAt: clock, Confirmed: true,
		Lane: pool.LaneScript, SessionID: &before}, 0)
	if err := h.start(t, req); err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	got := h.slot(t, name).Lease
	if got == nil || got.LeaseID != "own" || got.SessionID == nil ||
		*got.SessionID != req.SessionID {
		t.Errorf("lease = %+v, want its own, with the new session_id %s", got, req.SessionID)
	}
	checkHolder(t, got, self)
}

// A confirm shows the slot's path is not refused, so it ends the run of unconfirmed leases that
// would quarantine the slot (A50).
func TestSessionStartConfirmEndsTheUnconfirmedRun(t *testing.T) {
	h := startHarness(t, true, "session")
	req := h.capture(t, "session-start.json")
	name := filepath.Base(req.Cwd)
	h.readySlot(t, name, req.Cwd)
	h.setLease(t, name, pool.StateReady, nil, pool.QuarantineAfter-1)
	h.hookLease(t)
	if err := h.start(t, req); err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	if got := h.slot(t, name); got.State != pool.StateLeased || got.UnconfirmedExpiries != 0 {
		t.Errorf("%s = %s with %d unconfirmed expiries, want leased with 0", name, got.State,
			got.UnconfirmedExpiries)
	}
}

// A desktop /clear's startup comes with a new session_id, found by cwd. If it starts a new CLI
// process, the lease it finds is still its session's, with the old session_id and a holder that has
// ended: the startup takes it up. A session's lease whose holder is another live process stays
// that process's (A50).
func TestSessionStartAfterAClearInANewProcess(t *testing.T) {
	for _, tt := range []struct {
		name    string
		holder  func(t *testing.T) (int, time.Time)
		takenUp bool
	}{
		{"the old process ended", func(*testing.T) (int, time.Time) {
			// A process other than the hook's parent, at a start time it did not start at,
			// reads as ended.
			return os.Getppid(), clock
		}, true},
		{"another process alive", func(t *testing.T) (int, time.Time) {
			start, err := (proc.System{}).Start(os.Getppid())
			if err != nil {
				t.Fatal(err)
			}
			return os.Getppid(), start
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := startHarness(t, true, "session")
			req := h.capture(t, "desktop-clear-session-start.json")
			name := filepath.Base(req.Cwd)
			h.readySlot(t, name, req.Cwd)
			pid, start := tt.holder(t)
			before := "0c7e6e2c-66e6-464a-a850-ecd38269346d"
			h.setLease(t, name, pool.StateLeased, &pool.Lease{LeaseID: "session's",
				WorktreePath: req.Cwd, HolderPID: &pid, HolderPIDStart: &start, AcquiredAt: clock,
				Confirmed: true, Lane: pool.LaneScript, SessionID: &before}, 0)
			err := h.start(t, req)
			got := h.slot(t, name).Lease
			if tt.takenUp {
				if err != nil || got == nil || got.SessionID == nil ||
					*got.SessionID != req.SessionID {
					t.Errorf("SessionStart = %v, lease %+v; want it taken up with session_id %s",
						err, got, req.SessionID)
				}
				checkHolder(t, got, os.Getpid())
				return
			}
			if err == nil || got == nil || *got.SessionID != before || *got.HolderPID != pid {
				t.Errorf("SessionStart = %v, lease %+v; want it left to process %d", err, got, pid)
			}
		})
	}
}
