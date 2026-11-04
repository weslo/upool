//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of `upool take --confirm`, on the harness pool. They run on macOS only, since a lease's
// holder is recorded with its start time, which internal/proc reads on macOS alone.

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

// The confirm copies no file into the slot, with a .worktreeinclude or without one: Claude Code
// has read CLAUDE.md before SessionStart runs, so the files a .worktreeinclude names are mirrored
// into free slots before a handout instead (A44; #233). The slot's stale copy of a gitignored file,
// and an entry only it has, stay as they are.
func TestConfirmCopiesNothing(t *testing.T) {
	for _, include := range []string{"", ".claude/\nCLAUDE.md\n"} {
		t.Run(map[bool]string{false: "no .worktreeinclude", true: "a .worktreeinclude"}[include !=
			""], func(t *testing.T) {
			p := newHarnessPool(t)
			if include != "" {
				p.WriteFile(".worktreeinclude", include)
			}
			s := p.hookLease(t)
			slotFile := func(name string) string {
				return filepath.Join(s.Path, filepath.FromSlash(name))
			}
			for _, name := range []string{".claude/settings.local.json", ".claude/stale.json"} {
				if err := os.MkdirAll(filepath.Dir(slotFile(name)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(slotFile(name), []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p.WriteFile(".claude/settings.local.json", `{"model": "opus"}`+"\n")

			p.confirmSlot(t, s.Path, s.Path)
			for _, name := range []string{".claude/settings.local.json", ".claude/stale.json"} {
				if got, err := os.ReadFile(slotFile(name)); err != nil || string(got) != "{}\n" {
					t.Errorf("the slot's %s = %q, %v; want it as it was", name, got, err)
				}
			}
			for _, name := range []string{"CLAUDE.md", testenv.SkillLink} {
				if _, err := os.Lstat(slotFile(name)); err == nil {
					t.Errorf("the slot has %s, want the confirm to copy nothing", name)
				}
			}
		})
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

// Confirming a confirmed lease succeeds, and leaves the session's edit to a gitignored file as it
// is, since the confirm copies nothing (A44).
func TestConfirmIsIdempotent(t *testing.T) {
	p := newHarnessPool(t)
	s := p.hookLease(t)
	p.confirmSlot(t, s.Path, s.Path)
	claudeMD := filepath.Join(s.Path, "CLAUDE.md")
	if err := os.WriteFile(claudeMD, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.confirmSlot(t, s.Path, s.Path)
	if got, err := os.ReadFile(claudeMD); err != nil || string(got) != "edited\n" {
		t.Errorf("CLAUDE.md = %q, %v; want the session's edit kept", got, err)
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

// The acceptance test of #284: the startup that confirms a slot the create hook leased leaves it
// detached at base, since a hook's handout stays detached (A18), and the context it prints tells
// the session how to branch from the pool's base: after `git fetch origin`, from origin/<base>,
// when the repository has it, and from the base itself when it has no origin (A17). It reads the
// slot after the confirm, so a slot its last session left on a branch, handed out from
// needs-reset, is told the same once the deferred reset has detached it. The commands the context
// gives make the session's branch in the slot, and the checkout it forbids is one git refuses
// there, naming the main checkout, which has the base checked out. Once the session is on its
// branch, a compaction, which sends the instructions again, and a subagent's start tell it only
// never to check out the base: a branch from base would leave the session's work behind.
func TestConfirmLeavesTheSlotDetachedAndItsContextSaysHowToBranch(t *testing.T) {
	for _, tt := range []struct {
		name               string
		onOrigin, lastWork bool
	}{{"no origin", false, false}, {"origin", true, false}, {"needs-reset", false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			if tt.onOrigin {
				origin := tempDir(t)
				p.Git("init", "-q", "--bare", "-b", testenv.Base, origin)
				p.Git("remote", "add", "origin", origin)
				p.Git("push", "-q", "origin", testenv.Base)
			}
			if tt.lastWork {
				slot := p.slot("slot-1")
				p.Git("-C", slot, "checkout", "-q", "-b", "old-work")
				p.Git("-C", slot, "commit", "-q", "--allow-empty", "-m", "The last session's work")
				p.update(t, func(pl *pool.Pool) {
					pl.Slots[0].State = pool.StateNeedsReset
					pl.Slots[1].State = pool.StateParked
				})
			}
			p.Setenv("CLAUDE_ENV_FILE", filepath.Join(tempDir(t), "sessionstart-hook-0.sh"))
			s := p.hookLease(t)
			if tt.lastWork && !s.Lease.ResetPending {
				t.Fatalf("the hook's take leased %+v, want reset_pending", s)
			}
			detached := assets.Branching{Base: testenv.Base, Detached: true, OnOrigin: tt.onOrigin}
			out := runStartHook(t, p, "session-start", "session-start.json", s.Path, detached)
			if got := p.stateOf(t, s.Name); got.State != pool.StateLeased ||
				!got.Lease.Confirmed || got.Lease.ResetPending {
				t.Errorf("%s = %+v, want it leased and confirmed, with no reset pending", s.Name,
					got)
			}
			if got := p.Git("-C", s.Path, "branch", "--show-current"); got != "" {
				t.Errorf("the slot's branch = %q, want none: the handout stays detached", got)
			}

			forbidden, branch := branchCommands(t, out)
			if want := "git checkout " + testenv.Base; forbidden != want {
				t.Errorf("the context forbids %q, want %q", forbidden, want)
			}
			res, err := git.Exec{}.Run(context.Background(), s.Path, p.Environ(),
				strings.Fields(forbidden)[1:]...)
			if err != nil || res.ExitCode == 0 || !strings.Contains(string(res.Stderr), p.Repo) {
				t.Errorf("%s in the slot: exit %d, stderr %q, %v; want git's refusal naming %s",
					forbidden, res.ExitCode, res.Stderr, err, p.Repo)
			}
			for _, c := range branch {
				p.Git(append([]string{"-C", s.Path},
					strings.Fields(strings.ReplaceAll(c, "<name>", "issue-284"))[1:]...)...)
			}
			if got := p.Git("-C", s.Path, "branch", "--show-current"); got != "issue-284" {
				t.Errorf("after %q, the slot's branch = %q, want issue-284", branch, got)
			}
			if got := p.Git("-C", s.Path, "rev-parse", "HEAD"); got != p.BaseCommit {
				t.Errorf("issue-284 is at %s, want base's commit %s", got, p.BaseCommit)
			}

			onBranch := assets.Branching{Base: testenv.Base}
			runStartHook(t, p, "session-start", "session-start-compact.json", s.Path, onBranch)
			runStartHook(t, p, "subagent-start", "cli-subagent-start.json", s.Path, onBranch)
		})
	}
}

// runStartHook runs `upool hook <event>` in p through the dispatcher on capture, its cwd dir, and
// fails the test unless it exits 0, prints the context document that says what b says about
// branching, and says nothing on stderr. It returns what the hook printed. The top-up a startup's
// confirm starts is /usr/bin/true, which does nothing, so that no refresh outlives the test.
func runStartHook(t *testing.T, p harnessPool, event, capture, dir string,
	b assets.Branching) string {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable,
		stdin:  strings.NewReader(slotStdin(t, p, capture, dir)),
		stdout: &out, stderr: &errOut, getenv: p.Getenv, environ: p.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid(), ppid: os.Getppid(),
		executable: func() (string, error) { return "/usr/bin/true", nil }}
	code := d.run([]string{"hook", event})
	wantEvent := map[string]string{"session-start": hooks.EventSessionStart,
		"subagent-start": hooks.EventSubagentStart}[event]
	doc, err := hooks.ContextDocument(wantEvent, b)
	if err != nil {
		t.Fatal(err)
	}
	if code != output.ExitOK || out.String() != doc+"\n" || errOut.Len() != 0 {
		t.Fatalf("hook %s on %s: exit %d, stdout %q, stderr %q; want 0, the context for %+v "+
			"and nothing", event, capture, code, out.String(), errOut.String(), b)
	}
	return out.String()
}

// branchCommands returns, from doc, the context document a session-start hook printed, the
// command its instructions forbid in the slot and the ones they give to make the session's branch,
// in order: each `git …` in backticks in the item that says the slot starts detached (#284).
func branchCommands(t *testing.T, doc string) (forbidden string, branch []string) {
	t.Helper()
	var d struct {
		Out struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	var item string
	for _, line := range strings.Split(d.Out.Context, "\n") {
		if strings.HasPrefix(line, "- Your slot starts detached at ") {
			item = line
		}
	}
	spans := strings.Split(item, "`")
	for i := 1; i < len(spans); i += 2 {
		if !strings.HasPrefix(spans[i], "git ") {
			continue
		}
		if forbidden == "" {
			forbidden = spans[i]
		} else {
			branch = append(branch, spans[i])
		}
	}
	if forbidden == "" || len(branch) == 0 {
		t.Fatalf("the context has no item that forbids a command and gives a branch's: %q",
			d.Out.Context)
	}
	return forbidden, branch
}
