package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// The tests of refresh's move of ready slots to base, and of the re-warm after it (#97), on the
// harness pool, with an origin the tests push to: slot-1 is ready and idle, slot-2 leased, so that
// the top-up has the one ready slot it asks for and clones nothing. The fake unity replays the
// captures of `unity cache key`, open, `status --until-ready`, save_all and close, and the
// process table is injected, since the Editor's PID is the capture's.

// The fake's reply to `unity close` on a headless Editor.
var closeHeadlessReply = fake.Reply{Capture: "close-headless.json", Vars: spikeVars}

// closeUnreachableReply and closeForcedReply are the replies of `unity close` and `unity close
// --force` to an Editor in the middle of a cold import, which the first leaves running (#224).
var (
	closeUnreachableReply = fake.Reply{Capture: "close-unreachable.json",
		Vars: []pathvars.Var{{Name: "repo", Path: "/repo"}}, Exit: 6}
	closeForcedReply = fake.Reply{Capture: "close-force-unreachable.json",
		Vars: []pathvars.Var{{Name: "repo", Path: "/repo"}}}
	// noEditorsReply is the reply of the `unity status` after a forced close, listing no Editor,
	// before the instance files the terminated Editor left go (A45).
	noEditorsReply = fake.Reply{Capture: "status-no-instances.json", Exit: 6}
)

// fetchPool is a harness pool with slot-2 leased, slot-1 with a Library, so that its re-warm is
// not a cold import, and an origin, a bare repository holding main as the pool's main checkout has
// it. It returns the pool and origin's path.
func fetchPool(t *testing.T) (harnessPool, string) {
	t.Helper()
	p := warmPool(t)
	p.update(t, func(pl *pool.Pool) {
		pl.Slots[1].State = pool.StateLeased
		pl.Slots[1].Lease = &pool.Lease{LeaseID: "id", Holder: "bob", AcquiredAt: clock,
			Confirmed: true, Lane: pool.LaneScript}
	})
	origin := tempDir(t)
	p.Git("init", "-q", "--bare", "-b", testenv.Base, origin)
	p.Git("remote", "add", "origin", origin)
	p.Git("push", "-q", "origin", testenv.Base)
	return p, origin
}

