package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/desktop"
	"github.com/weslo/upool/internal/pool"
)

// The tests of the create hook's pooled branch (#78), on a harness pool.

// slotPath is the path of h's slot name, in the managed directory.
func (h harness) slotPath(name string) string {
	return filepath.Join(h.e.Repo, ".claude", "worktrees", name)
}

// create runs the create hook on req and fails the test unless it returned want and no error.
func (h harness) create(t *testing.T, req Request, want string) {
	t.Helper()
	got, err := WorktreeCreate(context.Background(), h.env, req)
	if err != nil || got != want {
		t.Fatalf("WorktreeCreate = %q, %v; want %q and no error", got, err, want)
	}
}

// checkHandedOut fails the test unless h's slot name holds the create hook's unconfirmed lease
// for req, with resetPending as given, and git lists the slot unlocked: no lease takes a git
// worktree lock (A27).
func (h harness) checkHandedOut(t *testing.T, name string, req Request, resetPending bool) {
	t.Helper()
	s := h.slot(t, name)
	l := s.Lease
	if s.State != pool.StateLeasedUnconfirmed || l == nil || l.Confirmed ||
		l.Holder != strings.TrimPrefix(req.SessionID, "local_") || l.NameHint != req.Name ||
		l.WorktreePath != s.Path || l.HolderPID != nil || l.ResetPending != resetPending ||
		!l.AcquiredAt.Equal(clock) {
		t.Errorf("%s = %s, lease %+v; want leased-unconfirmed, held by %s, named %s, "+
			"reset_pending %t, taken at %s", name, s.State, l, req.SessionID, req.Name,
			resetPending, clock)
	}
	wt, ok := h.worktree(t, s.Path)
	if !ok || wt.Locked {
		t.Errorf("git lists %s as %+v, %t; want it listed and unlocked", s.Path, wt, ok)
	}
}

// The acceptance test of #78: each captured desktop session create, against a pool with one ready
// slot, gets exactly the slot's path, and leaves the slot leased-unconfirmed with holder the stdin
// session_id and its name as the hint, its worktree unlocked, and nothing on stderr.
func TestPooledHandsOutAReadySlot(t *testing.T) {
	for _, file := range []string{"desktop-session-create.json", "sibling-session1-create.json"} {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t, true)
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			req := h.capture(t, file)
			h.create(t, req, h.slotPath("slot-1"))
			h.checkHandedOut(t, "slot-1", req, false)
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", h.stderr)
			}
		})
	}
}

// The holder is the bare session id, without the local_ prefix the desktop app's log puts on it.
func TestPooledHolderIsTheBareSessionID(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	id := req.SessionID
	req.SessionID = "local_" + id
	h.create(t, req, h.slotPath("slot-1"))
	if got := h.slot(t, "slot-1").Lease.Holder; got != id {
		t.Errorf("holder = %q, want %q", got, id)
	}
}

// After an archive the slot is needs-reset, and the reuse capture's session gets it with its
// reset deferred to the confirm when no slot is ready; a ready slot goes first (A5; A23).
func TestPooledHandsOutANeedsResetSlotWhenNoneIsReady(t *testing.T) {
	for _, ready := range []bool{false, true} {
		h := newHarness(t, true)
		h.readySlot(t, "slot-1", h.slotPath("slot-1"))
		err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
			p.Slots[0].State = pool.StateNeedsReset
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := "slot-1"
		if ready {
			h.readySlot(t, "slot-2", h.slotPath("slot-2"))
			want = "slot-2"
		}
		req := h.capture(t, "desktop-session-create-reuse.json")
		h.create(t, req, h.slotPath(want))
		h.checkHandedOut(t, want, req, !ready)
	}
}

// The app refuses a path that a live session holds, with no remove to follow, so the hook never
// prints one: each captured create the app refused, made while slot-1 was in use, gets slot-2.
func TestPooledNeverHandsOutASlotInUse(t *testing.T) {
	files := []string{"desktop-session-create-refused.json"}
	for i := 1; i <= 4; i++ {
		files = append(files, "sibling-refused-create-"+strconv.Itoa(i)+".json")
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t, true)
			h.leaseSlot(t, "slot-1", h.slotPath("slot-1"))
			h.readySlot(t, "slot-2", h.slotPath("slot-2"))
			req := h.capture(t, file)
			h.create(t, req, h.slotPath("slot-2"))
			h.checkHandedOut(t, "slot-2", req, false)
			if s := h.slot(t, "slot-1"); s.Lease == nil || s.Lease.LeaseID != leaseID {
				t.Errorf("slot-1 = %+v, want its own lease kept", s)
			}
		})
	}
}

