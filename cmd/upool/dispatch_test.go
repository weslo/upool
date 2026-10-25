package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pathvars"
)

// clock is the fixed time the test dispatchers' clock returns.
var clock = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// testPID is the process ID the dispatch helper's dispatcher hands its handlers.
const testPID = 4242

// tempDir is t.TempDir with its symlinks resolved (on macOS it is under /var, which is
// /private/var).
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// testEnv is the environment a test dispatcher runs in: HOME is a temporary directory, as for
// every test (CONTRIBUTING.md, "Testing"), and nothing else is set.
func testEnv(t *testing.T) map[string]string {
	return map[string]string{"HOME": tempDir(t)}
}

// result is what a dispatcher printed and returned.
type result struct {
	code           int
	stdout, stderr string
}

// dispatch runs args through a dispatcher with the given tables, env and stdin, as run does with
// the real ones. Its working directory is env's PWD, as a shell would set it.
func dispatch(t *testing.T, commands map[string]command, hooks map[string]hook,
	env map[string]string, stdin string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{
		commands: commands,
		hooks:    hooks,
		stdin:    strings.NewReader(stdin),
		stdout:   &out,
		stderr:   &errOut,
		getenv:   func(key string) string { return env[key] },
		environ: func() []string {
			var entries []string
			for _, k := range slices.Sorted(maps.Keys(env)) {
				entries = append(entries, k+"="+env[k])
			}
			return entries
		},
		getwd:      func() (string, error) { return env["PWD"], nil },
		now:        func() time.Time { return clock },
		pid:        testPID,
		executable: os.Executable,
	}
	code := d.run(args)
	return result{code, out.String(), errOut.String()}
}

// mustJSON is v as JSON, with map keys sorted, for comparing decoded documents.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// echoed is the echo command's data: its positional arguments and its --n flag.
type echoed struct {
	Args []string `json:"args"`
	N    int      `json:"n"`
}

func (e echoed) Human() string { return strings.Join(e.Args, " ") }

// echo is a test command with one flag of its own, --n, that returns what it was given.
var echo = command{
	usage: "echo <arg>... [--n N] [--json]",
	setup: func(fs *flag.FlagSet) handler {
		n := fs.Int("n", 0, "a number")
		return func(inv *invocation) (any, []string, error) {
			return echoed{Args: inv.args, N: *n}, nil, nil
		}
	},
}

// decode reads the envelope a command printed with --json.
func decode(t *testing.T, stdout string) output.Envelope {
	t.Helper()
	var env output.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decoding stdout %q: %v", stdout, err)
	}
	return env
}

func TestFlagsAfterPositionalArgs(t *testing.T) {
	tests := []struct {
		args []string
		want echoed
	}{
		{[]string{"a", "--json"}, echoed{[]string{"a"}, 0}},
		{[]string{"--json", "a"}, echoed{[]string{"a"}, 0}},
		{[]string{"a", "--json", "--n", "2", "b"}, echoed{[]string{"a", "b"}, 2}},
		{[]string{"a", "b", "-n=3", "-json"}, echoed{[]string{"a", "b"}, 3}},
		{[]string{"--n", "1", "a", "--json", "b", "--n", "4"}, echoed{[]string{"a", "b"}, 4}},
		{[]string{"a", "-", "--json"}, echoed{[]string{"a", "-"}, 0}},
		{[]string{"a", "--json", "--", "--n", "5", "--json"}, echoed{[]string{"a", "--n", "5", "--json"}, 0}},
	}
	commands := map[string]command{"echo": echo}
	for _, tt := range tests {
		r := dispatch(t, commands, nil, testEnv(t), "", append([]string{"echo"}, tt.args...)...)
		if r.code != output.ExitOK {
			t.Errorf("%q: exit = %d, want %d; stderr %q", tt.args, r.code, output.ExitOK, r.stderr)
			continue
		}
		env := decode(t, r.stdout)
		var got echoed
		data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%q: decoding data %s: %v", tt.args, data, err)
		}
		if !slices.Equal(got.Args, tt.want.Args) || got.N != tt.want.N {
			t.Errorf("%q: data = %+v, want %+v", tt.args, got, tt.want)
		}
		if !env.Success || env.Command != "echo" {
			t.Errorf("%q: success, command = %t, %q, want true, %q",
				tt.args, env.Success, env.Command, "echo")
		}
	}
}

