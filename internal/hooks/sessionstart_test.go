package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of the SessionStart hook that copy no agent file, so run everywhere; the startup
// confirms, which copy with cp -cR, are in sessionstart_darwin_test.go.

// startHarness is a harness whose hooks report this test process as their parent, a process
// internal/proc can read the start time of, and whose CLAUDE_ENV_FILE is where Claude Code puts
// it, for session, under the harness's home ((b), `CLAUDE_ENV_FILE`).
func startHarness(t *testing.T, register bool, session string) harness {
	t.Helper()
	h := newHarness(t, register)
	h.env.ParentPID = os.Getpid()
	dir := filepath.Join(h.e.Home, ".claude", "session-env", session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h.env.EnvFile = filepath.Join(dir, "sessionstart-hook-0.sh")
	return h
}

// readySlot adds a ready slot named name at path to h's pool: a worktree detached at base, as init
// and refresh leave one.
func (h harness) readySlot(t *testing.T, name, path string) {
	t.Helper()
	h.e.Git("worktree", "add", "-q", "--detach", path, testenv.Base)
	err := pool.Update(context.Background(), h.e.Home, h.key, func(p *pool.Pool) error {
		p.Slots = append(p.Slots, pool.Slot{Name: name, Path: path, State: pool.StateReady,
			SeedHead: strings.TrimSpace(h.e.Git("rev-parse", "HEAD")), LastUsed: before})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// hookLease gives the slot the pool hands out next the create hook's unconfirmed lease, as the
// pooled branch takes it, and returns that slot.
func (h harness) hookLease(t *testing.T) pool.Slot {
	t.Helper()
	s, _, err := pool.UnconfirmedTake{Holder: "49b55ea5-fc66-40a8-8f6c-b4afc31ec456",
		NameHint: "system-diagnostics-9f8a09", Now: clock, ConfirmTimeout: time.Minute}.Take(
		context.Background(), h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// start runs the hook on req and returns its error, checking what it printed: the context that
// gives a session in a slot, found as sessionSlot finds it, upool's instructions, and nothing for
// any other session, since
// Claude Code adds a SessionStart hook's stdout to the session's context (A43).
func (h harness) start(t *testing.T, req Request) error {
	t.Helper()
	got, err := SessionStart(context.Background(), h.env, req)
	_, inSlot, _, ferr := sessionSlot(h.env.Home, req.SessionID, req.Cwd)
	if ferr != nil {
		t.Fatal(ferr)
	}
	if inSlot && req.HookEventName == EventSessionStart && filepath.IsAbs(req.Cwd) {
		checkContext(t, got, EventSessionStart)
	} else if got != "" {
		t.Errorf("SessionStart = %q, want nothing for a session in no slot", got)
	}
	return err
}

// checkContext fails the test unless got is the one-line document that gives a session upool's
// instructions for event: Claude Code's hookSpecificOutput, with event as hookEventName and
// assets.Instructions as additionalContext ((b); A43).
func checkContext(t *testing.T, got, event string) {
	t.Helper()
	var doc struct {
		Out struct {
			Event   string `json:"hookEventName"`
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil || strings.Contains(got, "\n") ||
		doc.Out.Event != event || doc.Out.Context != assets.Instructions() {
		t.Errorf("context = %q (%v), want one line of hookSpecificOutput for %s holding "+
			"upool's instructions", got, err, event)
	}
}

// exports returns the lines of h's CLAUDE_ENV_FILE, and whether it exists.
func (h harness) exports(t *testing.T) ([]string, bool) {
	t.Helper()
	b, err := os.ReadFile(h.env.EnvFile)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), true
}

// checkExported fails the test unless h's CLAUDE_ENV_FILE holds exactly one line, the export of
// path as UNITY_PROJECT_PATH, and sourcing it in sh sets the variable to path.
func (h harness) checkExported(t *testing.T, path string) {
	t.Helper()
	lines, ok := h.exports(t)
	if want := "export UNITY_PROJECT_PATH='" + path + "'"; !ok || len(lines) != 1 ||
		lines[0] != want {
		t.Errorf("CLAUDE_ENV_FILE = %q, %t; want the one line %q", lines, ok, want)
	}
	out, err := exec.Command("sh", "-c", `. "$1" && printf %s "$UNITY_PROJECT_PATH"`, "sh",
		h.env.EnvFile).Output()
	if err != nil || string(out) != path {
		t.Errorf("sourcing CLAUDE_ENV_FILE sets UNITY_PROJECT_PATH to %q, %v; want %q", out, err,
			path)
	}
}

// stateFile returns what the file system says of h's state.json, for checking that a hook left
// it unwritten: every write replaces the file, by rename (internal/statefile).
func (h harness) stateFile(t *testing.T) os.FileInfo {
	t.Helper()
	info, err := os.Stat(filepath.Join(h.e.Home, ".upool", "pools", h.key, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// load returns h's pool as state.json records it.
func (h harness) load(t *testing.T) pool.Pool {
	t.Helper()
	p, err := pool.Load(h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A session that is in no slot is left alone, whether or not its repository is a pool: the
// desktop session without a worktree runs in the main checkout, and the CLI session in a worktree
// of a repository upool does not manage. Nothing is exported, and state.json is as it was.
func TestSessionStartOutsideASlotDoesNothing(t *testing.T) {
	for _, file := range []string{"desktop-no-worktree-session-start.json",
		"cli-session-start.json"} {
		for _, register := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s, registered %t", file, register), func(t *testing.T) {
				h := startHarness(t, register, "session")
				var before pool.Pool
				if register {
					h.leaseSlot(t, "slot-1", filepath.Join(h.e.Repo, ".claude", "worktrees",
						"slot-1"))
					before = h.load(t)
				}
				if err := h.start(t, h.capture(t, file)); err != nil {
					t.Errorf("SessionStart: %v, want no error", err)
				}
				if lines, ok := h.exports(t); ok {
					t.Errorf("CLAUDE_ENV_FILE = %q, want no file", lines)
				}
				if register && !reflect.DeepEqual(h.load(t), before) {
					t.Errorf("state.json = %+v, want it as it was: %+v", h.load(t), before)
				}
				if h.stderr.Len() != 0 {
					t.Errorf("stderr = %q, want nothing", h.stderr)
				}
			})
		}
	}
}

// On resume, clear and compact the hook exports the slot and records its parent as the lease's
// holder, since a resumed session may run in a new CLI process; it confirms nothing, re-copies no
// agent file, which would overwrite the session's edits, and runs no reset (A14). The captures are
// of sessions in pass-through worktrees, so their cwd is pointed at the slot here.
func TestSessionStartOtherSourcesExportAndRecordTheHolder(t *testing.T) {
	for _, file := range []string{"session-start-resume.json", "session-start-clear.json",
		"session-start-compact.json"} {
		t.Run(file, func(t *testing.T) {
			h := startHarness(t, true, "session")
			path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
			h.readySlot(t, "slot-1", path)
			s := h.hookLease(t)
			edited := filepath.Join(path, "CLAUDE.md")
			if err := os.WriteFile(edited, []byte("the session's own edit\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			req := h.capture(t, file)
			req.Cwd = path
			if err := h.start(t, req); err != nil {
				t.Fatalf("SessionStart: %v, want no error", err)
			}
			h.checkExported(t, path)
			got := h.slot(t, "slot-1")
			l := got.Lease
			if got.State != pool.StateLeasedUnconfirmed || l == nil || l.Confirmed ||
				l.LeaseID != s.Lease.LeaseID {
				t.Errorf("slot-1 = %+v, want its lease as it was, unconfirmed", got)
			}
			checkHolder(t, l, os.Getpid())
			b, err := os.ReadFile(edited)
			if err != nil || string(b) != "the session's own edit\n" {
				t.Errorf("CLAUDE.md = %q, %v; want the session's edit kept", b, err)
			}
			settings := filepath.Join(path, ".claude", "settings.local.json")
			if _, err := os.Lstat(settings); err == nil {
				t.Error("the main checkout's .claude/settings.local.json was copied in")
			}
		})
	}
}

// checkHolder fails the test unless l's holder is pid with a start time, where internal/proc can
// read one, and is unset elsewhere, as on Linux.
func checkHolder(t *testing.T, l *pool.Lease, pid int) {
	t.Helper()
	if l == nil {
		t.Fatal("no lease")
	}
	if runtime.GOOS != "darwin" {
		if l.HolderPID != nil {
			t.Errorf("holder = %d, want none where process start times are not read",
				*l.HolderPID)
		}
		return
	}
	if l.HolderPID == nil || *l.HolderPID != pid || l.HolderPIDStart == nil {
		t.Errorf("holder = %v, %v; want process %d with its start time", l.HolderPID,
			l.HolderPIDStart, pid)
	}
}

// A slot whose lease has ended still gets the export on resume, and state.json is not written:
// there is no lease to record a holder on.
func TestSessionStartResumeInAnUnleasedSlotOnlyExports(t *testing.T) {
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	before := h.stateFile(t)
	req := h.capture(t, "session-start-resume.json")
	req.Cwd = path
	if err := h.start(t, req); err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	h.checkExported(t, path)
	if !os.SameFile(h.stateFile(t), before) {
		t.Error("state.json was written, want it left alone")
	}
}

// A startup in a slot whose confirm fails, here because the slot holds no lease, still exports the
// slot, which is where the session works whatever its lease, and reports the failure; the hook
// exits 0 on it ((d)).
func TestSessionStartExportsWhenTheConfirmFails(t *testing.T) {
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	before := h.load(t)
	err := h.start(t, h.capture(t, "session-start.json"))
	if err == nil || !strings.Contains(err.Error(), "slot-1 is ready, not leased") {
		t.Errorf("SessionStart: %v, want the confirm's refusal", err)
	}
	h.checkExported(t, path)
	if !reflect.DeepEqual(h.load(t), before) {
		t.Errorf("state.json = %+v, want it as it was: %+v", h.load(t), before)
	}
}

// Without CLAUDE_ENV_FILE nothing can be exported, which the hook reports, and the rest of its
// work is still done.
func TestSessionStartWithoutAnEnvFileRecordsTheHolder(t *testing.T) {
	h := startHarness(t, true, "session")
	h.env.EnvFile = ""
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	h.hookLease(t)
	req := h.capture(t, "session-start-compact.json")
	req.Cwd = path
	err := h.start(t, req)
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_ENV_FILE is not set") {
		t.Errorf("SessionStart: %v, want it to say CLAUDE_ENV_FILE is not set", err)
	}
	checkHolder(t, h.slot(t, "slot-1").Lease, os.Getpid())
}

// The hook finds the slot by its canonical path, as the desktop app may hand it back lowercased,
// and exports the path state.json records.
func TestSessionStartFindsTheSlotByItsLowercasedPath(t *testing.T) {
	h := startHarness(t, true, "session")
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.readySlot(t, "slot-1", path)
	h.hookLease(t)
	if insensitive, err := pathsCaseInsensitive(path); err != nil || !insensitive {
		t.Skipf("%s is on a case-sensitive volume (%v)", path, err)
	}
	req := h.capture(t, "session-start-resume.json")
	req.Cwd = strings.ToLower(path)
	if err := h.start(t, req); err != nil {
		t.Fatalf("SessionStart: %v, want no error", err)
	}
	h.checkExported(t, path)
}

// pathsCaseInsensitive says whether path's volume treats names that differ only in case as one.
func pathsCaseInsensitive(path string) (bool, error) {
	upper, err := os.Stat(strings.ToUpper(path))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	actual, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return os.SameFile(upper, actual), nil
}

// A request the hook cannot use is refused, with nothing exported.
func TestSessionStartRefusesWhatItCannotUse(t *testing.T) {
	for name, edit := range map[string]func(*Request){
		"another event": func(r *Request) { r.HookEventName = EventWorktreeCreate },
		"relative cwd":  func(r *Request) { r.Cwd = "slot-1" },
	} {
		t.Run(name, func(t *testing.T) {
			h := startHarness(t, true, "session")
			req := h.capture(t, "session-start.json")
			edit(&req)
			if err := h.start(t, req); err == nil {
				t.Error("SessionStart: no error, want a refusal")
			}
			if lines, ok := h.exports(t); ok {
				t.Errorf("CLAUDE_ENV_FILE = %q, want no file", lines)
			}
		})
	}
}

// The export is appended after what the file holds, on a line of its own, and only once however
// often the hook runs for the session; a path with a single quote in it is quoted for sh.
func TestSessionStartExportsOnce(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sessionstart-hook-0.sh")
	if err := os.WriteFile(file, []byte("export OTHER=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(dir, "it's here", "slot-1")
	for range 2 {
		if err := exportProjectPath(file, slot); err != nil {
			t.Fatalf("exportProjectPath: %v", err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(slot, "'", `'\''`) + "'"
	if want := "export OTHER=1\nexport UNITY_PROJECT_PATH=" + quoted + "\n"; string(b) != want {
		t.Errorf("CLAUDE_ENV_FILE = %q, want %q", b, want)
	}
	out, err := exec.Command("sh", "-c", `. "$1" && printf %s "$UNITY_PROJECT_PATH"`, "sh",
		file).Output()
	if err != nil || string(out) != slot {
		t.Errorf("sourcing it sets UNITY_PROJECT_PATH to %q, %v; want %q", out, err, slot)
	}
}

// starts records the top-ups a hook started, standing in for the detached `upool refresh`.
type starts struct {
	calls []string
	err   error
}

// start records a top-up of the pool key in checkout, and fails with s.err.
func (s *starts) start(checkout, key string) error {
	s.calls = append(s.calls, checkout+" "+key)
	return s.err
}

// The top-up follows a handout, so it starts only after a startup's confirm: not on resume, not
// when the confirm fails, and not for a session in no slot (A14; #79).
func TestSessionStartSpawnsRefreshOnlyAfterAHandout(t *testing.T) {
	for name, run := range map[string]func(t *testing.T, h harness){
		"resume": func(t *testing.T, h harness) {
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			h.hookLease(t)
			req := h.capture(t, "session-start-resume.json")
			req.Cwd = h.slotPath("slot-1")
			if err := h.start(t, req); err != nil {
				t.Fatalf("SessionStart: %v", err)
			}
		},
		"failed confirm": func(t *testing.T, h harness) {
			h.readySlot(t, "slot-1", h.slotPath("slot-1"))
			if err := h.start(t, h.capture(t, "session-start.json")); err == nil {
				t.Fatal("SessionStart: no error, want the confirm's refusal")
			}
		},
		"no slot": func(t *testing.T, h harness) {
			req := h.capture(t, "desktop-no-worktree-session-start.json")
			if err := h.start(t, req); err != nil {
				t.Fatalf("SessionStart: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := startHarness(t, true, "session")
			var s starts
			h.env.StartRefresh = s.start
			run(t, h)
			if len(s.calls) != 0 {
				t.Errorf("top-ups started = %q, want none", s.calls)
			}
		})
	}
}

// A session in a slot gets upool's instructions as context on each start that does not hand the
// slot out, resume, clear and compact, as well as on startup (A43; question 43). The captures are
// of sessions in pass-through worktrees, so their cwd is pointed at the slot here.
func TestSessionStartGivesTheInstructions(t *testing.T) {
	for _, file := range []string{"session-start-resume.json", "session-start-clear.json",
		"session-start-compact.json"} {
		t.Run(file, func(t *testing.T) {
			h := startHarness(t, true, "session")
			path := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
			h.readySlot(t, "slot-1", path)
			h.hookLease(t)
			req := h.capture(t, file)
			req.Cwd = path
			got, err := SessionStart(context.Background(), h.env, req)
			if err != nil {
				t.Fatalf("SessionStart: %v, want no error", err)
			}
			checkContext(t, got, EventSessionStart)
		})
	}
}

// A `claude -p --resume` launched from the main checkout runs SessionStart with the main checkout
// as its cwd, so the hook cannot tell that the session is in a slot: it gives no context and
// exports nothing. The resumed session keeps the context its first start gave ((b); question 43).
func TestSessionStartResumedFromTheMainCheckoutGivesNothing(t *testing.T) {
	h := startHarness(t, true, "session")
	h.readySlot(t, "slot-1", filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1"))
	req := h.capture(t, "cli-resume-checkout-session-start.json")
	if req.Cwd != h.e.Repo || req.Source != "resume" {
		t.Fatalf("capture cwd %q, source %q; want the main checkout %s and resume", req.Cwd,
			req.Source, h.e.Repo)
	}
	got, err := SessionStart(context.Background(), h.env, req)
	if err != nil || got != "" {
		t.Errorf("SessionStart = %q, %v; want nothing and no error", got, err)
	}
	if lines, ok := h.exports(t); ok {
		t.Errorf("CLAUDE_ENV_FILE = %q, want nothing exported", lines)
	}
}

// When the startup confirm fails, the session still gets its instructions, with the confirm's
// error (A43).
func TestSessionStartGivesTheInstructionsWhenTheConfirmFails(t *testing.T) {
	h := startHarness(t, true, "session")
	h.readySlot(t, "slot-1", filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1"))
	got, err := SessionStart(context.Background(), h.env, h.capture(t, "session-start.json"))
	if err == nil {
		t.Error("SessionStart: no error, want the confirm's refusal")
	}
	checkContext(t, got, EventSessionStart)
}
