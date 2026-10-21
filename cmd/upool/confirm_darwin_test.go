//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of `upool take --confirm`, on the harness pool. They run on macOS only, since the
// agent files are copied with cp -cR, macOS's clonefile(2) (internal/clone).

// hookLease gives one of p's slots the create hook's unconfirmed lease, as the pooled branch takes
// it, and returns that slot.
func (p harnessPool) hookLease(t *testing.T) pool.Slot {
	t.Helper()
	s, _, err := pool.UnconfirmedTake{Holder: "49b55ea5-fc66-40a8-8f6c-b4afc31ec456",
		NameHint: "system-diagnostics-9f8a09", Now: clock, ConfirmTimeout: time.Minute}.Take(
		context.Background(), p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// confirmSlot runs `upool take --confirm path` in p and fails the test unless it printed path.
func (p harnessPool) confirmSlot(t *testing.T, path, want string) result {
	t.Helper()
	r := runIn(t, p.Env, p.Repo, "take", "--confirm", path)
	if r.code != output.ExitOK || r.stdout != want+"\n" {
		t.Fatalf("take --confirm: exit = %d, stdout %q, stderr %q; want %d and %q", r.code,
			r.stdout, r.stderr, output.ExitOK, want+"\n")
	}
	return r
}

// stateOf returns p's slot name from state.json.
func (p harnessPool) stateOf(t *testing.T, name string) pool.Slot {
	t.Helper()
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pl.Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("state.json has no %s", name)
	return pool.Slot{}
}

// The confirm makes the lease leased and confirmed, held by upool's parent, the session's CLI
// (A6), and takes no git worktree lock (A27); stdout is the slot's path.
func TestConfirmConfirmsTheLease(t *testing.T) {
	p := newHarnessPool(t)
	s := p.hookLease(t)
	p.confirmSlot(t, s.Path, s.Path)
	got := p.stateOf(t, s.Name)
	l := got.Lease
	if got.State != pool.StateLeased || l == nil || !l.Confirmed || l.LeaseID != s.Lease.LeaseID {
		t.Fatalf("%s = %+v, want leased under its lease, confirmed", s.Name, got)
	}
	if l.HolderPID == nil || *l.HolderPID != os.Getppid() || l.HolderPIDStart == nil {
		t.Errorf("holder = %v, %v; want upool's parent, %d, with its start time", l.HolderPID,
			l.HolderPIDStart, os.Getppid())
	}
	if list := p.Git("worktree", "list", "--porcelain"); strings.Contains(list, "\nlocked") {
		t.Errorf("git worktree list = %q, want no worktree locked (A27)", list)
	}
}

// The acceptance test of #76, the agent files: a changed .claude/settings.local.json in the main
// checkout replaces the slot's stale copy; an entry only the slot has goes; the slot's own
// .claude/worktrees stays and the main checkout's, where the slots are, is never copied; and the
// relative skill link stays relative (spike §10).
func TestConfirmRefreshesStaleAgentFiles(t *testing.T) {
	p := newHarnessPool(t)
	s := p.hookLease(t)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	slotFile := func(name string) string { return filepath.Join(s.Path, filepath.FromSlash(name)) }
	write(slotFile(".claude/settings.local.json"), "{}\n")
	write(slotFile(".claude/stale.json"), "{}\n")
	write(slotFile(".claude/worktrees/keep/file"), "kept\n")
	p.WriteFile(".claude/settings.local.json", `{"model": "opus"}`+"\n")

	p.confirmSlot(t, s.Path, s.Path)
	for _, name := range []string{".claude/settings.local.json", "CLAUDE.md",
		".agents/skills/x/SKILL.md"} {
		want, err := os.ReadFile(p.Path(name))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(slotFile(name)); err != nil || string(got) != string(want) {
			t.Errorf("the slot's %s = %q, %v; want the main checkout's %q", name, got, err, want)
		}
	}
	if _, err := os.Lstat(slotFile(".claude/stale.json")); err == nil {
		t.Error("the slot still has .claude/stale.json, which the main checkout lacks")
	}
	entries, err := os.ReadDir(slotFile(".claude/worktrees"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
		t.Errorf("the slot's .claude/worktrees = %v, %v; want its own keep alone", entries, err)
	}
	if link, err := os.Readlink(slotFile(testenv.SkillLink)); err != nil ||
		link != testenv.SkillTarget {
		t.Errorf("the slot's %s links to %q, %v; want %q", testenv.SkillLink, link, err,
			testenv.SkillTarget)
	}
}

// The acceptance test of #76, the deferred reset: a slot handed out from needs-reset, left on a
// branch with a commit and define-order noise by its last session, is reset before the confirm:
// noise reverted, detached at base, the branch and its commit kept, and reset_pending cleared
// (A5; A23).
func TestConfirmRunsDeferredReset(t *testing.T) {
	p := newHarnessPool(t)
	slot := p.slot("slot-1")
	p.Git("-C", slot, "checkout", "-q", "-b", "old-work")
	p.Git("-C", slot, "commit", "-q", "--allow-empty", "-m", "The last session's work")
	work := strings.TrimSpace(p.Git("rev-parse", "old-work"))
	asset := filepath.Join(slot, pool.SettingsAsset)
	b, err := os.ReadFile(asset)
	if err != nil {
		t.Fatal(err)
	}
	const before = "SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;STEAMWORKS_NET"
	const after = "APP_UI_EDITOR_ONLY;STEAMWORKS_NET;SENTIS_ANALYTICS_ENABLED"
	if !strings.Contains(string(b), before) {
		t.Fatalf("%s has no %q", asset, before)
	}
	if err := os.WriteFile(asset, []byte(strings.Replace(string(b), before, after, 1)),
		0o644); err != nil {
		t.Fatal(err)
	}
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].State = pool.StateNeedsReset
		pl.Slots[1].State = pool.StateParked
	})
	s := p.hookLease(t)
	if s.Name != "slot-1" || !s.Lease.ResetPending {
		t.Fatalf("the hook's take leased %+v, want slot-1 with reset_pending", s)
	}

	p.confirmSlot(t, s.Path, s.Path)
	if head, base := strings.TrimSpace(p.Git("-C", slot, "rev-parse", "HEAD")),
		strings.TrimSpace(p.Git("rev-parse", testenv.Base)); head != base {
		t.Errorf("slot-1 is at %s, want base %s", head, base)
	}
	branch := strings.TrimSpace(p.Git("-C", slot, "rev-parse", "--abbrev-ref", "HEAD"))
	if branch != "HEAD" {
		t.Errorf("slot-1 is on %q, want detached", branch)
	}
	if status := p.Git("-C", slot, "status", "--porcelain"); status != "" {
		t.Errorf("slot-1's status = %q, want clean: the noise reverted", status)
	}
	if got := strings.TrimSpace(p.Git("rev-parse", "old-work")); got != work {
		t.Errorf("old-work is at %s, want the session's commit %s kept", got, work)
	}
	if got := p.stateOf(t, "slot-1"); got.State != pool.StateLeased || got.Lease.ResetPending {
		t.Errorf("slot-1 = %s, reset_pending %t; want leased with the reset done", got.State,
			got.Lease.ResetPending)
	}
}

