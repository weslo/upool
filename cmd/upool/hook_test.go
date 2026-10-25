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

	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// `upool hook worktree-create` on a captured subagent request prints exactly the new worktree's
// path and a newline, writes nothing to stderr, and records its own parent as the agent
// worktree's parent_pid: the process that ran the hook (#71).
func TestHookWorktreeCreatePrintsTheSubagentsWorktree(t *testing.T) {
	e := testenv.New(t)
	repo, err := registry.Add(context.Background(), e.Home, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.Update(context.Background(), e.Home, repo.Key, func(p *pool.Pool) error {
		p.Repo, p.Base = e.Repo, testenv.Base
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"subagent-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: e.Home},
		pathvars.Var{Name: "repo", Path: e.Repo},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(e.Home, "claude-tmp")})
	if err != nil {
		t.Fatal(err)
	}

	const ppid = 31337
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid(), ppid: ppid}
	if code := d.run([]string{"hook", "worktree-create"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	path := filepath.Join(e.Repo, ".claude", "worktrees", "agent-a59f8e76142da8348")
	if want := path + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want exactly %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
	p, err := pool.Load(e.Home, repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AgentWorktrees) != 1 || p.AgentWorktrees[0].Path != path ||
		p.AgentWorktrees[0].ParentPID != ppid {
		t.Errorf("agent_worktrees = %+v, want %s with parent_pid %d", p.AgentWorktrees, path,
			ppid)
	}
}

// `upool hook worktree-create` on a captured desktop session's request, in a repository that is not
// a pool, prints exactly the pass-through worktree's path and a newline (#72).
func TestHookWorktreeCreatePrintsThePassThroughWorktree(t *testing.T) {
	e := testenv.New(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-create.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: e.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-create"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	path := filepath.Join(e.Repo, ".claude", "worktrees", "system-diagnostics-9f8a09")
	if want := path + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want exactly %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
	if got := e.Git("worktree", "list", "--porcelain"); !strings.Contains(got,
		"worktree "+path+"\n") {
		t.Errorf("git worktree list = %q, want %s", got, path)
	}
}

// After `upool take`, the WorktreeRemove hook on the captured archive of a session in slot-1 marks
// the slot needs-reset, clears the lease, prints the slot's path and exits 0 (#73).
func TestHookWorktreeRemoveMarksTheTakenSlot(t *testing.T) {
	p := newHarnessPool(t)
	if r := runIn(t, p.Env, p.Repo, "take", "feature"); r.code != output.ExitOK {
		t.Fatalf("take: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"desktop-session-remove.json"))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: p.Repo})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: p.Getenv, environ: p.Environ,
		now: func() time.Time { return clock }, pid: os.Getpid()}
	if code := d.run([]string{"hook", "worktree-remove"}); code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", code, output.ExitOK, errOut.String())
	}
	if want := p.slot("slot-1") + "\n"; out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr %q; want %q and nothing", out.String(), errOut.String(),
			want)
	}
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	if s := pl.Slots[0]; s.Name != "slot-1" || s.State != pool.StateNeedsReset || s.Lease != nil {
		t.Errorf("slot-1 = %+v, want needs-reset with no lease", s)
	}
}

// Through the dispatcher, the create hook's pass-through worktree for a desktop session in a
// repository upool does not manage is removed again by the remove hook on the session's archive,
// both exiting 0 (#72, #74).
func TestHookRemovesThePassThroughWorktreeItMade(t *testing.T) {
	e := testenv.New(t)
	hook := func(event, capture, worktree string) (int, string, string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks", capture))
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: e.Repo})
		if err != nil {
			t.Fatal(err)
		}
		if worktree != "" {
			stdin = strings.Replace(stdin, filepath.Join(e.Repo, ".claude", "worktrees", "slot-1"),
				worktree, 1)
		}
		var out, errOut bytes.Buffer
		d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
			stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
			now: func() time.Time { return clock }, pid: os.Getpid()}
		return d.run([]string{"hook", event}), out.String(), errOut.String()
	}
	code, out, errOut := hook("worktree-create", "desktop-session-create.json", "")
	path := strings.TrimSuffix(out, "\n")
	if code != output.ExitOK || errOut != "" {
		t.Fatalf("create: exit = %d, stderr %q; want %d and nothing", code, errOut, output.ExitOK)
	}
	code, out, errOut = hook("worktree-remove", "desktop-session-remove.json", path)
	if code != output.ExitOK || out != path+"\n" || errOut != "" {
		t.Errorf("remove: exit = %d, stdout %q, stderr %q; want %d, %q and nothing", code, out,
			errOut, output.ExitOK, path+"\n")
	}
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s is still on disk", path)
	}
	if got := e.Git("worktree", "list", "--porcelain"); strings.Contains(got, path) {
		t.Errorf("git worktree list = %q, want %s gone", got, path)
	}
}