func TestWithoutJSONTheDataPrintsInItsHumanForm(t *testing.T) {
	commands := map[string]command{"echo": echo}
	r := dispatch(t, commands, nil, testEnv(t), "", "echo", "a", "--n", "2", "b")
	if r.code != output.ExitOK {
		t.Errorf("exit = %d, want %d", r.code, output.ExitOK)
	}
	if want := "a b\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

func TestBadFlagFailsWithTheCommandsUsage(t *testing.T) {
	commands := map[string]command{"echo": echo}
	for _, args := range [][]string{{"--bogus"}, {"a", "--n"}, {"a", "--n", "x"}} {
		r := dispatch(t, commands, nil, testEnv(t), "", append([]string{"echo"}, args...)...)
		if r.code != output.ExitFailure {
			t.Errorf("%q: exit = %d, want %d", args, r.code, output.ExitFailure)
		}
		if r.stdout != "" {
			t.Errorf("%q: stdout = %q, want empty", args, r.stdout)
		}
		if want := "usage: upool " + echo.usage + "\n"; !strings.HasPrefix(r.stderr, "upool: ") ||
			!strings.HasSuffix(r.stderr, want) {
			t.Errorf("%q: stderr = %q, want an error ending in %q", args, r.stderr, want)
		}
	}
}

func TestBadFlagUnderJSONPrintsAFailureEnvelope(t *testing.T) {
	commands := map[string]command{"echo": echo}
	for _, args := range [][]string{
		{"--json", "--bogus"},
		{"--bogus", "--json"},
		{"a", "--n", "x", "--json=true"},
	} {
		r := dispatch(t, commands, nil, testEnv(t), "", append([]string{"echo"}, args...)...)
		if r.code != output.ExitFailure {
			t.Errorf("%q: exit = %d, want %d", args, r.code, output.ExitFailure)
		}
		if r.stderr != "" {
			t.Errorf("%q: stderr = %q, want empty", args, r.stderr)
		}
		env := decode(t, r.stdout)
		if env.Success || len(env.Errors) != 1 || env.Errors[0].Code != string(output.CodeCommandFailed) {
			t.Errorf("%q: envelope = %+v, want one %s error", args, env, output.CodeCommandFailed)
		}
	}
}

func TestJSONRequested(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"a"}, false},
		{[]string{"json"}, false},
		{[]string{"--json"}, true},
		{[]string{"-json"}, true},
		{[]string{"--json=true"}, true},
		{[]string{"--json=false"}, false},
		{[]string{"--json", "--json=false"}, false},
		{[]string{"--json=false", "--json"}, true},
		{[]string{"--json=nope"}, false},
		{[]string{"--", "--json"}, false},
	}
	for _, tt := range tests {
		if got := jsonRequested(tt.args); got != tt.want {
			t.Errorf("jsonRequested(%q) = %t, want %t", tt.args, got, tt.want)
		}
	}
}