// A needs-reset slot that is somehow dirty is not reset, which would take its files, nor
// confirmed; the lease stays as the hook's take left it.
func TestConfirmRefusesADirtySlotsReset(t *testing.T) {
	p := newHarnessPool(t)
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[0].State = pool.StateNeedsReset
		pl.Slots[1].State = pool.StateParked
	})
	s := p.hookLease(t)
	untracked := filepath.Join(s.Path, "work.txt")
	if err := os.WriteFile(untracked, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := runIn(t, p.Env, p.Repo, "take", "--confirm", s.Path)
	if r.code != output.ExitFailure ||
		!strings.Contains(r.stderr, "is dirty, so nothing was reset") {
		t.Errorf("exit = %d, stderr %q; want %d and the refusal", r.code, r.stderr,
			output.ExitFailure)
	}
	if _, err := os.Stat(untracked); err != nil {
		t.Errorf("work.txt: %v, want it kept", err)
	}
	if got := p.stateOf(t, s.Name); got.State != pool.StateLeasedUnconfirmed ||
		!got.Lease.ResetPending {
		t.Errorf("%s = %s, reset_pending %t; want the lease left unconfirmed", s.Name, got.State,
			got.Lease.ResetPending)
	}
}

// The desktop app may hand the slot's path back lowercased; on a case-insensitive volume it is the
// same slot, and the confirm prints the slot's own path.
func TestConfirmMatchesLowercasedPath(t *testing.T) {
	p := newHarnessPool(t)
	if sensitive, err := paths.CaseSensitive(p.Repo); err != nil || sensitive {
		t.Skipf("the volume is case-sensitive (%v), so a lowercased path is another path", err)
	}
	s := p.hookLease(t)
	p.confirmSlot(t, strings.ToLower(s.Path), s.Path)
	if got := p.stateOf(t, s.Name); got.State != pool.StateLeased {
		t.Errorf("%s = %s, want leased", s.Name, got.State)
	}
}

// Confirming a confirmed lease succeeds and re-copies the agent files again.
func TestConfirmIsIdempotent(t *testing.T) {
	p := newHarnessPool(t)
	s := p.hookLease(t)
	p.confirmSlot(t, s.Path, s.Path)
	claudeMD := filepath.Join(s.Path, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.confirmSlot(t, s.Path, s.Path)
	want, err := os.ReadFile(p.Path("CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(claudeMD); err != nil || string(got) != string(want) {
		t.Errorf("CLAUDE.md = %q, %v; want the main checkout's copy again", got, err)
	}
	if got := p.stateOf(t, s.Name); got.State != pool.StateLeased || !got.Lease.Confirmed {
		t.Errorf("%s = %+v, want it still leased and confirmed", s.Name, got)
	}
}

// A path that is no leased slot exits 1, and so does --confirm with take's other arguments.
func TestConfirmRefusesWhatIsNotALeasedSlot(t *testing.T) {
	p := newHarnessPool(t)
	for name, args := range map[string][]string{
		"a ready slot":    {"take", "--confirm", p.slot("slot-1")},
		"no slot":         {"take", "--confirm", p.Path("Assets")},
		"a branch name":   {"take", "feature", "--confirm", p.slot("slot-1")},
		"another flag":    {"take", "--confirm", p.slot("slot-1"), "--base", "main"},
		"a timeout too":   {"take", "--confirm", p.slot("slot-1"), "--timeout", "5"},
		"no-create, also": {"take", "--confirm", p.slot("slot-1"), "--no-create"},
	} {
		r := runIn(t, p.Env, p.Repo, args...)
		if r.code != output.ExitFailure || r.stdout != "" {
			t.Errorf("%s: exit = %d, stdout %q; want %d and nothing", name, r.code, r.stdout,
				output.ExitFailure)
		}
	}
	if got := p.stateOf(t, "slot-1"); got.State != pool.StateReady || got.Lease != nil {
		t.Errorf("slot-1 = %+v, want it ready and unleased", got)
	}
}
