package hooks

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// capturesDir holds the hook stdin captures (testdata/README.md, captures/hooks/).
var capturesDir = filepath.Join("..", "..", "testdata", "captures", "hooks")

// clock is the time the tests' hooks run at.
var clock = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// parentPID is the parent the tests' hooks report.
const parentPID = 4242

// eventSessionEnd is SessionEnd's hook_event_name, whose captures #148 adds and whose hook #80
// builds.
const eventSessionEnd = "SessionEnd"

// logCaptures are the files in capturesDir that are not hook stdin, by name: a hook's own log,
// which the classification passes over (testdata/README.md, captures/hooks/).
var logCaptures = map[string]bool{"desktop-create-timeout.txt": true}

// Every capture's name says what it is, and decoding it says the same: its event; for a
// WorktreeCreate, who sent it (docs/contracts.md (a), "Telling the kinds apart"); and for a
// WorktreeRemove, desktop or CLI by its transcript_path. testdata/README.md: this test runs over
// every file but the logs.
func TestClassifyEveryCapture(t *testing.T) {
	entries, err := os.ReadDir(capturesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s holds no captures", capturesDir)
	}
	for _, entry := range entries {
		if logCaptures[entry.Name()] {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if name == entry.Name() {
			t.Errorf("%s is not a .json capture", entry.Name())
			continue
		}
		f, err := os.Open(filepath.Join(capturesDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		req, err := Decode(f)
		_ = f.Close() // read-only
		if err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
			continue
		}

		var event string
		switch {
		case strings.Contains(name, "-create"):
			event = EventWorktreeCreate
		case strings.Contains(name, "-remove"):
			event = EventWorktreeRemove
		case strings.HasSuffix(name, "-start") || strings.HasPrefix(name, "session-start"):
			event = EventSessionStart
		case strings.HasSuffix(name, "-end"):
			event = eventSessionEnd
		default:
			t.Errorf("%s: its name says no event", entry.Name())
			continue
		}
		var kind Kind
		switch {
		case strings.HasPrefix(name, "subagent-"):
			kind = Subagent
		case strings.HasPrefix(name, "cli-"):
			kind = CLISession
		case strings.HasPrefix(name, "desktop-"), strings.HasPrefix(name, "sibling-"),
			strings.HasPrefix(name, "session-start"):
			kind = DesktopSession
		default:
			t.Errorf("%s: its name says no kind of request", entry.Name())
			continue
		}

		if req.HookEventName != event {
			t.Errorf("%s: hook_event_name = %q, want %q", entry.Name(), req.HookEventName, event)
		}
		switch event {
		case EventWorktreeCreate:
			if got := req.Kind(); got != kind {
				t.Errorf("%s: kind = %s, want %s", entry.Name(), got, kind)
			} else if kind == Subagent && !req.Corroborated() {
				t.Errorf("%s: a subagent's request without scratchpad_dir or an agent-<17 hex> "+
					"name: %+v", entry.Name(), req)
			}
		case EventWorktreeRemove:
			// A remove comes from a session, never a subagent (spike §9 F.3), and the CLI's
			// carries a prompt_id, so only transcript_path tells desktop from CLI.
			if desktop := req.TranscriptPath == ""; desktop != (kind == DesktopSession) {
				t.Errorf("%s: transcript_path %q, want a %s's", entry.Name(),
					req.TranscriptPath, kind)
			}
		}
		// SessionStart and SessionEnd run in the CLI process, so transcript_path is set whoever
		// the session is ((b)), and they carry a prompt_id after a prompt: their names say only
		// the event.
	}
}

// harness is a fake project registered as a pool, its state.json made as init leaves it.
type harness struct {
	e   *testenv.Env
	key string
	env Env
	// stderr is what the hooks warned.
	stderr *bytes.Buffer
}

// newHarness makes a harness; with register false the project is not a pool.
func newHarness(t *testing.T, register bool) harness {
	t.Helper()
	e := testenv.New(t)
	var stderr bytes.Buffer
	h := harness{e: e, stderr: &stderr, env: Env{Git: git.Client{Runner: git.Exec{},
		Env: e.Environ()}, Home: e.Home, ParentPID: parentPID,
		Now: func() time.Time { return clock }, Stderr: &stderr}}
	if register {
		repo, err := registry.Add(context.Background(), e.Home, e.Repo)
		if err != nil {
			t.Fatal(err)
		}
		h.key = repo.Key
		err = pool.Update(context.Background(), e.Home, repo.Key, func(p *pool.Pool) error {
			p.Repo, p.Base = e.Repo, testenv.Base
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// capture decodes the hook capture file, its paths filled with h's.
func (h harness) capture(t *testing.T, file string) Request {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(capturesDir, file))
	if err != nil {
		t.Fatal(err)
	}
	text, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: h.e.Home},
		pathvars.Var{Name: "repo", Path: h.e.Repo},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(h.e.Home, "claude-tmp")})
	if err != nil {
		t.Fatalf("filling %s: %v", file, err)
	}
	req, err := Decode(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// worktree returns git's entry for the worktree at path in h's repository, and whether it has one.
func (h harness) worktree(t *testing.T, path string) (git.Worktree, bool) {
	t.Helper()
	wts, err := h.env.Git.WorktreeList(context.Background(), h.e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range wts {
		if wt.Path == path {
			return wt, true
		}
	}
	return git.Worktree{}, false
}

// The acceptance test of #71: each captured subagent request, filled with the harness's paths,
// gets an ordinary worktree at <repo>/.claude/worktrees/<name>, detached at the repository's HEAD,
// whose path is what the hook returns for the dispatcher to print, and the pool's state.json
// records it with its parent session and process.
func TestSubagentGetsDetachedWorktree(t *testing.T) {
	h := newHarness(t, true)
	head := strings.TrimSpace(h.e.Git("rev-parse", "HEAD"))
	var want []pool.AgentWorktree
	for _, file := range []string{"subagent-create.json", "subagent-create-2.json"} {
		req := h.capture(t, file)
		path, err := WorktreeCreate(context.Background(), h.env, req)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		wantPath := filepath.Join(h.e.Repo, ".claude", "worktrees", req.Name)
		if path != wantPath {
			t.Errorf("%s: path = %q, want %q", file, path, wantPath)
		}
		wt, ok := h.worktree(t, wantPath)
		if !ok || !wt.Detached || wt.HEAD != head {
			t.Errorf("%s: git lists %+v, %t; want a worktree detached at %s", file, wt, ok, head)
		}
		version := filepath.Join(wantPath, "ProjectSettings", "ProjectVersion.txt")
		if _, err := os.Stat(version); err != nil {
			t.Errorf("%s: the worktree is not checked out: %v", file, err)
		}
		want = append(want, pool.AgentWorktree{Path: wantPath, ParentSessionID: req.SessionID,
			ParentPID: parentPID, CreatedAt: clock})
	}
	p, err := pool.Load(h.e.Home, h.key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AgentWorktrees) != len(want) {
		t.Fatalf("agent_worktrees = %+v, want %+v", p.AgentWorktrees, want)
	}
	for i := range want {
		if got := p.AgentWorktrees[i]; got != want[i] || !got.CreatedAt.Equal(clock) {
			t.Errorf("agent_worktrees[%d] = %+v, want %+v", i, got, want[i])
		}
	}
	if len(p.Slots) != 0 {
		t.Errorf("slots = %+v, want none: a subagent never gets one", p.Slots)
	}
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing for a corroborated request", h.stderr)
	}
}

// A subagent of a repository that is not a pool still gets its worktree, and nothing is recorded.
func TestSubagentOutsideAPoolIsNotRecorded(t *testing.T) {
	h := newHarness(t, false)
	req := h.capture(t, "subagent-create.json")
	path, err := WorktreeCreate(context.Background(), h.env, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.worktree(t, path); !ok {
		t.Errorf("git does not list %s", path)
	}
	if _, err := os.Stat(filepath.Join(h.e.Home, ".upool")); err == nil {
		t.Error("~/.upool exists, want nothing recorded outside a pool")
	}
}

// The acceptance test of #72, its first half: a session in a repository upool does not manage
// gets the worktree Claude Code would have made, at <repo>/.claude/worktrees/<name> on a branch
// named <name>, whichever kind of session asks (docs/contracts.md (a); the three fixtures).
func TestPassThroughForUnregisteredRepo(t *testing.T) {
	for _, file := range []string{"desktop-session-create.json", "cli-session-create.json",
		"desktop-probe-create.json"} {
		t.Run(file, func(t *testing.T) {
			h := newHarness(t, false)
			req := h.capture(t, file)
			path, err := WorktreeCreate(context.Background(), h.env, req)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(h.e.Repo, ".claude", "worktrees", req.Name)
			if path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			wt, ok := h.worktree(t, want)
			if !ok || wt.Branch != "refs/heads/"+req.Name {
				t.Errorf("git lists %+v, %t; want %s on branch %s", wt, ok, want, req.Name)
			}
			if h.stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", h.stderr)
			}
		})
	}
}

// The pass-through branch refuses a name that is not one path element, before git runs.
func TestPassThroughRefusesABadName(t *testing.T) {
	h := newHarness(t, false)
	for _, name := range []string{"", "..", "../../escape"} {
		req := h.capture(t, "desktop-session-create.json")
		req.Name = name
		if path, err := WorktreeCreate(context.Background(), h.env, req); err == nil {
			t.Errorf("name %q: path = %q, want an error", name, path)
		}
	}
	wts, err := h.env.Git.WorktreeList(context.Background(), h.e.Repo)
	if err != nil || len(wts) != 1 {
		t.Errorf("git worktree list = %+v, %v; want the main checkout alone", wts, err)
	}
}

// The acceptance test of #72, its second half: a session whose cwd is a registered main checkout,
// or a directory in one, is the pool's (A13), and is refused, with nothing made, until the pooled
// branch is built (#78); one in a pool's slot is not the main checkout, and passes through.
func TestRegisteredRepoRoutesToPool(t *testing.T) {
	h := newHarness(t, true)
	for _, dir := range []string{h.e.Repo, filepath.Join(h.e.Repo, "Assets")} {
		req := h.capture(t, "desktop-session-create.json")
		req.Cwd = dir
		_, err := WorktreeCreate(context.Background(), h.env, req)
		want := "is in pool " + h.key + ", and handing a session one of its slots is not built yet"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("from %s: err = %v, want it to say %q", dir, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(h.e.Repo, ".claude", "worktrees")); err == nil {
		t.Error(".claude/worktrees exists, want nothing made for a pooled session")
	}

	slot := filepath.Join(h.e.Repo, ".claude", "worktrees", "slot-1")
	h.e.Git("worktree", "add", "-q", "--detach", slot)
	req := h.capture(t, "desktop-session-create.json")
	req.Cwd = slot
	path, err := WorktreeCreate(context.Background(), h.env, req)
	if want := filepath.Join(slot, ".claude", "worktrees", req.Name); err != nil || path != want {
		t.Errorf("from slot-1: path = %q, %v; want the pass-through %s", path, err, want)
	}
}

// The hook refuses a request for another event, and a subagent name that is not one path element,
// before it makes anything.
func TestWorktreeCreateRefusesWhatItCannotUse(t *testing.T) {
	h := newHarness(t, true)
	good := h.capture(t, "subagent-create.json")
	for name, mutate := range map[string]func(*Request){
		"another event":    func(r *Request) { r.HookEventName = EventWorktreeRemove },
		"no name":          func(r *Request) { r.Name = "" },
		"a parent name":    func(r *Request) { r.Name = ".." },
		"a name with a /":  func(r *Request) { r.Name = "../../escape" },
		"a relative cwd":   func(r *Request) { r.Cwd = "project" },
		"a cwd not a repo": func(r *Request) { r.Cwd = h.e.Home },
	} {
		req := good
		mutate(&req)
		if path, err := WorktreeCreate(context.Background(), h.env, req); err == nil {
			t.Errorf("%s: path = %q, want an error", name, path)
		}
	}
	if _, err := os.Stat(filepath.Join(h.e.Repo, ".claude", "worktrees")); err == nil {
		t.Error(".claude/worktrees exists, want nothing made")
	}
	wts, err := h.env.Git.WorktreeList(context.Background(), h.e.Repo)
	if err != nil || len(wts) != 1 {
		t.Errorf("git worktree list = %+v, %v; want the main checkout alone", wts, err)
	}
	p, err := pool.Load(h.e.Home, h.key)
	if err != nil || len(p.AgentWorktrees) != 0 {
		t.Errorf("agent_worktrees = %+v, %v; want none", p.AgentWorktrees, err)
	}
}

// A request with a prompt_id is a subagent's even without the fields that corroborate it, and the
// hook says so on stderr.
func TestUncorroboratedSubagentWarns(t *testing.T) {
	h := newHarness(t, true)
	req := h.capture(t, "subagent-create.json")
	req.ScratchpadDir, req.Name = "", "helper"
	path, err := WorktreeCreate(context.Background(), h.env, req)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(h.e.Repo, ".claude", "worktrees", "helper"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if !strings.Contains(h.stderr.String(), "upool: warning: a request with a prompt_id") {
		t.Errorf("stderr = %q, want the warning", h.stderr)
	}
}

// When the worktree cannot be recorded, the hook removes it again and fails, so that no worktree
// the pool does not know about is left behind.
func TestSubagentWorktreeIsRemovedWhenItCannotBeRecorded(t *testing.T) {
	h := newHarness(t, true)
	state := filepath.Join(h.e.Home, ".upool", "pools", h.key, "state.json")
	if err := os.WriteFile(state, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := h.capture(t, "subagent-create.json")
	if path, err := WorktreeCreate(context.Background(), h.env, req); err == nil {
		t.Fatalf("path = %q, want an error", path)
	}
	path := filepath.Join(h.e.Repo, ".claude", "worktrees", req.Name)
	if _, ok := h.worktree(t, path); ok {
		t.Errorf("git still lists %s", path)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s is still on disk", path)
	}
}