func TestCommandExitsWithItsErrorsExitCode(t *testing.T) {
	commands := map[string]command{"fail": {
		usage: "fail",
		setup: func(*flag.FlagSet) handler {
			return func(*invocation) (any, []string, error) {
				return nil, []string{"careful"}, output.NoTarget(output.CodeCommandFailed, "no Editor")
			}
		},
	}}
	r := dispatch(t, commands, nil, testEnv(t), "", "fail")
	if r.code != output.ExitNoTarget {
		t.Errorf("exit = %d, want %d", r.code, output.ExitNoTarget)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	if want := "upool: warning: careful\nupool: no Editor\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
}

func TestHandlerGetsTheProcessBoundaryButNotStdout(t *testing.T) {
	var got *invocation
	var stdin []byte
	commands := map[string]command{"probe": {
		usage: "probe",
		setup: func(*flag.FlagSet) handler {
			return func(inv *invocation) (any, []string, error) {
				got = inv
				var err error
				if stdin, err = io.ReadAll(inv.stdin); err != nil {
					return nil, nil, err
				}
				_, _ = fmt.Fprintln(inv.stderr, "progress") // a test buffer does not fail
				return nil, nil, nil
			}
		},
	}}
	env := testEnv(t)
	env["UPOOL_READY"] = "2"
	env["PWD"] = tempDir(t)
	r := dispatch(t, commands, nil, env, "payload", "probe", "x", "--json")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if want := "progress\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
	if env := decode(t, r.stdout); !env.Success || env.Command != "probe" {
		t.Errorf("stdout = %q, want only the success envelope", r.stdout)
	}
	if !slices.Equal(got.args, []string{"x"}) {
		t.Errorf("args = %q, want [x]", got.args)
	}
	if f := got.flags.Lookup("json"); f == nil || f.Value.String() != "true" {
		t.Errorf("flags: --json = %v, want true", f)
	}
	if string(stdin) != "payload" {
		t.Errorf("stdin = %q, want %q", stdin, "payload")
	}
	if v := got.getenv("UPOOL_READY"); v != "2" {
		t.Errorf("getenv(UPOOL_READY) = %q, want %q", v, "2")
	}
	if entries := got.environ(); !slices.Contains(entries, "UPOOL_READY=2") ||
		!slices.Contains(entries, "HOME="+env["HOME"]) {
		t.Errorf("environ() = %q, want UPOOL_READY=2 and HOME among them", entries)
	}
	if wd, err := got.getwd(); err != nil || wd != env["PWD"] {
		t.Errorf("getwd() = %q, %v, want %q, nil", wd, err, env["PWD"])
	}
	if got.home != env["HOME"] {
		t.Errorf("home = %q, want %q", got.home, env["HOME"])
	}
	if now := got.now(); !now.Equal(clock) {
		t.Errorf("now() = %v, want %v", now, clock)
	}
	if got.pid != testPID {
		t.Errorf("pid = %d, want %d", got.pid, testPID)
	}
}

func TestUsageListsOnlyImplementedCommands(t *testing.T) {
	planned := []string{
		"setup", "init", "take", "release", "which", "path", "status",
		"open", "close", "refresh", "prune", "doctor", "run",
	}
	u := usage(commandTable)
	for _, name := range planned {
		_, implemented := commandTable[name]
		if listed := strings.Contains(u, "upool "+name); listed != implemented {
			t.Errorf("%s: listed in usage = %t, implemented = %t", name, listed, implemented)
		}
	}
	if strings.Contains(u, "hook") {
		t.Errorf("usage = %q, want no mention of upool hook", u)
	}
	if !strings.HasPrefix(u, "usage: upool --version\n") {
		t.Errorf("usage = %q, want it to start with upool --version", u)
	}

	got := usage(map[string]command{"echo": echo})
	if want := "usage: upool --version\n       upool " + echo.usage + "\n"; got != want {
		t.Errorf("usage with echo = %q, want %q", got, want)
	}
}

// hookStdin is the capture under testdata/captures/hooks/ named file, with its paths filled in.
func hookStdin(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "hooks", file))
	if err != nil {
		t.Fatal(err)
	}
	home := tempDir(t)
	text, err := pathvars.Fill(string(b),
		pathvars.Var{Name: "home", Path: home},
		pathvars.Var{Name: "repo", Path: filepath.Join(home, "src", "down")},
		pathvars.Var{Name: "claude_tmp", Path: filepath.Join(tempDir(t), "claude")},
	)
	if err != nil {
		t.Fatalf("filling %s: %v", file, err)
	}
	return text
}