// pushUpstream commits a change to a script on origin's main, from a clone of its own, as another
// developer would, and returns the new commit: the pool's main checkout does not have it.
func pushUpstream(t *testing.T, p harnessPool, origin string) string {
	t.Helper()
	other := filepath.Join(tempDir(t), "other")
	p.Git("clone", "-q", origin, other)
	script := filepath.Join(other, filepath.FromSlash(testenv.PlayerScript))
	if err := os.WriteFile(script, []byte("// moved on upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.Git("-C", other, "commit", "-q", "-am", "a script change upstream")
	p.Git("-C", other, "push", "-q", "origin", testenv.Base)
	return p.Git("-C", other, "rev-parse", "HEAD")
}

// rewarmReplies are the fake's replies for the move and re-warm of one slot: `unity cache key`
// before and after the checkout, the second with the key replaced by after when it is not empty,
// then open, `status --until-ready`, save_all and close.
func rewarmReplies(t *testing.T, p harnessPool, after []string) []fake.Reply {
	t.Helper()
	return []fake.Reply{{Capture: "cache-key.json"}, {Capture: "cache-key.json", Replace: after},
		openReply(t, p.Env, "open-headless.json"), statusReady, saveAllReply, closeHeadlessReply}
}

// rewarmProcs is the process table of a re-warm: upool itself, the owner of the warming slot,
// alive throughout, and the Editor it launches, capturedPID, running for its first starts start
// checks and then gone, as once `unity close` has returned. during, when not nil, runs at the
// first check of the Editor, while the slot is warming and its Editor starting.
type rewarmProcs struct {
	*dyingStart
	during func()
	fired  atomic.Bool
}

func newRewarmProcs(starts int, during func()) *rewarmProcs {
	return &rewarmProcs{dyingStart: &dyingStart{editorProcs: procsAlive(os.Getpid(), capturedPID),
		pid: capturedPID, starts: starts}, during: during}
}

func (r *rewarmProcs) Start(pid int) (time.Time, error) {
	if pid == capturedPID && r.during != nil && r.fired.CompareAndSwap(false, true) {
		r.during()
	}
	return r.dyingStart.Start(pid)
}

// runRefresh runs `upool refresh args…` in p's main checkout with procs as the process table.
func runRefresh(t *testing.T, p harnessPool, procs proc.Checker, args ...string) result {
	t.Helper()
	return runProcs(t, p.Env, p.Repo, procs, func() time.Time { return clock },
		append([]string{"refresh"}, args...)...)
}

// checkSlot1 fails the test unless slot-1 is ready, held by no owner, with HEAD at commit and
// reimport_pending as pending.
func checkSlot1(t *testing.T, p harnessPool, commit string, pending bool) {
	t.Helper()
	s := p.loadPool(t).Slots[0]
	if s.State != pool.StateReady || s.OwnerPID != nil || s.OwnerPIDStart != nil {
		t.Errorf("slot-1 = %s, owner %v; want ready and no owner", s.State, s.OwnerPID)
	}
	if s.ReimportPending != pending {
		t.Errorf("reimport_pending = %t, want %t", s.ReimportPending, pending)
	}
	if head := p.inSlot("rev-parse", "HEAD"); head != commit {
		t.Errorf("slot-1 HEAD = %s, want %s", head, commit)
	}
}

// The acceptance test of #97. A ready slot one commit behind origin/main ends ready at origin/main,
// with the fake's log showing, for it, the cache key compared across the checkout, then open with
// `--args -batchmode`, `status --until-ready` for the cold-import timeout, which a re-warm always
// waits (A40), save_all and close, each on slot-1. While it is
// warming, a take run as the Editor starts skips it. A dead owner's clean warming slot returns to
// ready at the next take, status or doctor, and a later refresh moves it to base.
func TestRefreshFetch(t *testing.T) {
	t.Run("moved and re-warmed", func(t *testing.T) {
		p, origin := fetchPool(t)
		upstream := pushUpstream(t, p, origin)
		u := p.installUnity(t, rewarmReplies(t, p, nil)...)
		var during result
		procs := newRewarmProcs(1, func() {
			during = runProcs(t, p.Env, p.Repo, procsAlive(os.Getpid(), capturedPID),
				func() time.Time { return clock }, "take", "feature-y", "--no-create")
		})
		r := runRefresh(t, p, procs, "--fetch")

		want := "the pool has 1 ready, as ready = 1 asks\nmoved slot-1 to origin/main (" +
			upstream[:7] + ") and re-warmed it in 0.0 s\n"
		if r.code != output.ExitOK || r.stdout != want {
			t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr,
				want)
		}
		progress := "upool: moving slot-1 to origin/main (" + upstream[:7] + ") and re-warming it\n"
		if !strings.HasPrefix(r.stderr, progress) {
			t.Errorf("stderr = %q, want it to start %q", r.stderr, progress)
		}
		slot := p.slot("slot-1")
		wantCalls := [][]string{{"cache", "key", slot, "--format", "json"},
			{"cache", "key", slot, "--format", "json"},
			{"open", slot, "--args", "-batchmode", "--format", "json"},
			{"status", "--until-ready", "--timeout", "300", "--format", "json"},
			{"command", "save_all", "--format", "json"},
			{"close", slot, "--format", "json"}}
		if got := callArgs(t, p, u); !slices.EqualFunc(got, wantCalls, slices.Equal) {
			t.Errorf("unity calls = %q, want the cache key twice, then open, status, save_all "+
				"and close", got)
		}
		checkSlot1(t, p, upstream, false)
		checkEditors(t, p)

		wantTake := "upool: skipping slot-1: warming\nupool: skipping slot-2: leased\nupool: no " +
			"slot of " + p.Repo + " is ready\n"
		if during.code != output.ExitFailure || during.stderr != wantTake {
			t.Errorf("take while slot-1 was warming: exit %d, stderr %q; want 1 and %q",
				during.code, during.stderr, wantTake)
		}
	})

	t.Run("a dead owner's clean warming slot", func(t *testing.T) {
		for _, command := range [][]string{{"take", "feature-y", "--no-create"}, {"status"},
			{"doctor"}} {
			t.Run(command[0], func(t *testing.T) {
				p, origin := fetchPool(t)
				upstream := pushUpstream(t, p, origin)
				owner, start := 4100, time.Unix(1, 0)
				p.update(t, func(pl *pool.Pool) {
					s := &pl.Slots[0]
					s.State, s.OwnerPID, s.OwnerPIDStart = pool.StateWarming, &owner, &start
				})
				// Every process is gone but upool itself.
				r := runProcs(t, p.Env, p.Repo, procsAlive(os.Getpid()),
					func() time.Time { return clock }, command...)
				want := "returned slot-1, which was warming, to ready, since its owner, " +
					"process 4100, is gone\n"
				if !strings.Contains(r.stderr, want) {
					t.Errorf("stderr = %q, want it to say %q", r.stderr, want)
				}
				if command[0] == "take" {
					// Back to ready, slot-1 is the slot take hands out.
					if r.code != output.ExitOK || r.stdout != p.slot("slot-1")+"\n" {
						t.Errorf("take: exit %d, stdout %q; want 0 and slot-1", r.code, r.stdout)
					}
					return
				}
				checkSlot1(t, p, p.BaseCommit, false)

				u := p.installUnity(t, rewarmReplies(t, p, nil)...)
				r = runRefresh(t, p, newRewarmProcs(1, nil), "--fetch")
				if r.code != output.ExitOK || !strings.Contains(r.stdout, "moved slot-1") {
					t.Errorf("refresh: exit %d, stdout %q, stderr %q; want slot-1 moved",
						r.code, r.stdout, r.stderr)
				}
				checkSlot1(t, p, upstream, false)
				if len(u.Calls()) != 6 {
					t.Errorf("unity calls = %q, want the move and re-warm of slot-1",
						callArgs(t, p, u))
				}
			})
		}
	})

	t.Run("a checkout that changed the cache key", func(t *testing.T) {
		p, origin := fetchPool(t)
		upstream := pushUpstream(t, p, origin)
		const key = "0cce5c29ab901534b6575c7da8c743206ba94cf3ff8898d5f290334719897bf7"
		u := p.installUnity(t, rewarmReplies(t, p, []string{key, strings.Repeat("ef", 32)})...)
		// The ledger's charge while the Editor starts, and the slot's reimport_pending.
		var charged []ledger.Editor
		var pending bool
		procs := newRewarmProcs(2, func() {
			charged = ledgerOf(t, p).Editors
			pending = p.loadPool(t).Slots[0].ReimportPending
		})
		r := runRefresh(t, p, procs, "--fetch")
		if r.code != output.ExitOK || !strings.Contains(r.stdout, "re-warmed it") {
			t.Errorf("exit %d, stdout %q, stderr %q; want slot-1 re-warmed", r.code, r.stdout,
				r.stderr)
		}
		warning := "upool: warning: slot-1's unity cache key changed across its checkout: its " +
			"next open is a large reimport\n"
		if !strings.Contains(r.stderr, warning) {
			t.Errorf("stderr = %q, want it to say %q", r.stderr, warning)
		}
		if len(charged) != 1 || !charged[0].ColdImport || charged[0].ChargeGB != ledger.ColdImportGB {
			t.Errorf("the ledger while the Editor started = %+v, want slot-1's Editor charged "+
				"as a cold import, %v GB", charged, ledger.ColdImportGB)
		}
		if !pending {
			t.Error("reimport_pending was not set while the Editor started")
		}
		if calls := callArgs(t, p, u); len(calls) != 6 {
			t.Errorf("unity calls = %q, want the move and re-warm of slot-1", calls)
		}
		checkSlot1(t, p, upstream, false)
		checkEditors(t, p)
	})
}

// A slot already at base is left alone, with no Unity call: the top-up the session-start hook
// spawns after each handout opens no Editor while the pool's base stands still.
func TestRefreshLeavesASlotAtBaseAlone(t *testing.T) {
	for _, args := range [][]string{nil, {"--fetch"}} {
		t.Run(strings.Join(append([]string{"refresh"}, args...), " "), func(t *testing.T) {
			p, _ := fetchPool(t)
			u := p.installUnity(t)
			r := runRefresh(t, p, newRewarmProcs(1, nil), args...)
			if want := "the pool has 1 ready, as ready = 1 asks\n"; r.code != output.ExitOK ||
				r.stdout != want || r.stderr != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want 0, %q and nothing", r.code,
					r.stdout, r.stderr, want)
			}
			if calls := u.Calls(); len(calls) != 0 {
				t.Errorf("unity calls = %+v, want none", calls)
			}
			checkSlot1(t, p, p.BaseCommit, false)
		})
	}
}

