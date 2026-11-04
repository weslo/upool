package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pool"
)

// The tests of the create hook's patience with its git call in the main checkout (#286): a git
// that macOS holds for its Documents prompt is waited for, saying so, and any other stall fails
// within the patience. Git is real but for the worktree list, which a stallingGit holds, and the
// unified log is the TCC captures. The waits pass on testing/synctest's clock.

// capturedUpool is the client the captured prompt asks about: the Homebrew upool the benchmark's
// create hook ran, by its resolved path (testdata/captures/tcc/documents-prompting.ndjson).
const capturedUpool = "/opt/homebrew/Cellar/upool/0.6.1/bin/upool"

// waitingLine is the line the hook writes while the captured prompt holds its git call.
const waitingLine = "upool: waiting: macOS is asking whether upool may access Documents; " +
	"answer the prompt\n"

// stallingGit is a git.Runner that runs real git, except that `git worktree list` first waits
// until release is closed, as a git macOS holds for a prompt does, or until its context ends,
// which ended records.
type stallingGit struct {
	release chan struct{}
	ended   *atomic.Bool
}

func newStallingGit() stallingGit {
	return stallingGit{release: make(chan struct{}), ended: new(atomic.Bool)}
}

func (s stallingGit) Run(ctx context.Context, dir string, env []string, args ...string) (git.Result,
	error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "list" {
		select {
		case <-s.release:
		case <-ctx.Done():
			s.ended.Store(true)
			return git.Result{}, ctx.Err()
		}
	}
	return git.Exec{}.Run(ctx, dir, env, args...)
}

// capturedLog is a tcc.Log that answers the query for prompts with the TCC capture prompts and
// the query for their results with results, or fails with err, and counts its queries.
type capturedLog struct {
	prompts, results string
	err              error
	queries          *atomic.Int32
}

func newCapturedLog(prompts, results string) capturedLog {
	return capturedLog{prompts: prompts, results: results, queries: new(atomic.Int32)}
}

func (l capturedLog) Show(_ context.Context, _ time.Duration, predicate string) ([]byte, error) {
	l.queries.Add(1)
	if l.err != nil {
		return nil, l.err
	}
	file := l.prompts
	if strings.Contains(predicate, "AUTHREQ_RESULT") {
		file = l.results
	}
	return os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "tcc", file))
}

// stalling returns h's environment with a stallingGit, which it also returns, and log as its
// unified log, the captured prompt's client as upool's own binary. Call it inside the bubble, whose
// channels the git uses, and leave h's own environment for the checks after it.
func (h harness) stalling(log capturedLog) (Env, stallingGit) {
	g := newStallingGit()
	env := h.env
	env.Git = git.Client{Runner: g, Env: h.e.Environ()}
	env.Consent, env.Self = log, capturedUpool
	return env, g
}

// The acceptance test of #286, a stall with a prompt pending: the captured prompt has no result
// yet, so the hook keeps waiting, writing the waiting line at once and every 5 s, and once the
// prompt is answered and git goes on, it hands out the slot as it would have.
func TestCreateWaitsThroughAPendingDocumentsPrompt(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	synctest.Test(t, func(t *testing.T) {
		env, g := h.stalling(newCapturedLog("documents-prompting.ndjson",
			"documents-result-pending.ndjson"))
		start := time.Now()
		go func() {
			time.Sleep(11 * time.Second)
			close(g.release)
		}()
		got, err := WorktreeCreate(context.Background(), env, req)
		if waited := time.Since(start); waited != 11*time.Second {
			t.Errorf("the hook waited %s, want 11s: until git went on", waited)
		}
		if err != nil || got != h.slotPath("slot-1") {
			t.Errorf("WorktreeCreate = %q, %v; want slot-1", got, err)
		}
	})
	if want := strings.Repeat(waitingLine, 2); h.stderr.String() != want {
		t.Errorf("stderr = %q, want the waiting line at 2 s and 7 s: %q", h.stderr, want)
	}
	h.checkHandedOut(t, "slot-1", req, false)
}

