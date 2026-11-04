package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// The hook log: two lines for each run of `upool hook <event>` whose request is a pool's, one as it
// starts its work and one as it ends, and what `doctor` reads from it (#278; docs/contracts.md (e),
// "The hook log").

// captureStdin returns the hook capture file with its paths filled for e and each field of set in
// place of the capture's.
func captureStdin(t *testing.T, e *testenv.Env, file string, set map[string]any) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks", file))
	if err != nil {
		t.Fatal(err)
	}
	text, err := pathvars.Fill(string(b), pathvars.Var{Name: "home", Path: e.Home},
		pathvars.Var{Name: "repo", Path: e.Repo},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(e.Home, "claude-tmp")})
	if err != nil {
		t.Fatal(err)
	}
	if len(set) == 0 {
		return text
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(text), &req); err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		req[k] = v
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// runHook runs `upool hook <event>` in e on stdin through the dispatcher, with a clock that moves
// on a millisecond each time it is read, and returns the exit code, stdout and stderr.
func runHook(t *testing.T, e *testenv.Env, event, stdin string) (int, string, string) {
	t.Helper()
	now := clock
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(stdin),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		now: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		}, pid: os.Getpid(), ppid: os.Getppid()}
	code := d.run([]string{"hook", event})
	return code, out.String(), errOut.String()
}

