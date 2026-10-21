package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the WorktreeRemove hook, on a harness pool whose slot a session has leased.

// leaseID is the lease the tests' sessions hold.
const leaseID = "0123456789abcdef0123456789abcdef"

// before is when the tests' slots were last used, before the hook runs at clock.
var before = clock.Add(-time.Hour)

// leaseSlot adds a slot named name at path to h's pool, a worktree detached at base, leased and
// confirmed as a desktop session's is once SessionStart has run.
func (h harness) leaseSlot(t *testing.T, name, path string) {
	t.Helper()
	h.e.Git("worktree", "add", "-q", "--detach", path, testenv.Base)
	pid := 4321
	start := before
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots = append(p.Slots, pool.Slot{Name: name, Path: path, State: pool.StateLeased,
			SeedHead: strings.TrimSpace(h.e.Git("rev-parse", "HEAD")), LastUsed: before,
			Lease: &pool.Lease{LeaseID: leaseID, Holder: "49b55ea5-fc66-40a8-8f6c-b4afc31ec456",
				WorktreePath: path, HolderPID: &pid, HolderPIDStart: &start, AcquiredAt: before,
				Confirmed: true, Lane: "script"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// slot returns h's slot name from state.json.
func (h harness) slot(t *testing.T, name string) pool.Slot {
	t.Helper()
	p, err := pool.Load(h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range p.Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("state.json has no %s", name)
	return pool.Slot{}
}

// checkEnded fails the test unless h's slot name is in state, with its lease cleared, last_used
// set to the hook's time, and its worktree still on disk for the deferred reset.
func (h harness) checkEnded(t *testing.T, name string, state pool.State) {
	t.Helper()
	s := h.slot(t, name)
	if s.State != state || s.Lease != nil || !s.LastUsed.Equal(clock) {
		t.Errorf("%s = %s, lease %+v, last_used %s; want %s, no lease and %s", name, s.State,
			s.Lease, s.LastUsed, state, clock)
	}
	wt, ok := h.worktree(t, s.Path)
	if !ok {
		t.Errorf("git lists %s as %+v, %t; want it still there", s.Path, wt, ok)
	}
}

// remove runs the hook on req and fails the test unless it returned path and no error.
func (h harness) remove(t *testing.T, req Request, path string) {
	t.Helper()
	got, err := WorktreeRemove(context.Background(), h.env, req)
	if err != nil || got != path {
		t.Fatalf("WorktreeRemove = %q, %v; want %q and no error", got, err, path)
	}
}

// The acceptance test of #73, a clean slot: the remove after each captured desktop session's
// archive marks slot-1 needs-reset, its reset deferred to its next lease (transition 8; A5).
func TestRemoveCleanSlotNeedsReset(t *testing.T) {
	for _, file := range []string{"desktop-session-remove.json",
		"desktop-session-remove-reuse.json"} {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t, true)
			path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
			h.leaseSlot(t, "slot-1", path)
			h.remove(t, h.capture(t, file), path)
			h.checkEnded(t, "slot-1", pool.StateNeedsReset)
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing for a clean slot", h.stderr)
			}
		})
	}
}

// The acceptance test of #73, a dirty slot: the spike's session 3 left an untracked file, which
// the remove hook saw as testdata/captures/git/remove-status-untracked.txt records; rebuilt with
// real git, the slot is parked with the file kept and the line on stderr (transition 9).
func TestRemoveUntrackedFileParks(t *testing.T) {
	h := newHarness(t, true)
	spike := filepath.Join(h.e.Home, "spike")
	path := filepath.Join(spike, "ta")
	h.leaseSlot(t, "ta", path)
	untracked := filepath.Join(path, "spike-reuse.txt")
	if err := os.WriteFile(untracked, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "git",
		"remove-status-untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// e.Git returns git's output without its last newline.
	if got := h.e.Git("-C", path, "status", "--porcelain"); got+"\n" != string(want) {
		t.Fatalf("the rebuilt slot's status = %q, want the capture's %q", got, want)
	}

	b, err := os.ReadFile(filepath.Join(capturesDir, "sibling-session3-remove-dirty.json"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: h.e.Repo},
		pathvars.Var{Name: "spike", Path: spike})
	if err != nil {
		t.Fatal(err)
	}
	req, err := Decode(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	h.remove(t, req, path)
	h.checkEnded(t, "ta", pool.StateParked)
	if line := "upool: parked ta, which is dirty: " + string(want); h.stderr.String() != line {
		t.Errorf("stderr = %q, want %q", h.stderr, line)
	}
	if _, err := os.Stat(filepath.Join(path, "spike-reuse.txt")); err != nil {
		t.Errorf("spike-reuse.txt: %v, want it kept", err)
	}
}

// The scriptingDefineSymbols entry of testdata/captures/git/projectsettings-define-reorder.diff,
// as it was and as the Editor rewrote it, as internal/pool's tests use it.
const (
	capturedBefore = "    Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;STEAMWORKS_NET"
	capturedAfter  = "    Standalone: APP_UI_EDITOR_ONLY;STEAMWORKS_NET;SENTIS_ANALYTICS_ENABLED"
)

// Define-order noise counts as clean, but the hook leaves it in place: reverting it is a git
// write, which waits for the deferred reset (docs/design.md, "What remains unverified").
func TestRemoveCountsDefineOrderNoiseCleanAndKeepsIt(t *testing.T) {
	h := newHarness(t, true)
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", path)
	asset := filepath.Join(path, pool.SettingsAsset)
	b, err := os.ReadFile(asset)
	if err != nil || !strings.Contains(string(b), capturedBefore) {
		t.Fatalf("%s = %v, want the captured define order in it", asset, err)
	}
	reordered := strings.Replace(string(b), capturedBefore, capturedAfter, 1)
	if err := os.WriteFile(asset, []byte(reordered), 0o644); err != nil {
		t.Fatal(err)
	}
	h.remove(t, h.capture(t, "desktop-session-remove.json"), path)
	h.checkEnded(t, "slot-1", pool.StateNeedsReset)
	if got, err := os.ReadFile(asset); err != nil || string(got) != reordered {
		t.Errorf("%s was changed: %v; want the reorder left for the reset", asset, err)
	}
}

// A commit on the slot's detached HEAD is on no branch, so a reset would orphan it: the slot is
// dirty, however clean git status says it is (docs/contracts.md (f), the definition of clean).
func TestRemoveParksACommitOnNoBranch(t *testing.T) {
	h := newHarness(t, true)
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", path)
	h.e.Git("-C", path, "commit", "-q", "--allow-empty", "-m", "Work on no branch")
	h.remove(t, h.capture(t, "desktop-session-remove.json"), path)
	h.checkEnded(t, "slot-1", pool.StateParked)
	want := "upool: parked slot-1, which is dirty: HEAD is on no branch\n"
	if h.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", h.stderr, want)
	}
}

// The desktop app may hand the path back lowercased, as it stores it; on a case-insensitive volume
// that is the same slot.
func TestRemoveFindsTheSlotByItsLowercasedPath(t *testing.T) {
	h := newHarness(t, true)
	if sensitive, err := paths.CaseSensitive(h.e.Repo); err != nil || sensitive {
		t.Skipf("the volume is case-sensitive (%v), so a lowercased path is another path", err)
	}
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", path)
	req := h.capture(t, "desktop-session-remove.json")
	req.WorktreePath = strings.ToLower(req.WorktreePath)
	h.remove(t, req, path)
	h.checkEnded(t, "slot-1", pool.StateNeedsReset)
}

// A path that is no pool's slot and is already gone leaves nothing to remove, and the hook
// succeeds, so the app's removal goes on; with no registry at all it is the same.
func TestRemoveUnknownPathExitsZero(t *testing.T) {
	for name, register := range map[string]bool{"in a pool": true, "with no pool": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, register)
			req := h.capture(t, "desktop-session-remove.json")
			req.WorktreePath = filepath.Join(h.e.Repo, ".claude", "worktrees", "pwd-64bb41")
			h.remove(t, req, req.WorktreePath)
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", h.stderr)
			}
			if register {
				p, err := pool.Load(h.e.Home, h.key)
				if err != nil || len(p.Slots) != 0 {
					t.Errorf("slots = %+v, %v; want none", p.Slots, err)
				}
			}
		})
	}
}

