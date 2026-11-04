package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/hooks"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of doctor's documents-access check (#286), with a fake desktop app helper: an
// executable file where the check looks for the helper, and a runner in its place that records
// each run and answers as the test scripts it. The check never runs this machine's helper.

// helperRun is one run of a fakeHelper.
type helperRun struct {
	helper string
	argv   []string
	dir    string
	stdin  []byte
}

// fakeHelper is a desktop app helper for the check.
type fakeHelper struct {
	path string
	// answer is what each run answers.
	answer func(ctx context.Context, run helperRun) (stderr []byte, exit int, err error)
	// logErr, when set, is why the unified log cannot be read.
	logErr error

	mu   sync.Mutex
	runs []helperRun
}

// newFakeHelper makes a fakeHelper whose runs answer with answer, at Claude.app's helper path
// in a directory of its own.
func newFakeHelper(t *testing.T, answer func(context.Context, helperRun) ([]byte, int,
	error)) *fakeHelper {
	t.Helper()
	path := filepath.Join(tempDir(t), "Claude.app", filepath.FromSlash(helperPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// The check only looks for an executable file; the runner stands in for running it.
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 99\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeHelper{path: path, answer: answer}
}

// desktop is the desktopHelper that finds and runs f, with a unified log that reads as empty, or
// that fails with f.logErr, as for a standard account.
func (f *fakeHelper) desktop() desktopHelper {
	return desktopHelper{paths: []string{f.path}, run: f.run, log: emptyLog{f.logErr}}
}

// emptyLog is a tcc.Log with no event in it, as `log show` prints that, or that fails with err.
type emptyLog struct{ err error }

func (l emptyLog) Show(context.Context, time.Duration, string) ([]byte, error) {
	if l.err != nil {
		return nil, l.err
	}
	return []byte(`{"count":0,"finished":1}` + "\n"), nil
}

func (f *fakeHelper) run(ctx context.Context, helper string, argv, _ []string, dir string,
	stdin []byte) ([]byte, int, error) {
	r := helperRun{helper: helper, argv: argv, dir: dir, stdin: stdin}
	f.mu.Lock()
	f.runs = append(f.runs, r)
	f.mu.Unlock()
	return f.answer(ctx, r)
}

// ran returns f's runs.
func (f *fakeHelper) ran() []helperRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// answering is an answer: stderr and exit, at once.
func answering(stderr string, exit int) func(context.Context, helperRun) ([]byte, int, error) {
	return func(context.Context, helperRun) ([]byte, int, error) {
		return []byte(stderr), exit, nil
	}
}

// holding is an answer that writes stderr and then holds, as a hook macOS holds for a prompt
// does, until the check's bound ends it.
func holding(stderr string) func(context.Context, helperRun) ([]byte, int, error) {
	return func(ctx context.Context, _ helperRun) ([]byte, int, error) {
		<-ctx.Done()
		return []byte(stderr), -1, ctx.Err()
	}
}

// documentsCheck runs the check for p's main checkout with f as the helper and upool as the
// binary the create hook runs.
func documentsCheck(t *testing.T, p harnessPool, f *fakeHelper, upool string) doctorCheck {
	t.Helper()
	inv := &invocation{desktop: f.desktop(), environ: p.Environ, now: time.Now}
	c, ok := documentsAccessCheck(context.Background(), inv, p.Repo, upool)
	if !ok {
		t.Fatal("the check did not run, with a helper to look for")
	}
	if c.Name != checkDocumentsAccess {
		t.Errorf("check name = %q, want %s", c.Name, checkDocumentsAccess)
	}
	return c
}

// runDesktop runs upool with args in e's dir, as runIn does, with desktop as the desktop app's
// helper.
func runDesktop(t *testing.T, e *testenv.Env, dir string, desktop desktopHelper,
	args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{commands: commandTable, hooks: hookTable, stdin: strings.NewReader(""),
		stdout: &out, stderr: &errOut, getenv: e.Getenv, environ: e.Environ,
		getwd: func() (string, error) { return dir, nil }, now: func() time.Time { return clock },
		pid: os.Getpid(), ppid: os.Getppid(), executable: os.Executable, desktop: desktop}
	return newResult(d.run(args), out.String(), errOut.String())
}

// asTheHelper is an answer that does what the helper does with its arguments, upool's create hook
// with --dry-run: it runs them, in-process, with the run's stdin and e's environment, and answers
// with upool's stderr and exit code.
func asTheHelper(e *testenv.Env) func(context.Context, helperRun) ([]byte, int, error) {
	return func(_ context.Context, r helperRun) ([]byte, int, error) {
		var out, errOut bytes.Buffer
		d := dispatcher{hooks: hookTable, stdin: bytes.NewReader(r.stdin), stdout: &out,
			stderr: &errOut, getenv: e.Getenv, environ: e.Environ, now: time.Now,
			executable: os.Executable}
		code := d.run(r.argv[1:])
		if out.Len() != 0 {
			errOut.WriteString("stdout: " + out.String())
			code = 1
		}
		return errOut.Bytes(), code, nil
	}
}

// The acceptance test of #286, a hook that answers: doctor runs, last, the project's create hook
// through the helper as `upool hook worktree-create --dry-run`, with a desktop session's create
// request for the main checkout on stdin, from it. The hook's dry run makes its git call, prints
// nothing and takes no slot, and the check passes.
func TestDoctorDocumentsAccessPassesWhenTheHookAnswers(t *testing.T) {
	p := newHarnessPool(t)
	f := newFakeHelper(t, asTheHelper(p.Env))
	r := runDesktop(t, p.Env, p.Repo, f.desktop(), "doctor", "--json")
	checks := doctorChecks(t, r)
	last := checks[len(checks)-1]
	if last.Name != checkDocumentsAccess || last.Result != checkPass ||
		!strings.HasPrefix(last.Reason, "the desktop app's helper ran upool's create hook, and "+
			"its git read "+p.Repo+" in ") {
		t.Errorf("last check = %+v, want documents-access passing", last)
	}
	runs := f.ran()
	upool := createHookUpool(p.Repo)
	if len(runs) != 1 || runs[0].helper != f.path || runs[0].dir != p.Repo ||
		!slices.Equal(runs[0].argv, []string{upool, "hook", "worktree-create", "--dry-run"}) {
		t.Fatalf("helper runs = %+v, want one of %s hook worktree-create --dry-run in %s", runs,
			upool, p.Repo)
	}
	req, err := hooks.Decode(bytes.NewReader(runs[0].stdin))
	if err != nil {
		t.Fatal(err)
	}
	if req.Kind() != hooks.DesktopSession || req.Cwd != p.Repo ||
		req.HookEventName != hooks.EventWorktreeCreate || req.Name == "" {
		t.Errorf("stdin = %+v, want a desktop session's create request from %s", req, p.Repo)
	}
	for _, s := range p.loadPool(t).Slots {
		if s.State != pool.StateReady || s.Lease != nil {
			t.Errorf("%s = %s with lease %+v, want it still ready and unleased", s.Name, s.State,
				s.Lease)
		}
	}
	// The dry run is no session's create, so the hook log has no line for it (#278).
	if lines, err := hooks.ReadLog(p.Home, p.key); err != nil || len(lines) != 0 {
		t.Errorf("hook log = %+v, %v; want no line for the dry run", lines, err)
	}
}

// The acceptance test of #286, a prompt pending: the hook's waiting line on stderr, or no answer
// within the 10 s bound, warns that macOS is asking, saying to answer and run doctor again. The
// bound passes on testing/synctest's clock.
func TestDoctorDocumentsAccessWarnsWhileThePromptIsPending(t *testing.T) {
	p := newHarnessPool(t)
	want := "macOS is asking whether upool may access Documents; answer it, then run upool " +
		"doctor again"
	for name, tt := range map[string]struct{ stderr, reason string }{
		"waiting line": {"upool: " + hooks.Waiting("upool") + "\n", want},
		"no answer":    {"", want + " (the hook did not answer within 10s)"},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFakeHelper(t, holding(tt.stderr))
				start := time.Now()
				c := documentsCheck(t, p, f, createHookUpool(p.Repo))
				if took := time.Since(start); took != documentsBound {
					t.Errorf("the check took %s, want its bound, %s", took, documentsBound)
				}
				if c.Result != checkWarn || c.Reason != tt.reason {
					t.Errorf("check = %s, %q; want warn, %q", c.Result, c.Reason, tt.reason)
				}
			})
		})
	}
}

