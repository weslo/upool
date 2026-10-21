//go:build darwin

package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
)

// The tests of the SessionStart hook's startup confirm. They run on macOS only, since the agent
// files are copied with cp -cR, macOS's clonefile(2) (internal/clone).

// The acceptance test of #77: each captured startup in a slot, its cwd filled to a harness slot
// holding the create hook's unconfirmed lease, confirms the lease with the hook's parent, the
// session's CLI, as its holder (A6), re-copies the agent files from the main checkout, and leaves
// CLAUDE_ENV_FILE holding exactly one export line, naming the slot.
func TestSessionStartConfirmsTheSlot(t *testing.T) {
	for _, file := range []string{"session-start.json", "session-start-reuse.json",
		"sibling-session1-start.json", "sibling-session2-start.json",
		"sibling-session3-start.json", "sibling-b4-start.json"} {
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
			copied := filepath.Join(req.Cwd, ".claude", "settings.local.json")
			if b, err := os.ReadFile(copied); err != nil || string(b) != `{"changed": true}`+"\n" {
				t.Errorf("%s = %q, %v; want the main checkout's copy", copied, b, err)
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