// A hook that reaches main logs its start line before its git call (#278), so the hook log shows
// a run the prompt holds there as started and not ended, and its end line, once git goes on, the
// slot it handed out.
func TestCreateWaitsWithItsStartLineLogged(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	var waiting []LogLine
	synctest.Test(t, func(t *testing.T) {
		env, g := h.stalling(newCapturedLog("documents-prompting.ndjson",
			"documents-result-pending.ndjson"))
		env.Log = StartLog(env, EventWorktreeCreate, parentPID, req)
		go func() {
			time.Sleep(7 * time.Second)
			lines, err := ReadLog(h.e.Home, h.key)
			if err != nil {
				t.Error(err)
			}
			waiting = lines
			close(g.release)
		}()
		_, err := WorktreeCreate(context.Background(), env, req)
		env.Log.End(err)
		if err != nil {
			t.Errorf("WorktreeCreate = %v, want no error", err)
		}
	})
	if len(waiting) != 1 || waiting[0].Phase != PhaseStart ||
		waiting[0].Event != EventWorktreeCreate {
		t.Errorf("log while waiting = %+v, want the start line alone", waiting)
	}
	lines := h.logOf(t)
	if len(lines) != 2 || lines[1].Phase != PhaseEnd || lines[1].Outcome != OutcomeSlot ||
		lines[1].Slot != "slot-1" {
		t.Errorf("log = %+v, want the start line, then the end line with slot-1", lines)
	}
}

// The acceptance test of #286, a stall with no prompt: with none logged, or with the only one
// answered, the hook ends git and fails at the patience's end, naming the folder and the likely
// causes, and hands out nothing.
func TestCreateFailsFastOnAStallWithNoPrompt(t *testing.T) {
	for name, results := range map[string]string{"no prompt logged": "",
		"the prompt answered": "documents-result-allowed.ndjson"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, true)
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			req := h.capture(t, "desktop-session-create.json")
			prompts := "documents-prompting.ndjson"
			if results == "" {
				// No event matched: the query for results printed only the count line.
				prompts = "documents-result-pending.ndjson"
			}
			var g stallingGit
			synctest.Test(t, func(t *testing.T) {
				var env Env
				env, g = h.stalling(newCapturedLog(prompts, results))
				start := time.Now()
				got, err := WorktreeCreate(context.Background(), env, req)
				if waited := time.Since(start); waited != 2*time.Second {
					t.Errorf("the hook waited %s, want 2s, its patience", waited)
				}
				want := "git -C " + h.e.Repo + " worktree list did not answer within 2s, and " +
					"macOS is not asking whether upool may access Documents: the likely causes " +
					"are upool denied the Documents folder in System Settings → Privacy & " +
					"Security → Files and Folders, a prompt for it left unanswered for over 60 " +
					"minutes, or a hung git"
				if got != "" || err == nil || err.Error() != want {
					t.Errorf("WorktreeCreate = %q, %v; want no path and %q", got, err, want)
				}
			})
			if !g.ended.Load() {
				t.Error("git's call was not ended")
			}
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing: no prompt to wait for", h.stderr)
			}
			if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
				t.Errorf("slot-1 = %s with lease %+v, want it ready and unleased", s.State, s.Lease)
			}
		})
	}
}

// A stall the log cannot explain, because it cannot be read, as from a standard account, still
// fails at the patience's end, ending git: but it cannot tell whether a prompt is open, so it says
// macOS may be asking, to answer the prompt if it is showing and run upool doctor, and only then
// the other causes, with no prompt left unanswered among them (decision on #286, 2026-11-04).
func TestCreateFailsFastOnAStallWithAnUnreadableLog(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	var g stallingGit
	synctest.Test(t, func(t *testing.T) {
		log := newCapturedLog("", "")
		log.err = errors.New("reading the TCC log with /usr/bin/log show: exit status 64")
		var env Env
		env, g = h.stalling(log)
		start := time.Now()
		got, err := WorktreeCreate(context.Background(), env, req)
		if waited := time.Since(start); waited != 2*time.Second {
			t.Errorf("the hook waited %s, want 2s", waited)
		}
		want := "git -C " + h.e.Repo + " worktree list did not answer within 2s, and upool " +
			"could not read macOS's unified log to tell why (it needs an admin account: " +
			"reading the TCC log with /usr/bin/log show: exit status 64): macOS may be asking " +
			"whether upool may access the Documents folder, so answer the prompt if it is " +
			"showing, then run upool doctor; otherwise the likely causes are upool denied the " +
			"Documents folder in System Settings → Privacy & Security → Files and Folders, or a " +
			"hung git"
		if got != "" || err == nil || err.Error() != want {
			t.Errorf("WorktreeCreate = %q, %v; want no path and %q", got, err, want)
		}
		if !strings.Contains(err.Error(), LogUnreadable) {
			t.Errorf("the error does not contain %q, which doctor looks for", LogUnreadable)
		}
	})
	if !g.ended.Load() {
		t.Error("git's call was not ended")
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: no prompt known to wait for", h.stderr)
	}
	if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %s with lease %+v, want it ready and unleased", s.State, s.Lease)
	}
}

