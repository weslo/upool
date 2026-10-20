package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of `upool release`, on the harness pool (harness_test.go), with slot-1 leased by
// `upool take feature-x` as a person would lease it.

// takenPool is a harness pool with slot-1 taken on the branch feature-x.
func takenPool(t *testing.T) harnessPool {
	t.Helper()
	p := newHarnessPool(t)
	if r := runIn(t, p.Env, p.Repo, "take", "feature-x"); r.code != output.ExitOK {
		t.Fatalf("take: exit %d, stderr %q", r.code, r.stderr)
	}
	return p
}

// inSlot runs git in the harness pool's slot-1 and returns its output.
func (p harnessPool) inSlot(args ...string) string {
	return p.Git(append([]string{"-C", p.slot("slot-1")}, args...)...)
}

// checkReleased fails the test unless slot-1 is in state, with no lease and its worktree
// unlocked.
func (p harnessPool) checkReleased(t *testing.T, state pool.State) pool.Slot {
	t.Helper()
	s := p.loadPool(t).Slots[0]
	if s.State != state || s.Lease != nil {
		t.Errorf("slot-1 = %s, lease %+v; want %s with no lease", s.State, s.Lease, state)
	}
	if p.worktree(t, s.Path).Locked {
		t.Error("slot-1 is still locked, want it unlocked")
	}
	return s
}

// A clean slot goes back to base, detached, with the branch and its commits kept, the ignored
// Library kept, and last_used set (docs/design.md, "Slot lifecycle: seed only from a Library
// current at HEAD, refresh on handout, reset without `-x`"; A5).
func TestReleaseResetsCleanSlot(t *testing.T) {
	tests := []struct {
		name string
		dir  func(p harnessPool) string
		args []string
	}{
		{"the slot containing the working directory",
			func(p harnessPool) string { return p.slot("slot-1") + "/Assets/Scripts" }, nil},
		{"by path", func(p harnessPool) string { return p.Repo },
			[]string{".claude/worktrees/slot-1"}},
		{"by branch", func(p harnessPool) string { return p.Repo }, []string{"feature-x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := takenPool(t)
			p.WriteFile(".claude/worktrees/slot-1/Notes.md", "work\n")
			p.inSlot("add", "Notes.md")
			p.inSlot("commit", "-q", "-m", "work on feature-x")
			work := p.inSlot("rev-parse", "HEAD")
			p.WriteFile(".claude/worktrees/slot-1/Library/marker", "warm\n")

			r := runIn(t, p.Env, tt.dir(p), append([]string{"release"}, tt.args...)...)
			if r.code != output.ExitOK || r.stdout != "released slot-1: ready, detached at main\n" ||
				r.stderr != "" {
				t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and the slot ready", r.code,
					r.stdout, r.stderr)
			}
			s := p.checkReleased(t, pool.StateReady)
			if !s.LastUsed.Equal(clock) {
				t.Errorf("last_used = %s, want the clock, %s", s.LastUsed, clock)
			}
			if head := p.inSlot("rev-parse", "HEAD"); head != p.BaseCommit {
				t.Errorf("slot-1 HEAD = %s, want base at %s", head, p.BaseCommit)
			}
			if out := p.inSlot("status", "--porcelain", "--branch"); !strings.HasPrefix(out,
				"## HEAD (no branch)") {
				t.Errorf("slot-1 status = %q, want detached and clean", out)
			}
			if got := p.Git("rev-parse", "feature-x"); got != work {
				t.Errorf("feature-x = %s, want the slot's commit kept, %s", got, work)
			}
			if _, err := os.Stat(filepath.Join(p.slot("slot-1"), "Library", "marker")); err != nil {
				t.Errorf("Library/marker: %v, want the ignored Library kept", err)
			}
		})
	}
}