// The hook refuses another event's request, and a slot with no lease to end, changing nothing;
// its error goes to stderr and the hook still exits 0 (cmd/upool's hook table).
func TestRemoveRefusesWhatItCannotEnd(t *testing.T) {
	h := newHarness(t, true)
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.leaseSlot(t, "slot-1", path)
	create := h.capture(t, "desktop-session-create.json")
	if got, err := WorktreeRemove(context.Background(), h.env, create); err == nil {
		t.Errorf("a create request: %q, want an error", got)
	}
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots[0].State, p.Slots[0].Lease = pool.StateReady, nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := WorktreeRemove(context.Background(), h.env,
		h.capture(t, "desktop-session-remove.json")); err == nil {
		t.Errorf("an unleased slot: %q, want an error", got)
	}
	if s := h.slot(t, "slot-1"); s.State != pool.StateReady || !s.LastUsed.Equal(before) {
		t.Errorf("slot-1 = %s, last_used %s; want it ready and unchanged", s.State, s.LastUsed)
	}
}

// probeWorktree makes the spike's probe worktree in h, the sibling worktree a project-scope create
// hook made for desktop-probe-create.json, on the branch that hook's name gives it, and returns
// desktop-probe-remove.json filled with h's paths, whose worktree_path it is.
func (h harness) probeWorktree(t *testing.T) Request {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(capturesDir, "desktop-probe-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: h.e.Home},
		pathvars.Var{Name: "repo", Path: h.e.Repo})
	if err != nil {
		t.Fatal(err)
	}
	req, err := Decode(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	h.e.Git("worktree", "add", "-q", "-b", "shell-command-output-8ff578", req.WorktreePath)
	return req
}

// checkGone fails the test unless the worktree at path is neither on disk nor in git's list, and
// branch, which it was on, is still there.
func (h harness) checkGone(t *testing.T, path, branch string) {
	t.Helper()
	if _, ok := h.worktree(t, path); ok {
		t.Errorf("git still lists %s", path)
	}
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s is still on disk", path)
	}
	if got := strings.TrimSpace(h.e.Git("branch", "--list", branch)); got != branch {
		t.Errorf("git branch --list %s = %q, want the branch kept", branch, got)
	}
}