// Without --fetch, base is the pool's base as the main checkout has it: a slot behind a commit
// made there is moved to it, and no fetch is needed.
func TestRefreshMovesToTheLocalBase(t *testing.T) {
	p, _ := fetchPool(t)
	p.WriteFile(testenv.PlayerScript, "// moved on locally\n")
	p.Git("commit", "-q", "-m", "a script change", "--", testenv.PlayerScript)
	commit := p.Git("rev-parse", "HEAD")
	p.installUnity(t, rewarmReplies(t, p, nil)...)
	r := runRefresh(t, p, newRewarmProcs(1, nil))
	want := "the pool has 1 ready, as ready = 1 asks\nmoved slot-1 to main (" + commit[:7] +
		") and re-warmed it in 0.0 s\n"
	if r.code != output.ExitOK || r.stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr, want)
	}
	checkSlot1(t, p, commit, false)
}

// A re-warm that fails is a warning, and the moved slot goes back to ready with reimport_pending
// set, so that its next open is charged as the cold import the re-warm did not finish (A40). An
// Editor still running when the wait ran out is closed without save_all and with --force, which
// an Editor in the middle of a cold import needs (#224), and the instance files it was terminated
// with go once `unity status` lists none (A45); one that exited is already out of the ledger. If
// the close fails, the Editor stays in the ledger, its files and the slot warming.
func TestRefreshRewarmFailures(t *testing.T) {
	notReady := "the Editor of %s was not ready after 5m0s: "
	for _, tt := range []struct {
		name string
		// close is the fake's reply to `unity close`, if refresh is to close the Editor.
		close *fake.Reply
		// exits is whether the Editor exits before it is ready.
		exits bool
		// warning is how the warning starts after "moved slot-1 to origin/main but did not
		// re-warm it: ", with %s the slot.
		warning string
		closed  bool
	}{
		{"not ready, closed", &closeForcedReply, false, notReady, true},
		{"not ready, the close failed", &closeUnreachableReply, false, notReady, false},
		{"exited before ready", nil, true, "the Editor of %s, process 25147, exited before " +
			"it was ready", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, origin := fetchPool(t)
			upstream := pushUpstream(t, p, origin)
			replies := rewarmReplies(t, p, nil)[:3]
			replies = append(replies, statusNoInstances)
			forced := tt.close == &closeForcedReply
			if tt.close != nil {
				replies = append(replies, *tt.close)
			}
			if forced {
				replies = append(replies, noEditorsReply)
			}
			slotDir := p.slot("slot-1")
			leftovers := []string{"Library/EditorInstance.json", "Library/ProtocolInstance.json",
				"Temp/UnityLockfile"}
			for _, name := range leftovers {
				file := filepath.Join(slotDir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				content := fmt.Sprintf(`{"process_id": %d}`, capturedPID)
				if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			u := p.installUnity(t, replies...)
			procs := newRewarmProcs(1, nil)
			if tt.exits {
				procs.checks[capturedPID] = 0
			}
			r := runRefresh(t, p, procs, "--fetch")
			slot := p.slot("slot-1")
			warning := "upool: warning: moved slot-1 to origin/main but did not re-warm it: " +
				fmt.Sprintf(tt.warning, slot)
			if r.code != output.ExitOK || !strings.Contains(r.stderr, warning) {
				t.Errorf("exit %d, stderr %q; want 0 and a warning starting %q", r.code, r.stderr,
					warning)
			}
			want := [][]string{{"cache", "key", slot, "--format", "json"},
				{"cache", "key", slot, "--format", "json"},
				{"open", slot, "--args", "-batchmode", "--format", "json"},
				{"status", "--until-ready", "--timeout", "300", "--format", "json"}}
			if tt.close != nil {
				want = append(want, []string{"close", slot, "--force", "--format", "json"})
			}
			if forced {
				want = append(want, []string{"status", "--format", "json"})
			}
			if got := callArgs(t, p, u); !slices.EqualFunc(got, want, slices.Equal) {
				t.Errorf("unity calls = %q, want %q: no save_all", got, want)
			}
			for _, name := range leftovers {
				_, err := os.Stat(filepath.Join(slot, filepath.FromSlash(name)))
				if gone := err != nil; gone != forced {
					t.Errorf("slot-1/%s: %v; want it gone: %t", name, err, forced)
				}
			}
			if !tt.closed {
				if s := p.loadPool(t).Slots[0]; s.State != pool.StateWarming || !s.ReimportPending {
					t.Errorf("slot-1 = %s, reimport_pending %t; want warming while its Editor "+
						"runs, and pending", s.State, s.ReimportPending)
				}
				checkEditors(t, p, ledger.Editor{Project: slot, PID: capturedPID,
					Mode: modeHeadless, OpenedByUpool: true, ChargeGB: 2.5})
				if strings.Contains(r.stdout, "moved") {
					t.Errorf("stdout = %q, want no move reported", r.stdout)
				}
				// Once refresh and the Editor are gone, transition 22 returns the slot, still
				// pending.
				runProcs(t, p.Env, p.Repo, procsAlive(), func() time.Time { return clock },
					"status")
				checkSlot1(t, p, upstream, true)
				return
			}
			wantOut := "the pool has 1 ready, as ready = 1 asks\nmoved slot-1 to origin/main (" +
				upstream[:7] + ") but did not re-warm it\n"
			if r.stdout != wantOut {
				t.Errorf("stdout = %q, want %q", r.stdout, wantOut)
			}
			checkSlot1(t, p, upstream, true)
			checkEditors(t, p)
		})
	}
}

// --fetch with no origin to fetch from fails, moving nothing, and the refresh job is cleared.
func TestRefreshFetchFailsWithoutAnOrigin(t *testing.T) {
	p, _ := fetchPool(t)
	p.Git("remote", "remove", "origin")
	u := p.installUnity(t)
	r := runRefresh(t, p, newRewarmProcs(1, nil), "--fetch")
	want := "upool: fetching main from origin: "
	if r.code != output.ExitFailure || !strings.HasPrefix(r.stderr, want) {
		t.Errorf("exit %d, stderr %q; want 1 and %q…", r.code, r.stderr, want)
	}
	if calls := u.Calls(); len(calls) != 0 {
		t.Errorf("unity calls = %+v, want none", calls)
	}
	checkSlot1(t, p, p.BaseCommit, false)
	if job := p.loadPool(t).RefreshJob; job != nil {
		t.Errorf("refresh_job = %+v, want it cleared", job)
	}
}

// A slot whose checkout fails is not moved: a warning says so, stdout and the JSON list no move,
// and the slot is ready where it was. It is tried once, so the refresh ends.
func TestRefreshDoesNotCountAFailedCheckoutAsMoved(t *testing.T) {
	p, origin := fetchPool(t)
	pushUpstream(t, p, origin)
	p.WriteFile(".git/worktrees/slot-1/index.lock", "")
	u := p.installUnity(t, fake.Reply{Capture: "cache-key.json"})
	r := runRefresh(t, p, newRewarmProcs(1, nil), "--fetch", "--json")
	env := decode(t, r.stdout)
	data, _ := env.Data.(map[string]any)
	if r.code != output.ExitOK || data == nil || len(data["moved"].([]any)) != 0 {
		t.Errorf("exit %d, stdout %q; want 0 and nothing moved", r.code, r.stdout)
	}
	warning := "did not move slot-1 to origin/main: "
	if !slices.ContainsFunc(env.Warnings, func(w string) bool {
		return strings.HasPrefix(w, warning)
	}) {
		t.Errorf("warnings = %q, want one starting %q", env.Warnings, warning)
	}
	if calls := u.Calls(); len(calls) != 1 {
		t.Errorf("unity calls = %+v, want only the cache key before the checkout", calls)
	}
	checkSlot1(t, p, p.BaseCommit, false)
}

// A slot handed out while a move runs leaves its top-up to the running refresh, the pool's
// refresh_job, so the top-up runs again once the moves are done: with ready = 2 and max_slots = 2,
// a take of slot-2 during slot-1's re-warm leaves one ready slot, and the second top-up stops at
// max_slots rather than at ready.
func TestRefreshTopsUpAgainAfterTheMoves(t *testing.T) {
	p := warmPool(t)
	origin := tempDir(t)
	p.Git("init", "-q", "--bare", "-b", testenv.Base, origin)
	p.Git("remote", "add", "origin", origin)
	p.Git("push", "-q", "origin", testenv.Base)
	upstream := pushUpstream(t, p, origin)
	p.Setenv(config.EnvName("ready"), "2")
	p.Setenv(config.EnvName("max_slots"), "2")
	p.installUnity(t, rewarmReplies(t, p, nil)...)
	var during result
	procs := newRewarmProcs(1, func() {
		during = runProcs(t, p.Env, p.Repo, procsAlive(os.Getpid(), capturedPID),
			func() time.Time { return clock }, "take", "feature-z", "--no-create")
	})
	r := runRefresh(t, p, procs, "--fetch")
	if during.code != output.ExitOK || during.stdout != p.slot("slot-2")+"\n" {
		t.Fatalf("take during the re-warm: exit %d, stdout %q, stderr %q; want slot-2",
			during.code, during.stdout, during.stderr)
	}
	want := "the pool has 2 slots, its max_slots\nmoved slot-1 to origin/main (" + upstream[:7] +
		") and re-warmed it in 0.0 s\n"
	if r.code != output.ExitOK || r.stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and %q", r.code, r.stdout, r.stderr, want)
	}
}