// hookLines returns the lines of the hook log of the pool key under home.
func hookLines(t *testing.T, home, key string) []hooks.LogLine {
	t.Helper()
	lines, err := hooks.ReadLog(home, key)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// loggedRun is what one hook run should log: its start line's kind and path, which the end line
// repeats, and the end line's outcome, slot, worktree and error, the error as a substring.
type loggedRun struct {
	kind, path, outcome, slot, worktree, err string
}

// Each run of `upool hook <event>` whose request is a pool's appends two lines to the pool's hook
// log, its start line before its work and its end line after, with the same process, event, kind
// and path, and, on the end line, what the hook did: every outcome of every event the hooks
// handle (#278).
func TestHookLogsEachRunBeforeAndAfterItsWork(t *testing.T) {
	const passThrough = "system-diagnostics-9f8a09"
	scene := func(p harnessPool, file string) map[string]any {
		return map[string]any{"cwd": p.slot("slot-1"),
			"tool_input": map[string]any{"file_path": p.slot("slot-1") + "/" + file}}
	}
	take := func(t *testing.T, p harnessPool) {
		t.Helper()
		if r := runIn(t, p.Env, p.Repo, "take", "feature"); r.code != output.ExitOK {
			t.Fatalf("take: exit = %d, want 0; stderr %q", r.code, r.stderr)
		}
	}
	// madeInSlot2 makes the pass-through worktree of a session in slot-2 with the create hook,
	// dirty when dirty is set, and returns the stdin of its removal.
	madeInSlot2 := func(t *testing.T, p harnessPool, dirty bool) string {
		t.Helper()
		create := captureStdin(t, p.Env, "desktop-session-create.json",
			map[string]any{"cwd": p.slot("slot-2")})
		code, out, errOut := runHook(t, p.Env, "worktree-create", create)
		if code != output.ExitOK {
			t.Fatalf("create: exit = %d, want 0; stderr %q", code, errOut)
		}
		path := strings.TrimSuffix(out, "\n")
		if dirty {
			if err := os.WriteFile(filepath.Join(path, "notes.txt"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return captureStdin(t, p.Env, "desktop-session-remove.json",
			map[string]any{"cwd": p.slot("slot-2"), "worktree_path": path})
	}
	tests := []struct {
		name, hook, event string
		code              int
		stdin             func(t *testing.T, p harnessPool) string
		want              func(p harnessPool) loggedRun
	}{
		{"a subagent's create", "worktree-create", hooks.EventWorktreeCreate, output.ExitOK,
			func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "subagent-create.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "subagent", path: p.Repo, outcome: "agent-worktree",
					worktree: p.Path(".claude/worktrees/agent-a59f8e76142da8348")}
			}},
		{"a desktop session's create", "worktree-create", hooks.EventWorktreeCreate,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "desktop-session-create.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.Repo, outcome: "slot",
					slot: "slot-1", worktree: p.slot("slot-1")}
			}},
		{"the create of a session in a slot", "worktree-create", hooks.EventWorktreeCreate,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "desktop-session-create.json",
					map[string]any{"cwd": p.slot("slot-2")})
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.slot("slot-2"),
					outcome:  "pass-through",
					worktree: p.slot("slot-2") + "/.claude/worktrees/" + passThrough}
			}},
		{"a create that fails", "worktree-create", hooks.EventWorktreeCreate, output.ExitFailure,
			func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "desktop-session-remove.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.Repo, outcome: "failed",
					err: `stdin is a "WorktreeRemove" request, not WorktreeCreate`}
			}},
		{"the remove of a clean slot", "worktree-remove", hooks.EventWorktreeRemove,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				take(t, p)
				return captureStdin(t, p.Env, "desktop-session-remove.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.slot("slot-1"),
					outcome: "needs-reset", slot: "slot-1"}
			}},
		{"the remove of a dirty slot", "worktree-remove", hooks.EventWorktreeRemove,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				take(t, p)
				if err := os.WriteFile(filepath.Join(p.slot("slot-1"), "spike-reuse.txt"), nil,
					0o600); err != nil {
					t.Fatal(err)
				}
				return captureStdin(t, p.Env, "desktop-session-remove.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.slot("slot-1"),
					outcome: "parked", slot: "slot-1"}
			}},
		{"the remove of a slot with no lease", "worktree-remove", hooks.EventWorktreeRemove,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "desktop-session-remove.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session", path: p.slot("slot-1"),
					outcome: "failed", err: "slot-1 is ready, with no lease to end"}
			}},
		{"a CLI session's remove of a worktree gone from disk", "worktree-remove",
			hooks.EventWorktreeRemove, output.ExitOK,
			func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "cli-session-remove.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "CLI session", path: p.Path(".claude/worktrees/a15-probe"),
					outcome: "gone"}
			}},
		{"the remove of a clean pass-through worktree", "worktree-remove",
			hooks.EventWorktreeRemove, output.ExitOK,
			func(t *testing.T, p harnessPool) string { return madeInSlot2(t, p, false) },
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session",
					path:    p.slot("slot-2") + "/.claude/worktrees/" + passThrough,
					outcome: "removed"}
			}},
		{"the remove of a dirty pass-through worktree", "worktree-remove",
			hooks.EventWorktreeRemove, output.ExitOK,
			func(t *testing.T, p harnessPool) string { return madeInSlot2(t, p, true) },
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "desktop session",
					path:    p.slot("slot-2") + "/.claude/worktrees/" + passThrough,
					outcome: "kept"}
			}},
		{"a compaction in a slot", "session-start", hooks.EventSessionStart, output.ExitOK,
			func(t *testing.T, p harnessPool) string {
				p.Setenv("CLAUDE_ENV_FILE", filepath.Join(tempDir(t), "sessionstart-hook-0.sh"))
				return captureStdin(t, p.Env, "session-start-compact.json",
					map[string]any{"cwd": p.slot("slot-1")})
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "compact", path: p.slot("slot-1"), outcome: "context",
					slot: "slot-1"}
			}},
		{"a startup in a slot whose confirm is refused", "session-start", hooks.EventSessionStart,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				p.Setenv("CLAUDE_ENV_FILE", filepath.Join(tempDir(t), "sessionstart-hook-0.sh"))
				return captureStdin(t, p.Env, "session-start.json",
					map[string]any{"cwd": p.slot("slot-1")})
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "startup", path: p.slot("slot-1"), outcome: "context",
					slot: "slot-1",
					err:  "slot-1 is ready, not leased, so there is no lease to confirm"}
			}},
		{"a startup in the main checkout", "session-start", hooks.EventSessionStart,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "desktop-no-worktree-session-start.json", nil)
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "startup", path: p.Repo, outcome: "none"}
			}},
		{"a subagent of a session in a slot", "subagent-start", hooks.EventSubagentStart,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "cli-subagent-start.json",
					map[string]any{"cwd": p.slot("slot-1")})
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "subagent", path: p.slot("slot-1"), outcome: "context",
					slot: "slot-1"}
			}},
		{"a subagent in the main checkout", "subagent-start", hooks.EventSubagentStart,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				return captureStdin(t, p.Env, "cli-subagent-start.json",
					map[string]any{"cwd": p.Repo})
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "subagent", path: p.Repo, outcome: "none"}
			}},
		{"a scene written in a script lane", "pre-tool-use", hooks.EventPreToolUse,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				take(t, p)
				return captureStdin(t, p.Env, "pretooluse-edit.json",
					scene(p, "Assets/Scenes/Main.unity"))
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "Edit", path: p.slot("slot-1") + "/Assets/Scenes/Main.unity",
					outcome: "deny", slot: "slot-1"}
			}},
		{"a script written in a script lane", "pre-tool-use", hooks.EventPreToolUse,
			output.ExitOK, func(t *testing.T, p harnessPool) string {
				take(t, p)
				return captureStdin(t, p.Env, "pretooluse-edit.json",
					scene(p, "Assets/Scripts/Player.cs"))
			},
			func(p harnessPool) loggedRun {
				return loggedRun{kind: "Edit", path: p.slot("slot-1") + "/Assets/Scripts/Player.cs",
					outcome: "none"}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newHarnessPool(t)
			stdin := tt.stdin(t, p)
			before := len(hookLines(t, p.Home, p.key))
			code, _, errOut := runHook(t, p.Env, tt.hook, stdin)
			if code != tt.code || strings.Contains(errOut, "did not log") {
				t.Fatalf("exit = %d, stderr %q; want %d and no logging warning", code, errOut,
					tt.code)
			}
			lines := hookLines(t, p.Home, p.key)[before:]
			if len(lines) != 2 {
				t.Fatalf("logged %d lines, want 2: %+v", len(lines), lines)
			}
			want := tt.want(p)
			start, end := lines[0], lines[1]
			if start.Phase != hooks.PhaseStart || start.PID != os.Getpid() ||
				start.Event != tt.event || start.Kind != want.kind || start.Path != want.path ||
				start.Outcome != "" || start.Slot != "" || start.Worktree != "" ||
				start.Error != "" {
				t.Errorf("start line = %+v; want %s's start, by process %d, of a %s at %s, "+
					"with no outcome", start, tt.event, os.Getpid(), want.kind, want.path)
			}
			if end.Phase != hooks.PhaseEnd || end.PID != start.PID || end.Event != start.Event ||
				end.Kind != start.Kind || end.Path != start.Path ||
				!end.Time.After(start.Time) {
				t.Errorf("end line = %+v; want the end of the run %+v, later", end, start)
			}
			if end.Outcome != want.outcome || end.Slot != want.slot ||
				end.Worktree != want.worktree || (want.err == "") != (end.Error == "") ||
				!strings.Contains(end.Error, want.err) {
				t.Errorf("end line: outcome %q, slot %q, worktree %q, error %q; want %q, %q, "+
					"%q and an error containing %q", end.Outcome, end.Slot, end.Worktree,
					end.Error, want.outcome, want.slot, want.worktree, want.err)
			}
		})
	}
}