// An untracked file parks the slot, unreset and unlocked, with the file and its status line on
// stderr: the remove hook's capture of the same state (testdata/captures/git/
// remove-status-untracked.txt), rebuilt with real git.
func TestReleaseParksUntrackedFile(t *testing.T) {
	p := takenPool(t)
	p.WriteFile(".claude/worktrees/slot-1/spike-reuse.txt", "left behind\n")
	capture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "git",
		"remove-status-untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(capture), "\n")
	if got := p.inSlot("status", "--porcelain"); got != line {
		t.Fatalf("slot-1 status = %q, want the capture's %q", got, line)
	}

	r := runIn(t, p.Env, p.Repo, "release", "feature-x")
	want := "upool: warning: slot-1 is parked, not reset: " + line + "\n"
	if r.code != output.ExitOK || r.stderr != want ||
		r.stdout != "parked slot-1, which is dirty; nothing in it was changed\n" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0, parked, and %q", r.code, r.stdout,
			r.stderr, want)
	}
	p.checkReleased(t, pool.StateParked)
	if _, err := os.Stat(filepath.Join(p.slot("slot-1"), "spike-reuse.txt")); err != nil {
		t.Errorf("spike-reuse.txt: %v, want it kept", err)
	}
	if got := p.inSlot("symbolic-ref", "--short", "HEAD"); got != "feature-x" {
		t.Errorf("slot-1 HEAD = %s, want feature-x, untouched", got)
	}
}

// A commit on the slot's detached HEAD that no ref contains parks the slot, since a reset to base
// would orphan it (docs/contracts.md (f), the definition of clean; A5).
func TestReleaseParksDetachedCommit(t *testing.T) {
	p := takenPool(t)
	p.inSlot("checkout", "-q", "--detach")
	p.WriteFile(".claude/worktrees/slot-1/Notes.md", "lost?\n")
	p.inSlot("add", "Notes.md")
	p.inSlot("commit", "-q", "-m", "on no branch")
	head := p.inSlot("rev-parse", "HEAD")

	r := runIn(t, p.Env, p.slot("slot-1"), "release", "--json")
	env := decode(t, r.stdout)
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got releaseReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"slot-1 is parked, not reset: HEAD is on no branch"}
	if r.code != output.ExitOK || got.State != pool.StateParked ||
		strings.Join(got.Dirty, "|") != "HEAD is on no branch" ||
		strings.Join(env.Warnings, "|") != strings.Join(want, "|") {
		t.Errorf("exit %d, data %s, warnings %q; want parked because HEAD is on no branch",
			r.code, data, env.Warnings)
	}
	p.checkReleased(t, pool.StateParked)
	if got := p.inSlot("rev-parse", "HEAD"); got != head {
		t.Errorf("slot-1 HEAD = %s, want the commit kept checked out, %s", got, head)
	}
}

// The Editor's reorder of scriptingDefineSymbols is noise: release reverts it and resets the slot
// (testdata/captures/git/projectsettings-define-reorder.diff; internal/pool's noise rule).
func TestReleaseRevertsDefineReorder(t *testing.T) {
	p := takenPool(t)
	settings := ".claude/worktrees/slot-1/" + pool.SettingsAsset
	committed := readFile(t, p.Env, settings)
	reordered := strings.Replace(committed,
		"Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;STEAMWORKS_NET",
		"Standalone: APP_UI_EDITOR_ONLY;STEAMWORKS_NET;SENTIS_ANALYTICS_ENABLED", 1)
	if reordered == committed {
		t.Fatal("the settings file has no define line to reorder")
	}
	p.WriteFile(settings, reordered)

	r := runIn(t, p.Env, p.Repo, "release", "feature-x")
	if r.code != output.ExitOK || r.stderr != "" {
		t.Errorf("exit %d, stderr %q; want 0 and nothing", r.code, r.stderr)
	}
	p.checkReleased(t, pool.StateReady)
	if got := readFile(t, p.Env, settings); got != committed {
		t.Errorf("%s = %q, want the committed order back", settings, got)
	}
}

// A define added, unlike a reorder, is work: the slot is parked with the change kept.
func TestReleaseParksAnyOtherSettingsChange(t *testing.T) {
	p := takenPool(t)
	settings := ".claude/worktrees/slot-1/" + pool.SettingsAsset
	changed := strings.Replace(readFile(t, p.Env, settings), "STEAMWORKS_NET",
		"STEAMWORKS_NET;NEW_DEFINE", 1)
	p.WriteFile(settings, changed)
	r := runIn(t, p.Env, p.Repo, "release", "feature-x")
	if r.code != output.ExitOK || !strings.Contains(r.stderr, " M "+pool.SettingsAsset) {
		t.Errorf("exit %d, stderr %q; want 0 and the settings file's status line", r.code,
			r.stderr)
	}
	p.checkReleased(t, pool.StateParked)
	if got := readFile(t, p.Env, settings); got != changed {
		t.Errorf("%s changed, want the new define kept", settings)
	}
}