// pathHook is a hook handler that writes progress to stderr and returns path.
func pathHook(path string) hookHandler {
	return func(inv *invocation) (string, error) {
		_, _ = fmt.Fprintln(inv.stderr, "cloning…") // a test buffer does not fail
		return path, nil
	}
}

func TestHookSuccessPrintsOnlyThePath(t *testing.T) {
	path := filepath.Join(tempDir(t), ".claude", "worktrees", "slot-1")
	stdin := hookStdin(t, "desktop-session-create.json")
	for event, real := range hookTable {
		hooks := map[string]hook{event: {handle: pathHook(path), failExit: real.failExit}}
		r := dispatch(t, nil, hooks, testEnv(t), stdin, "hook", event)
		if r.code != output.ExitOK {
			t.Errorf("%s: exit = %d, want %d", event, r.code, output.ExitOK)
		}
		if want := path + "\n"; r.stdout != want {
			t.Errorf("%s: stdout = %q, want %q", event, r.stdout, want)
		}
		if want := "cloning…\n"; r.stderr != want {
			t.Errorf("%s: stderr = %q, want %q", event, r.stderr, want)
		}
	}
}

// A hook that succeeds with no path, as session-start does, leaves stdout empty: Claude Code adds a
// SessionStart hook's stdout to the session's context. The real session-start, for a session in
// no slot, does nothing and says nothing.
func TestHookWithNoPathPrintsNothing(t *testing.T) {
	var out, errOut bytes.Buffer
	env := testEnv(t)
	env["CLAUDE_ENV_FILE"] = filepath.Join(tempDir(t), "sessionstart-hook-0.sh")
	stdin := strings.NewReader(hookStdin(t, "desktop-no-worktree-session-start.json"))
	code := run([]string{"hook", "session-start"}, stdin, &out, &errOut,
		func(key string) string { return env[key] })
	if code != output.ExitOK || out.String() != "" || errOut.String() != "" {
		t.Errorf("exit = %d, stdout %q, stderr %q; want %d and nothing on either", code,
			out.String(), errOut.String(), output.ExitOK)
	}
	if _, err := os.Lstat(env["CLAUDE_ENV_FILE"]); err == nil {
		t.Errorf("%s exists, want nothing exported for a session in no slot",
			env["CLAUDE_ENV_FILE"])
	}
}

// Each hook that fails says so on stderr, with its event's exit code and nothing on stdout:
// worktree-create for a session in a repository that is not there, whose pass-through worktree git
// cannot make, and worktree-remove, session-start and subagent-start given another event's
// request.
func TestHookFailuresGoToStderr(t *testing.T) {
	tests := []struct {
		event, capture string
		want           int
		message        string
		// stdin, when set, is the hook's stdin in place of the capture.
		stdin string
	}{
		{"worktree-create", "desktop-session-create.json", output.ExitFailure,
			"making the worktree ", ""},
		{"worktree-remove", "desktop-session-create.json", output.ExitOK,
			`stdin is a "WorktreeCreate" request, not WorktreeRemove`, ""},
		{"session-start", "desktop-session-create.json", output.ExitOK,
			`stdin is a "WorktreeCreate" request, not SessionStart`, ""},
		{"subagent-start", "desktop-session-create.json", output.ExitOK,
			`stdin is a "WorktreeCreate" request, not SubagentStart`, ""},
		// A request it cannot read fails the lane guard, exiting non-zero, which the
		// registration's "onFailure": "block" makes a denial.
		{"pre-tool-use", "", output.ExitFailure, "decoding the hook's stdin", "not json"},
	}
	if len(tests) != len(hookTable) {
		t.Errorf("%d events tested, want all %d in the hook table", len(tests), len(hookTable))
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		env := testEnv(t)
		input := tt.stdin
		if input == "" {
			input = hookStdin(t, tt.capture)
		}
		stdin := strings.NewReader(input)
		code := run([]string{"hook", tt.event}, stdin, &out, &errOut,
			func(key string) string { return env[key] })
		if code != tt.want {
			t.Errorf("%s: exit = %d, want %d", tt.event, code, tt.want)
		}
		if out.String() != "" {
			t.Errorf("%s: stdout = %q, want empty", tt.event, out.String())
		}
		if want := "upool: hook " + tt.event + ": " + tt.message; !strings.HasPrefix(
			errOut.String(), want) || strings.Count(errOut.String(), "\n") != 1 {
			t.Errorf("%s: stderr = %q, want one line starting %q", tt.event, errOut.String(), want)
		}
	}
}