// A hook whose request is in no pool, such as a session's create in a repository upool does not
// manage, logs nothing and makes nothing under ~/.upool.
func TestHookOutsideEveryPoolLogsNothing(t *testing.T) {
	e := testenv.New(t)
	code, _, errOut := runHook(t, e, "worktree-create",
		captureStdin(t, e, "desktop-session-create.json", nil))
	if code != output.ExitOK || errOut != "" {
		t.Fatalf("exit = %d, stderr %q; want 0 and nothing", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool: %v, want it not made", err)
	}
}

// A hook that cannot write its start line, here because its pool's directory does not exist yet,
// says so in a warning and does its work all the same; with no start line, it writes no end line
// either.
func TestHookWarnsWhenItCannotLogAndWorksOn(t *testing.T) {
	e := testenv.New(t)
	repo, err := registry.Add(context.Background(), e.Home, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runHook(t, e, "worktree-create",
		captureStdin(t, e, "subagent-create.json", nil))
	path := filepath.Join(e.Repo, ".claude", "worktrees", "agent-a59f8e76142da8348")
	warning := "upool: warning: did not log the hook: opening ~/.upool/pools/" + repo.Key + "/" +
		hooks.LogFile + ": "
	if code != output.ExitOK || out != path+"\n" || !strings.HasPrefix(errOut, warning) ||
		strings.Count(errOut, "\n") != 1 {
		t.Errorf("exit = %d, stdout %q, stderr %q; want 0, %q and one line starting %q", code,
			out, errOut, path+"\n", warning)
	}
	if lines := hookLines(t, e.Home, repo.Key); len(lines) != 0 {
		t.Errorf("hook log = %+v, want no line", lines)
	}
}