// A refresh moves a slot only forward (A41): a slot ahead of the pool's base, as `refresh --fetch`
// leaves one when the main checkout has not pulled, or on another line of history, is left where
// it is by the plain refresh the session-start hook spawns, with no Unity call.
func TestRefreshMovesASlotOnlyForward(t *testing.T) {
	for _, tt := range []struct {
		name string
		// at returns the commit to put slot-1 on, made in p.
		at func(t *testing.T, p harnessPool, origin string) string
	}{
		{"ahead of base", func(t *testing.T, p harnessPool, origin string) string {
			upstream := pushUpstream(t, p, origin)
			p.Git("fetch", "-q", "origin")
			return upstream
		}},
		{"on another line", func(t *testing.T, p harnessPool, _ string) string {
			p.inSlot("checkout", "-q", "-b", "elsewhere")
			p.WriteFile(".claude/worktrees/slot-1/"+testenv.GameScript, "// elsewhere\n")
			p.inSlot("commit", "-q", "-am", "a change on another line")
			commit := p.inSlot("rev-parse", "HEAD")
			p.inSlot("checkout", "-q", "--detach", commit)
			p.WriteFile(testenv.PlayerScript, "// moved on locally\n")
			p.Git("commit", "-q", "-m", "a script change", "--", testenv.PlayerScript)
			return commit
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, origin := fetchPool(t)
			commit := tt.at(t, p, origin)
			p.inSlot("checkout", "-q", "--detach", commit)
			u := p.installUnity(t)
			r := runRefresh(t, p, newRewarmProcs(1, nil))
			if want := "the pool has 1 ready, as ready = 1 asks\n"; r.code != output.ExitOK ||
				r.stdout != want || r.stderr != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want 0, %q and nothing", r.code,
					r.stdout, r.stderr, want)
			}
			if calls := u.Calls(); len(calls) != 0 {
				t.Errorf("unity calls = %+v, want none", calls)
			}
			checkSlot1(t, p, commit, false)
		})
	}
}