// A ready slot that is gone from disk, or that git does not list, is passed over for the next.
func TestPooledPassesOverAMissingSlot(t *testing.T) {
	for name, lose := range map[string]func(t *testing.T, h harness, path string){
		"gone from disk": func(t *testing.T, _ harness, path string) {
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
		},
		"not in git worktree list": func(t *testing.T, h harness, path string) {
			h.e.Git("worktree", "remove", "--force", path)
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, true)
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			h.readySlot(t, "slot-2", h.slotPath("slot-2"))
			lose(t, h, h.slotPath("slot-1"))
			req := h.capture(t, "desktop-session-create.json")
			h.create(t, req, h.slotPath("slot-2"))
			if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
				t.Errorf("slot-1 = %+v, want it left ready, unleased", s)
			}
		})
	}
}

// The branch opens no file under the repository, which the desktop app's helper cannot read
// (docs/contracts.md (a)): with the slot's .git file and the repository's .upool.toml unreadable,
// the slot is handed out all the same.
func TestPooledOpensNoFileInTheRepository(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	h.e.WriteFile(".upool.toml", "confirm_timeout = \"1s\"\n")
	for _, file := range []string{filepath.Join(h.slotPath("slot-1"), ".git"),
		h.e.Path(".upool.toml")} {
		if err := os.Chmod(file, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(file, 0o644) }) // so the temporary directory goes
		if _, err := os.ReadFile(file); err == nil {
			t.Skipf("%s is readable with mode 0, as it is for root", file)
		}
	}
	req := h.capture(t, "desktop-session-create.json")
	h.create(t, req, h.slotPath("slot-1"))
	h.checkHandedOut(t, "slot-1", req, false)
}

