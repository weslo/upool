package fake

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/weslo/upool/internal/pathvars"
)

func TestMain(m *testing.M) {
	Main(m)
}

// tempDir is t.TempDir() with symlinks resolved: on macOS it is under /var, which is a symlink to
// /private/var.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// capture reads a capture under testdata/captures/unity/ and fills its variables.
func capture(t *testing.T, file string, vars ...pathvars.Var) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "captures", "unity", file))
	if err != nil {
		t.Fatal(err)
	}
	filled, err := pathvars.Fill(string(raw), vars...)
	if err != nil {
		t.Fatal(err)
	}
	return filled
}

// command is `unity args…` run from dir as a shell would run it with u.PATH: the first `unity` on
// that PATH, argv[0] the bare name, HOME a temporary directory, and env added to that.
func command(t *testing.T, u *Unity, dir string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	for _, d := range filepath.SplitList(u.PATH) {
		path := filepath.Join(d, "unity")
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return &exec.Cmd{
				Path: path,
				Args: append([]string{"unity"}, args...),
				Env:  append([]string{"PATH=" + u.PATH, "HOME=" + tempDir(t)}, env...),
				Dir:  dir,
			}
		}
	}
	t.Fatalf("no unity on PATH %s", u.PATH)
	return nil
}

// result runs cmd and returns its exit code, stdout and stderr; err is set only when it did not
// run to an exit.
func result(cmd *exec.Cmd) (code int, stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), out.String(), errOut.String(), nil
	}
	if err != nil {
		return -1, out.String(), errOut.String(), err
	}
	return 0, out.String(), errOut.String(), nil
}

// mustRun is result, failing the test when cmd did not run to an exit.
func mustRun(t *testing.T, cmd *exec.Cmd) (code int, stdout, stderr string) {
	t.Helper()
	code, stdout, stderr, err := result(cmd)
	if err != nil {
		t.Fatalf("running %q: %v", cmd.Args, err)
	}
	return code, stdout, stderr
}

func TestReplaysCaptureAndExitCode(t *testing.T) {
	// docs/contracts.md (d): STATUS_NO_INSTANCES comes with exit 6.
	u := Install(t, Reply{Capture: "status-until-ready-no-instances.json", Exit: 6})
	project, cwd := tempDir(t), tempDir(t)

	cmd := command(t, u, cwd, []string{"UNITY_PROJECT_PATH=" + project}, "status", "--until-ready")
	code, stdout, stderr := mustRun(t, cmd)

	if code != 6 {
		t.Errorf("exit = %d, want 6", code)
	}
	if want := capture(t, "status-until-ready-no-instances.json"); stdout != want {
		t.Errorf("stdout = %q, want the capture %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := []Call{{
		Args:           []string{"status", "--until-ready"},
		ProjectPath:    project,
		ProjectPathSet: true,
		Dir:            cwd,
	}}
	if got := u.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %+v, want %+v", got, want)
	}
}

func TestRepliesInOrderWithVariablesFilledAndStderr(t *testing.T) {
	spike := pathvars.Var{Name: "spike", Path: tempDir(t)}
	clone := filepath.Join(spike.Path, "clone1")
	u := Install(t,
		Reply{Capture: "close-gui.json", Vars: []pathvars.Var{spike}, Exit: 0},
		Reply{Capture: "status-until-ready-no-instances.json", Exit: 6, Stderr: "waited 15s\n"},
	)
	cwd := tempDir(t)

	code, stdout, stderr := mustRun(t, command(t, u, cwd, nil, "close", clone))
	if code != 0 {
		t.Errorf("first call: exit = %d, want 0", code)
	}
	if want := capture(t, "close-gui.json", spike); stdout != want {
		t.Errorf("first call: stdout = %q, want the filled capture %q", stdout, want)
	}
	if !strings.Contains(stdout, `"project": "`+clone+`"`) {
		t.Errorf("first call: stdout = %q, want ${spike} filled with %s", stdout, spike.Path)
	}
	if stderr != "" {
		t.Errorf("first call: stderr = %q, want empty", stderr)
	}

	code, stdout, stderr = mustRun(t, command(t, u, cwd, nil, "status"))
	if code != 6 {
		t.Errorf("second call: exit = %d, want 6", code)
	}
	if want := capture(t, "status-until-ready-no-instances.json"); stdout != want {
		t.Errorf("second call: stdout = %q, want the capture %q", stdout, want)
	}
	if stderr != "waited 15s\n" {
		t.Errorf("second call: stderr = %q, want %q", stderr, "waited 15s\n")
	}

	want := []Call{
		{Args: []string{"close", clone}, Dir: cwd},
		{Args: []string{"status"}, Dir: cwd},
	}
	if got := u.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %+v, want %+v (UNITY_PROJECT_PATH unset)", got, want)
	}
}

func TestRunByPathFindsItsScript(t *testing.T) {
	u := Install(t, Reply{Capture: "status-until-ready-no-instances.json", Exit: 6})
	cmd := command(t, u, tempDir(t), nil, "status")
	cmd.Args[0] = cmd.Path

	if code, _, stderr := mustRun(t, cmd); code != 6 {
		t.Errorf("exit = %d, want 6; stderr %q", code, stderr)
	}
	if n := len(u.Calls()); n != 1 {
		t.Errorf("calls = %d, want 1", n)
	}
}

// recordingT keeps the errors and cleanups of the testing.TB it wraps, so a test can check what
// Install's cleanup reports without failing itself.
type recordingT struct {
	testing.TB
	errors   []string
	cleanups []func()
}

func (r *recordingT) Error(args ...any) { r.errors = append(r.errors, fmt.Sprint(args...)) }
func (r *recordingT) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recordingT) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func TestCallBeyondTheScriptFailsTheTest(t *testing.T) {
	rt := &recordingT{TB: t}
	u := Install(rt, Reply{Capture: "status-until-ready-no-instances.json", Exit: 6})
	cwd := tempDir(t)
	mustRun(t, command(t, u, cwd, nil, "status"))

	code, stdout, stderr := mustRun(t, command(t, u, cwd, nil, "close", cwd))
	if code != exitBroken {
		t.Errorf("exit = %d, want %d", code, exitBroken)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := "fake unity: call 2 is beyond the script's 1 replies: unity close " + cwd + "\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}

	for i := len(rt.cleanups) - 1; i >= 0; i-- {
		rt.cleanups[i]()
	}
	const called = "unity called 2 times, want at most 1"
	if len(rt.errors) != 1 || !strings.Contains(rt.errors[0], called) {
		t.Errorf("errors = %q, want one saying %q", rt.errors, called)
	}
}

// Calls made at the same time each take their own reply. Fewer calls than this rarely overlap
// enough to catch two taking the same one.
func TestConcurrentCallsTakeEachReplyOnce(t *testing.T) {
	const n = 32
	replies := make([]Reply, n)
	want := make([]int, n)
	for i := range replies {
		replies[i] = Reply{Capture: "status-until-ready-no-instances.json", Exit: i}
		want[i] = i
	}
	u := Install(t, replies...)
	cwd := tempDir(t)
	cmds := make([]*exec.Cmd, n)
	for i := range cmds {
		cmds[i] = command(t, u, cwd, nil, "status")
	}

	codes := make([]int, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, cmd := range cmds {
		wg.Go(func() { codes[i], _, _, errs[i] = result(cmd) })
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	slices.Sort(codes)
	if !slices.Equal(codes, want) {
		t.Errorf("exit codes = %v, want each of %v once", codes, want)
	}
	if got := len(u.Calls()); got != n {
		t.Errorf("calls = %d, want %d", got, n)
	}
}