// failingWriter is a stdout that cannot be written.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestWorktreeRemoveAlwaysExitsZero(t *testing.T) {
	remove := hookTable["worktree-remove"]
	stdin := hookStdin(t, "desktop-session-remove.json")
	failures := map[string]hookHandler{
		"the handler, given a create request": func(inv *invocation) (string, error) {
			inv.stdin = strings.NewReader(hookStdin(t, "desktop-session-create.json"))
			return remove.handle(inv)
		},
		"a plain error": func(*invocation) (string, error) { return "", errors.New("disk full") },
		"a no-target failure": func(*invocation) (string, error) {
			return "", output.NoTarget(output.CodeCommandFailed, "no Editor")
		},
	}
	for name, handle := range failures {
		hooks := map[string]hook{"worktree-remove": {handle: handle, failExit: remove.failExit}}
		r := dispatch(t, nil, hooks, testEnv(t), stdin, "hook", "worktree-remove")
		if r.code != output.ExitOK {
			t.Errorf("%s: exit = %d, want %d", name, r.code, output.ExitOK)
		}
		if r.stdout != "" {
			t.Errorf("%s: stdout = %q, want empty", name, r.stdout)
		}
		if r.stderr == "" {
			t.Errorf("%s: stderr empty, want the error", name)
		}
	}

	hooks := map[string]hook{"worktree-remove": remove}
	r := dispatch(t, nil, hooks, testEnv(t), stdin, "hook", "worktree-remove", "extra")
	if r.code != output.ExitOK {
		t.Errorf("extra argument: exit = %d, want %d", r.code, output.ExitOK)
	}

	var errOut bytes.Buffer
	d := dispatcher{
		hooks: map[string]hook{
			"worktree-remove": {handle: pathHook("/slot"), failExit: remove.failExit},
		},
		stdin:  strings.NewReader(stdin),
		stdout: failingWriter{},
		stderr: &errOut,
		getenv: func(string) string { return "" },
		now:    func() time.Time { return clock },
	}
	if code := d.run([]string{"hook", "worktree-remove"}); code != output.ExitOK {
		t.Errorf("stdout unwritable: exit = %d, want %d", code, output.ExitOK)
	}
}

func TestWorktreeCreateFailureExitsNonZero(t *testing.T) {
	create := hookTable["worktree-create"]
	hooks := map[string]hook{"worktree-create": create}
	stdin := hookStdin(t, "desktop-session-create.json")
	r := dispatch(t, nil, hooks, testEnv(t), stdin, "hook", "worktree-create", "extra")
	if r.code != output.ExitFailure {
		t.Errorf("extra argument: exit = %d, want %d", r.code, output.ExitFailure)
	}
	if r.stdout != "" {
		t.Errorf("extra argument: stdout = %q, want empty", r.stdout)
	}
}

func TestHookWithoutAKnownEventFails(t *testing.T) {
	for _, args := range [][]string{{"hook"}, {"hook", "nope"}, {"hook", "--json"}} {
		r := dispatch(t, nil, hookTable, testEnv(t), "", args...)
		if r.code != output.ExitFailure {
			t.Errorf("%q: exit = %d, want %d", args, r.code, output.ExitFailure)
		}
		if r.stdout != "" {
			t.Errorf("%q: stdout = %q, want empty", args, r.stdout)
		}
		if !strings.Contains(r.stderr,
			"session-start, subagent-start, worktree-create, worktree-remove") {
			t.Errorf("%q: stderr = %q, want it to list the events", args, r.stderr)
		}
	}
}