// An unconfirmed lease older than confirm_timeout ends first, the worktree lock an earlier upool
// took for it released, and its slot goes to the new session, which stderr says (transition 7; #75). confirm_timeout comes
// from the user file: a project's .upool.toml is under the repository.
func TestPooledEndsAnExpiredLeaseFirst(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	old, _, err := pool.UnconfirmedTake{Holder: "a-session-that-never-started", NameHint: "x",
		Now: clock.Add(-30 * time.Second), ConfirmTimeout: time.Minute}.Take(
		context.Background(), h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	h.e.Git("worktree", "lock", "--reason", "upool lease "+old.Lease.LeaseID, h.slotPath("slot-1"))
	h.e.WriteFile(".upool.toml", "confirm_timeout = \"5m\"\n")
	user := filepath.Join(h.e.Home, ".config", "upool", "config.toml")
	if err := os.MkdirAll(filepath.Dir(user), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(user, []byte("confirm_timeout = \"10s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := h.capture(t, "desktop-session-create.json")
	h.create(t, req, h.slotPath("slot-1"))
	h.checkHandedOut(t, "slot-1", req, false)
	if want := "upool: ended the unconfirmed lease on slot-1, which no session confirmed " +
		"within 10s: slot-1 is ready again\n"; h.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", h.stderr, want)
	}
}

// With no slot free the hook tries once a second for the 20 s A11 sets and then fails, which
// aborts the session, naming upool refresh; a slot freed meanwhile is taken at the next try. The
// waits pass on testing/synctest's clock, so the test does not sleep.
func TestPooledWaitsForAFreeSlot(t *testing.T) {
	t.Run("none freed", func(t *testing.T) {
		h := newHarness(t, true)
		h.leaseSlot(t, "slot-1", h.slotPath("slot-1"))
		req := h.capture(t, "desktop-session-create.json")
		synctest.Test(t, func(t *testing.T) {
			h.env.Now = time.Now
			start := time.Now()
			got, err := WorktreeCreate(context.Background(), h.env, req)
			if waited := time.Since(start); waited != 20*time.Second {
				t.Errorf("the hook waited %s, want 20s", waited)
			}
			if got != "" || err == nil || !strings.Contains(err.Error(), "run upool refresh in "+
				h.e.Repo) {
				t.Errorf("WorktreeCreate = %q, %v; want no path and an error naming upool "+
					"refresh", got, err)
			}
		})
		if s := h.slot(t, "slot-1"); s.Lease == nil || s.Lease.LeaseID != leaseID {
			t.Errorf("slot-1 = %+v, want its own lease kept", s)
		}
	})
	t.Run("one freed", func(t *testing.T) {
		h := newHarness(t, true)
		h.leaseSlot(t, "slot-1", h.slotPath("slot-1"))
		req := h.capture(t, "desktop-session-create.json")
		synctest.Test(t, func(t *testing.T) {
			h.env.Now = time.Now
			start := time.Now()
			go func() {
				time.Sleep(2500 * time.Millisecond)
				err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
					p.Slots[0].State, p.Slots[0].Lease = pool.StateReady, nil
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			}()
			got, err := WorktreeCreate(context.Background(), h.env, req)
			if waited := time.Since(start); waited != 3*time.Second {
				t.Errorf("the hook waited %s, want 3s: the first try after the slot was freed",
					waited)
			}
			if err != nil || got != h.slotPath("slot-1") {
				t.Errorf("WorktreeCreate = %q, %v; want slot-1", got, err)
			}
		})
	})
}

// The wait is the smaller of 20 s and the desktop app's limit less 5 s (A11). The capture of a
// create hook that slept 30 s shows the app waited it out, so its limit is above the worktree time
// it logged, and the wait is 20 s.
func TestPooledWaitFitsTheAppsLimit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(capturesDir, "desktop-create-timeout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`\[CCD start-timing\] .* worktree=(\d+)ms `).FindSubmatch(b)
	if m == nil {
		t.Fatal("desktop-create-timeout.txt has no [CCD start-timing] line with worktree=")
	}
	ms, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	waited := time.Duration(ms) * time.Millisecond
	if want := min(20*time.Second, waited-5*time.Second); pooledWait != want {
		t.Errorf("pooledWait = %s, want %s: the app waited %s and still took the path",
			pooledWait, want, waited)
	}
}

// writeAppRecord writes the desktop app's record of its sessions' worktrees under h's home, naming
// each path in worktrees as the worktree its key names (A50).
func (h harness) writeAppRecord(t *testing.T, worktrees map[string]string) {
	t.Helper()
	entries := map[string]any{}
	for name, path := range worktrees {
		entries[name] = map[string]any{"name": name, "path": path, "leasedBy": "local_" + name}
	}
	b, err := json.Marshal(map[string]any{"schemaVersion": 2, "worktrees": entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.e.Home, filepath.FromSlash(desktop.RecordFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A ready slot the desktop app still records for another session is passed over, once on stderr,
// for the next free slot: the app would refuse its path (#192; A50). With no other free slot the
// hook waits and fails, as for a full pool.
func TestPooledPassesOverASlotTheAppRecords(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	h.readySlot(t, "slot-2", h.slotPath("slot-2"))
	h.writeAppRecord(t, map[string]string{"pwd-03f0cb": h.slotPath("slot-1")})
	req := h.capture(t, "desktop-session-create.json")
	h.create(t, req, h.slotPath("slot-2"))
	h.checkHandedOut(t, "slot-2", req, false)
	if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %+v, want it left ready, unleased", s)
	}
	if got := h.stderr.String(); got != "upool: passing over slot-1: the desktop app still "+
		"records it for session local_pwd-03f0cb, as its worktree pwd-03f0cb\n" {
		t.Errorf("stderr = %q, want slot-1 passed over, once", got)
	}

	synctest.Test(t, func(t *testing.T) {
		h.stderr.Reset()
		h.env.Now = time.Now
		got, err := WorktreeCreate(context.Background(), h.env, req)
		if err == nil || got != "" || !strings.Contains(err.Error(), "no slot of") {
			t.Errorf("WorktreeCreate with only slot-1 free = %q, %v; want no slot", got, err)
		}
		if n := strings.Count(h.stderr.String(), "passing over slot-1"); n != 1 {
			t.Errorf("stderr says slot-1 was passed over %d times, want once: %q", n, h.stderr)
		}
	})
}

// A record upool cannot read passes over nothing: the slot's quarantine is the fallback (A50).
func TestPooledIgnoresAnUnreadableAppRecord(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	path := filepath.Join(h.e.Home, filepath.FromSlash(desktop.RecordFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := h.capture(t, "desktop-session-create.json")
	h.create(t, req, h.slotPath("slot-1"))
	h.checkHandedOut(t, "slot-1", req, false)
}

// A quarantined slot is passed over for the next free one, and with none the hook fails as for a
// full pool (A50).
func TestPooledPassesOverAQuarantinedSlot(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	h.readySlot(t, "slot-2", h.slotPath("slot-2"))
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots[0].UnconfirmedExpiries = pool.QuarantineAfter
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req := h.capture(t, "desktop-session-create.json")
	h.create(t, req, h.slotPath("slot-2"))
	if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %+v, want it left ready, unleased", s)
	}
}

// The create hook's take counts an expired lease towards its slot's quarantine only while the app's
// record cannot be read (A50).
func TestPooledCountsExpiriesOnlyWithoutTheAppRecord(t *testing.T) {
	for _, tt := range []struct {
		name   string
		record string
		want   int
	}{{"record unreadable", "{", 1}, {"record readable", `{"worktrees": {}}`, 0}} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, true)
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			path := filepath.Join(h.e.Home, filepath.FromSlash(desktop.RecordFile))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tt.record), 0o600); err != nil {
				t.Fatal(err)
			}
			req := h.capture(t, "desktop-session-create.json")
			h.create(t, req, h.slotPath("slot-1"))
			h.env.Now = func() time.Time { return clock.Add(2 * time.Minute) }
			h.create(t, req, h.slotPath("slot-1"))
			if got := h.slot(t, "slot-1").UnconfirmedExpiries; got != tt.want {
				t.Errorf("unconfirmed_expiries = %d, want %d", got, tt.want)
			}
		})
	}
}