// Through the dispatcher, `upool hook pre-tool-use` on the captured Edit request, pointed at a
// scene in slot-1, which `upool take` leased in the script lane, prints Claude Code's deny
// decision and exits 0; pointed at a script, it prints nothing and exits 0 (#95).
func TestHookPreToolUseDeniesASceneInAScriptLane(t *testing.T) {
	p := newHarnessPool(t)
	if r := runIn(t, p.Env, p.Repo, "take", "feature"); r.code != output.ExitOK {
		t.Fatalf("take: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks",
		"pretooluse-edit.json"))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := pathvars.Fill(string(b), pathvars.Var{Name: "repo", Path: p.Repo},
		pathvars.Var{Name: "home", Path: p.Home},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(p.Home, "claude-tmp")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		file string
		deny bool
	}{{"Assets/Scenes/Main.unity", true}, {"Assets/Scripts/Player.cs", false}} {
		stdin := strings.Replace(capture, p.slot("slot-1")+"/upool-capture.txt",
			p.slot("slot-1")+"/"+tt.file, 1)
		var out, errOut bytes.Buffer
		d := dispatcher{commands: commandTable, hooks: hookTable,
			stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, getenv: p.Getenv,
			environ: p.Environ, now: func() time.Time { return clock }, pid: os.Getpid()}
		if code := d.run([]string{"hook", "pre-tool-use"}); code != output.ExitOK ||
			errOut.Len() != 0 {
			t.Fatalf("%s: exit = %d, stderr %q; want 0 and nothing", tt.file, code,
				errOut.String())
		}
		if !tt.deny {
			if out.Len() != 0 {
				t.Errorf("%s: stdout = %q, want nothing", tt.file, out.String())
			}
			continue
		}
		var decision struct {
			HookSpecificOutput struct {
				HookEventName, PermissionDecision, PermissionDecisionReason string
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &decision); err != nil ||
			!strings.HasSuffix(out.String(), "}\n") || strings.Count(out.String(), "\n") != 1 {
			t.Fatalf("%s: stdout = %q, want one line of JSON: %v", tt.file, out.String(), err)
		}
		if got := decision.HookSpecificOutput; got.HookEventName != "PreToolUse" ||
			got.PermissionDecision != "deny" ||
			!strings.Contains(got.PermissionDecisionReason, "--lane scene") {
			t.Errorf("%s: decision %+v, want PreToolUse denied, naming --lane scene", tt.file,
				got)
		}
	}
}

// slotStdin returns the hook capture file with its cwd pointed at dir and its paths filled for p.
func slotStdin(t *testing.T, p harnessPool, file, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks", file))
	if err != nil {
		t.Fatal(err)
	}
	text, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: p.Home},
		pathvars.Var{Name: "repo", Path: p.Repo},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(p.Home, "claude-tmp")})
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(text), &req); err != nil {
		t.Fatal(err)
	}
	req["cwd"] = dir
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Through the dispatcher, `upool hook session-start` and `upool hook subagent-start` print, for a
// session in a slot and its subagent, the one line of context that gives them upool's
// instructions, and exit 0 (A43; #229). A startup whose confirm is refused still prints it, with
// the refusal on stderr.
func TestHookStartsPrintTheInstructions(t *testing.T) {
	p := newHarnessPool(t)
	p.Setenv("CLAUDE_ENV_FILE", filepath.Join(tempDir(t), "sessionstart-hook-0.sh"))
	slot := p.slot("slot-1")
	for _, tt := range []struct {
		event, capture, wantEvent, stderr string
	}{
		{"session-start", "session-start-compact.json", hooks.EventSessionStart, ""},
		{"session-start", "session-start.json", hooks.EventSessionStart,
			"upool: hook session-start: "},
		{"subagent-start", "cli-subagent-start.json", hooks.EventSubagentStart, ""},
	} {
		var out, errOut bytes.Buffer
		d := dispatcher{commands: commandTable, hooks: hookTable,
			stdin: strings.NewReader(slotStdin(t, p, tt.capture, slot)), stdout: &out,
			stderr: &errOut, getenv: p.Getenv, environ: p.Environ,
			now: func() time.Time { return clock }, pid: os.Getpid(), ppid: os.Getppid()}
		code := d.run([]string{"hook", tt.event})
		doc, err := hooks.ContextDocument(tt.wantEvent)
		if err != nil {
			t.Fatal(err)
		}
		if code != output.ExitOK || out.String() != doc+"\n" {
			t.Errorf("%s on %s: exit %d, stdout %q; want 0 and the %s context", tt.event,
				tt.capture, code, out.String(), tt.wantEvent)
		}
		if (tt.stderr == "") != (errOut.Len() == 0) ||
			!strings.HasPrefix(errOut.String(), tt.stderr) {
			t.Errorf("%s on %s: stderr %q, want %q", tt.event, tt.capture, errOut.String(),
				tt.stderr)
		}
	}
}