// Where the unified log cannot be read, as from a standard account, the check cannot tell a prompt
// from a stall (decision on #286, 2026-11-04). A dry run the bound ended with no waiting line, and
// one that failed saying it could not read the log, as the hook does at its patience then, each
// warn that macOS may be asking, saying to answer the prompt if it is showing and run doctor
// again, and naming the other causes; neither fails. A denial is still a denial.
func TestDoctorDocumentsAccessWarnsWhenItCannotReadTheLog(t *testing.T) {
	p := newHarnessPool(t)
	unknown := "macOS may be asking whether upool may access Documents, but upool cannot read " +
		"the unified log to tell, which needs an admin account: answer the prompt if it is " +
		"showing, then run upool doctor again; otherwise upool may be denied the Documents " +
		"folder in System Settings → Privacy & Security → Files and Folders, or its git may be " +
		"hung"
	readErr := errors.New("reading the TCC log with /usr/bin/log show: exit status 64")
	t.Run("the bound reached", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newFakeHelper(t, holding(""))
			f.logErr = readErr
			c := documentsCheck(t, p, f, createHookUpool(p.Repo))
			if want := unknown + " (" + readErr.Error() + ")"; c.Result != checkWarn ||
				c.Reason != want {
				t.Errorf("check = %s, %q; want warn, %q", c.Result, c.Reason, want)
			}
		})
	})
	t.Run("the hook failed fast", func(t *testing.T) {
		stalled := "upool: hook worktree-create: git -C " + p.Repo + " worktree list did not " +
			"answer within 2s, and " + hooks.LogUnreadable + " to tell why (it needs an admin " +
			"account: " + readErr.Error() + "): macOS may be asking …\n"
		f := newFakeHelper(t, answering(stalled, output.ExitFailure))
		f.logErr = readErr
		if c := documentsCheck(t, p, f, createHookUpool(p.Repo)); c.Result != checkWarn ||
			c.Reason != unknown {
			t.Errorf("check = %s, %q; want warn, %q", c.Result, c.Reason, unknown)
		}
	})
	t.Run("denied", func(t *testing.T) {
		f := newFakeHelper(t, answering(deniedHookStderr(t, p.Repo), output.ExitFailure))
		f.logErr = readErr
		if c := documentsCheck(t, p, f, createHookUpool(p.Repo)); c.Result != checkFail ||
			!strings.HasPrefix(c.Reason, "macOS denies upool access to Documents") {
			t.Errorf("check = %s, %q; want the denial, failing", c.Result, c.Reason)
		}
	})
}