// A prompt nobody answers holds the hook for the 4 min 55 s the desktop app leaves it, the
// waiting line every 5 s from the patience's end, and then the hook fails, saying so.
func TestCreateStopsWaitingForAPromptNobodyAnswers(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	synctest.Test(t, func(t *testing.T) {
		env, _ := h.stalling(newCapturedLog("documents-prompting.ndjson",
			"documents-result-pending.ndjson"))
		start := time.Now()
		_, err := WorktreeCreate(context.Background(), env, req)
		if waited := time.Since(start); waited != 295*time.Second {
			t.Errorf("the hook waited %s, want 4m55s", waited)
		}
		want := "macOS asked whether upool may access Documents, and the prompt was not " +
			"answered within 4m55s, the most the desktop app waits for a session to start: " +
			"answer it, then start the session again"
		if err == nil || err.Error() != want {
			t.Errorf("WorktreeCreate = %v; want %q", err, want)
		}
	})
	// At 2 s, then every 5 s until 292 s.
	if want := strings.Repeat(waitingLine, 59); h.stderr.String() != want {
		t.Errorf("stderr has %d waiting lines, want 59", strings.Count(h.stderr.String(),
			waitingLine))
	}
}

// A git that answers within the patience is unaffected: the hook reads no log, writes nothing on
// stderr, and takes no time on the bubble's clock.
func TestCreateWithAFastGitNeverWaits(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	log := newCapturedLog("documents-prompting.ndjson", "documents-result-pending.ndjson")
	synctest.Test(t, func(t *testing.T) {
		env := h.env
		env.Consent, env.Self = log, capturedUpool
		start := time.Now()
		got, err := WorktreeCreate(context.Background(), env, req)
		if waited := time.Since(start); waited != 0 {
			t.Errorf("the hook waited %s, want no time", waited)
		}
		if err != nil || got != h.slotPath("slot-1") {
			t.Errorf("WorktreeCreate = %q, %v; want slot-1", got, err)
		}
	})
	if n := log.queries.Load(); n != 0 {
		t.Errorf("the hook read the log %d times, want none", n)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", h.stderr)
	}
}

// The dry run that `upool doctor` sends through the desktop app's helper runs the same git call,
// with the same wait for a pending prompt, and then stops: no slot is taken.
func TestCreateDryRunWaitsLikeTheHookAndTakesNoSlot(t *testing.T) {
	h := newHarness(t, true)
	h.readySlot(t, "slot-1", h.slotPath("slot-1"))
	req := h.capture(t, "desktop-session-create.json")
	if err := WorktreeCreateDryRun(context.Background(), h.env, req); err != nil {
		t.Fatalf("WorktreeCreateDryRun = %v, want no error", err)
	}
	synctest.Test(t, func(t *testing.T) {
		env, g := h.stalling(newCapturedLog("documents-prompting.ndjson",
			"documents-result-pending.ndjson"))
		go func() {
			time.Sleep(3 * time.Second)
			close(g.release)
		}()
		if err := WorktreeCreateDryRun(context.Background(), env, req); err != nil {
			t.Errorf("WorktreeCreateDryRun = %v, want no error once git went on", err)
		}
	})
	if h.stderr.String() != waitingLine {
		t.Errorf("stderr = %q, want the waiting line once", h.stderr)
	}
	if s := h.slot(t, "slot-1"); s.State != pool.StateReady || s.Lease != nil {
		t.Errorf("slot-1 = %s with lease %+v, want it ready and unleased", s.State, s.Lease)
	}
	req.HookEventName = EventSessionStart
	if err := WorktreeCreateDryRun(context.Background(), h.env, req); err == nil {
		t.Error("WorktreeCreateDryRun of a SessionStart request succeeded, want it refused")
	}
}