// The acceptance test of #74, a clean worktree: the remove of desktop-probe-remove.json, pointed at
// a clean pass-through worktree, removes it with `git worktree remove`, and the branch keeps the
// commit made in it (A22).
func TestRemovePassThroughRemovesACleanWorktree(t *testing.T) {
	h := newHarness(t, false)
	req := h.probeWorktree(t)
	h.e.Git("-C", req.WorktreePath, "commit", "-q", "--allow-empty", "-m", "Work on the branch")
	commit := strings.TrimSpace(h.e.Git("-C", req.WorktreePath, "rev-parse", "HEAD"))
	h.remove(t, req, req.WorktreePath)
	h.checkGone(t, req.WorktreePath, "shell-command-output-8ff578")
	branch := strings.TrimSpace(h.e.Git("rev-parse", "shell-command-output-8ff578"))
	if got := branch; got != commit {
		t.Errorf("the branch is at %s, want the worktree's commit %s", got, commit)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr)
	}
}

// The acceptance test of #74, a dirty worktree: an untracked file keeps the worktree, file and all,
// and stderr says why; the hook still succeeds (A22).
func TestRemovePassThroughKeepsADirtyWorktree(t *testing.T) {
	h := newHarness(t, false)
	req := h.probeWorktree(t)
	notes := filepath.Join(req.WorktreePath, "notes.txt")
	if err := os.WriteFile(notes, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.remove(t, req, req.WorktreePath)
	if _, ok := h.worktree(t, req.WorktreePath); !ok {
		t.Errorf("git no longer lists %s", req.WorktreePath)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Errorf("notes.txt: %v, want it kept", err)
	}
	want := "upool: kept " + req.WorktreePath + ", which is dirty: ?? notes.txt\n"
	if h.stderr.String() != want {
		t.Errorf("stderr = %q, want %q", h.stderr, want)
	}
}

// Define-order noise alone counts as clean: it is reverted and the worktree removed. A commit on a
// detached HEAD is on no branch, so the worktree is kept.
func TestRemovePassThroughJudgesCleanAsSlotsAre(t *testing.T) {
	t.Run("define-order noise", func(t *testing.T) {
		h := newHarness(t, false)
		req := h.probeWorktree(t)
		asset := filepath.Join(req.WorktreePath, pool.SettingsAsset)
		b, err := os.ReadFile(asset)
		if err != nil {
			t.Fatal(err)
		}
		reordered := strings.Replace(string(b), capturedBefore, capturedAfter, 1)
		if err := os.WriteFile(asset, []byte(reordered), 0o644); err != nil {
			t.Fatal(err)
		}
		h.remove(t, req, req.WorktreePath)
		h.checkGone(t, req.WorktreePath, "shell-command-output-8ff578")
	})
	t.Run("a commit on no branch", func(t *testing.T) {
		h := newHarness(t, false)
		req := h.probeWorktree(t)
		h.e.Git("-C", req.WorktreePath, "checkout", "-q", "--detach")
		h.e.Git("-C", req.WorktreePath, "commit", "-q", "--allow-empty", "-m", "On no branch")
		h.remove(t, req, req.WorktreePath)
		if _, ok := h.worktree(t, req.WorktreePath); !ok {
			t.Errorf("git no longer lists %s", req.WorktreePath)
		}
		if !strings.HasSuffix(h.stderr.String(), "which is dirty: HEAD is on no branch\n") {
			t.Errorf("stderr = %q, want it to say HEAD is on no branch", h.stderr)
		}
	})
}

// The pass-through branch's own worktree, made for a desktop session in a repository upool does
// not manage, is removed again on the session's archive: the two branches of the hook undo each
// other.
func TestRemovePassThroughUndoesTheCreate(t *testing.T) {
	h := newHarness(t, false)
	create := h.capture(t, "desktop-session-create.json")
	path, err := WorktreeCreate(context.Background(), h.env, create)
	if err != nil {
		t.Fatal(err)
	}
	remove := h.capture(t, "desktop-session-remove.json")
	remove.WorktreePath = path
	h.remove(t, remove, path)
	h.checkGone(t, path, create.Name)
}

// git's own refusals stand: a directory that is no worktree, and a locked worktree, are left as
// they are, and the error is the hook's to put on stderr.
func TestRemovePassThroughLeavesWhatGitRefuses(t *testing.T) {
	t.Run("not a worktree", func(t *testing.T) {
		h := newHarness(t, false)
		req := h.probeWorktree(t)
		req.WorktreePath = filepath.Join(h.e.Repo, "Assets")
		if got, err := WorktreeRemove(context.Background(), h.env, req); err == nil {
			t.Errorf("WorktreeRemove = %q, want git's refusal", got)
		}
		if _, err := os.Stat(filepath.Join(h.e.Repo, "Assets")); err != nil {
			t.Errorf("Assets: %v, want it left alone", err)
		}
	})
	t.Run("locked", func(t *testing.T) {
		h := newHarness(t, false)
		req := h.probeWorktree(t)
		h.e.Git("worktree", "lock", req.WorktreePath)
		if got, err := WorktreeRemove(context.Background(), h.env, req); err == nil {
			t.Errorf("WorktreeRemove = %q, want git's refusal", got)
		}
		if _, ok := h.worktree(t, req.WorktreePath); !ok {
			t.Errorf("git no longer lists %s", req.WorktreePath)
		}
	})
}