// The acceptance test of #286, access denied: git under the helper fails with `Operation not
// permitted`, as the capture records it, and the check fails, naming the setting that allows it.
// So it does when the hook wrote the waiting line first: the prompt came up and was answered Don't
// Allow within the bound, and the hook's exit, not its waiting line, says what came of it.
func TestDoctorDocumentsAccessFailsWhenMacOSDeniesTheFolder(t *testing.T) {
	p := newHarnessPool(t)
	want := "macOS denies upool access to Documents: allow it in System Settings → Privacy & " +
		"Security → Files and Folders, then run upool doctor again"
	denied := deniedHookStderr(t, p.Repo)
	for name, stderr := range map[string]string{
		"denied":                        denied,
		"denied after the waiting line": "upool: " + hooks.Waiting("upool") + "\n" + denied,
	} {
		f := newFakeHelper(t, answering(stderr, output.ExitFailure))
		c := documentsCheck(t, p, f, createHookUpool(p.Repo))
		if c.Result != checkFail || c.Reason != want {
			t.Errorf("%s: check = %s, %q; want fail, %q", name, c.Result, c.Reason, want)
		}
	}
}

// deniedHookStderr is what the create hook writes when its git call in repo fails as git did under
// the desktop app's helper, denied the folder (testdata/captures/git/helper-worktree-list.*): the
// dispatcher's line for internal/git's error.
func deniedHookStderr(t *testing.T, repo string) string {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "captures", "git")
	stderr, err := os.ReadFile(filepath.Join(dir, "helper-worktree-list.stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "helper-worktree-list.exit"))
	if err != nil {
		t.Fatal(err)
	}
	exit, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	g := git.Client{Runner: replayGit{git.Result{Stderr: stderr, ExitCode: exit}}}
	_, err = g.WorktreeList(t.Context(), repo)
	if err == nil {
		t.Fatal("the replayed git call succeeded")
	}
	return "upool: hook worktree-create: " + err.Error() + "\n"
}

// replayGit is a git.Runner that answers every call with res.
type replayGit struct{ res git.Result }

func (r replayGit) Run(context.Context, string, []string, ...string) (git.Result, error) {
	return r.res, nil
}

// A hook that fails otherwise, such as on a stall with no prompt, fails the check with the hook's
// own message.
func TestDoctorDocumentsAccessFailsWithTheHooksMessage(t *testing.T) {
	p := newHarnessPool(t)
	msg := "git -C " + p.Repo + " worktree list did not answer within 2s, and macOS is not " +
		"asking whether upool may access Documents: the likely causes are …"
	f := newFakeHelper(t, answering("upool: hook worktree-create: "+msg+"\n", output.ExitFailure))
	c := documentsCheck(t, p, f, createHookUpool(p.Repo))
	if want := "the create hook failed under the desktop app's helper, exit 1: hook " +
		"worktree-create: " + msg; c.Result != checkFail || c.Reason != want {
		t.Errorf("check = %s, %q; want fail, %q", c.Result, c.Reason, want)
	}
}

// runHelper runs the helper as a process, a stand-in that runs its arguments as the real one does,
// in dir with PWD dir, stdin given and stderr returned, and when its context ends it kills the
// whole process group, so that a child still running holds nothing up.
func TestDoctorDocumentsRunHelperRunsTheHookAndEndsItsGroup(t *testing.T) {
	helper := filepath.Join(tempDir(t), "disclaimer")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := tempDir(t)
	env := []string{"PATH=/usr/bin:/bin", "PWD=/elsewhere"}
	stderr, exit, err := runHelper(t.Context(), helper, []string{"/bin/sh", "-c",
		`pwd -P >&2; echo "$PWD" >&2; cat >&2; exit 3`}, env, dir, []byte("stdin\n"))
	if want := dir + "\n" + dir + "\nstdin\n"; err != nil || exit != 3 || string(stderr) != want {
		t.Errorf("runHelper = %q, %d, %v; want %q, 3 and no error", stderr, exit, err, want)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = runHelper(ctx, helper, []string{"/bin/sh", "-c", "sleep 30 & wait"}, env, dir,
		nil)
	if took := time.Since(start); !errors.Is(err, context.DeadlineExceeded) ||
		took > 5*time.Second {
		t.Errorf("runHelper = %v after %s; want the deadline, at once, with the sleep killed", err,
			took)
	}
}

// The acceptance test of #286, no helper: where none of the paths holds the helper, the check is
// skipped, warning and naming where it looked, and runs nothing; so it is when the project
// registers no create hook to run.
func TestDoctorDocumentsAccessSkipsWithoutTheHelper(t *testing.T) {
	p := newHarnessPool(t)
	f := newFakeHelper(t, answering("", 0))
	missing := []string{filepath.Join(tempDir(t), "Claude.app", filepath.FromSlash(helperPath)),
		filepath.Join(tempDir(t), "Applications", "Claude.app", filepath.FromSlash(helperPath))}
	desktop := f.desktop()
	desktop.paths = missing
	inv := &invocation{desktop: desktop, environ: p.Environ, now: time.Now}
	c, ok := documentsAccessCheck(context.Background(), inv, p.Repo, createHookUpool(p.Repo))
	want := "skipped: the Claude desktop app's helper, which runs its sessions' create hooks, " +
		"is not at " + missing[0] + " or " + missing[1]
	if !ok || c.Result != checkWarn || c.Reason != want {
		t.Errorf("check = %t, %s, %q; want warn, %q", ok, c.Result, c.Reason, want)
	}
	c = documentsCheck(t, p, f, "")
	if want := "skipped: " + p.Path(projectSettings) + " registers no upool create hook to " +
		"run: run upool init"; c.Result != checkWarn || c.Reason != want {
		t.Errorf("check = %s, %q; want warn, %q", c.Result, c.Reason, want)
	}
	if runs := f.ran(); len(runs) != 0 {
		t.Errorf("helper runs = %+v, want none", runs)
	}
	inv.desktop = desktopHelper{}
	if _, ok := documentsAccessCheck(context.Background(), inv, p.Repo, "upool"); ok {
		t.Error("the check ran with no helper to look for, as off macOS")
	}
}