// --if-lease-id naming another lease changes nothing; naming the slot's lease releases it.
func TestReleaseIfLeaseID(t *testing.T) {
	p := takenPool(t)
	id := p.loadPool(t).Slots[0].Lease.LeaseID
	r := runIn(t, p.Env, p.Repo, "release", "feature-x", "--if-lease-id", "other")
	want := "upool: slot-1's lease is " + id + ", not other; nothing was changed\n"
	if r.code != output.ExitFailure || r.stderr != want {
		t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, want)
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil ||
		!p.worktree(t, s.Path).Locked {
		t.Errorf("slot-1 = %s, lease %+v; want it still leased and locked", s.State, s.Lease)
	}
	r = runIn(t, p.Env, p.Repo, "release", "feature-x", "--if-lease-id", id)
	if r.code != output.ExitOK {
		t.Errorf("--if-lease-id %s: exit %d, stderr %q; want 0", id, r.code, r.stderr)
	}
	p.checkReleased(t, pool.StateReady)
}

func TestReleaseRefusesWhatItCannotRelease(t *testing.T) {
	p := takenPool(t)
	tests := []struct {
		name string
		dir  string
		args []string
		want string
	}{
		{"a slot that is not leased", p.Repo, []string{".claude/worktrees/slot-2"},
			"slot-2 is ready, not leased, so there is nothing to release"},
		{"the main checkout", p.Repo, nil,
			p.Repo + " is not inside a slot; name the slot by its path or its branch"},
		{"a branch no slot has", p.Repo, []string{"no-such-branch"},
			"no slot of " + p.Repo + " has branch no-such-branch checked out"},
		{"a branch only the main checkout has", p.Repo, []string{testenv.Base},
			"has branch main checked out; " + p.Repo + " has it, and is not a slot"},
		{"two arguments", p.Repo, []string{"feature-x", "other"}, "unexpected arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runIn(t, p.Env, tt.dir, append([]string{"release"}, tt.args...)...)
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, tt.want) {
				t.Errorf("exit %d, stderr %q; want 1 and %q", r.code, r.stderr, tt.want)
			}
		})
	}
	if s := p.loadPool(t).Slots[0]; s.State != pool.StateLeased || s.Lease == nil {
		t.Errorf("slot-1 = %s, lease %+v; want it still leased", s.State, s.Lease)
	}
	outside := tempDir(t)
	r := runIn(t, p.Env, outside, "release")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr,
		outside+" is not inside a registered pool") {
		t.Errorf("outside a pool: exit %d, stderr %q; want 1", r.code, r.stderr)
	}
}

// release compares the slot's cache key across its reset: a changed key sets reimport_pending and
// warns that the next open is a large reimport; an equal one changes nothing (A20). The fake unity
// replays testdata/captures/unity/cache-key.json, and a copy with the key changed.
func TestReleaseComparesTheCacheKeyAcrossTheReset(t *testing.T) {
	const key = "0cce5c29ab901534b6575c7da8c743206ba94cf3ff8898d5f290334719897bf7"
	for _, tt := range []struct {
		name    string
		after   []string
		pending bool
	}{
		{"equal keys", nil, false},
		{"a changed key", []string{key, strings.Repeat("ef", 32)}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := takenPool(t)
			u := fake.Install(t, fake.Reply{Capture: "cache-key.json"},
				fake.Reply{Capture: "cache-key.json", Replace: tt.after})
			p.Setenv("PATH", u.Dir()+string(os.PathListSeparator)+p.Getenv("PATH"))
			r := runIn(t, p.Env, p.Repo, "release", "feature-x")
			want := ""
			if tt.pending {
				want = "upool: warning: slot-1's unity cache key changed across its checkout: its " +
					"next open is a large reimport\n"
			}
			if r.code != output.ExitOK || r.stderr != want {
				t.Errorf("exit %d, stderr %q; want 0 and %q", r.code, r.stderr, want)
			}
			if s := p.checkReleased(t, pool.StateReady); s.ReimportPending != tt.pending {
				t.Errorf("reimport_pending = %t, want %t", s.ReimportPending, tt.pending)
			}
			if calls := u.Calls(); len(calls) != 2 || calls[0].Args[2] != p.slot("slot-1") {
				t.Errorf("unity calls = %+v, want two, on slot-1", calls)
			}
		})
	}
}
