//go:build darwin

package proc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// childEnv, when set, makes the test binary a child for the tests below instead of running them:
// the child waits for its stdin to close and exits, so the test decides how long it lives
// (CONTRIBUTING.md, "Other processes by re-exec"). With holdEnv set to a path too, it first opens
// that file and prints a line once it holds it.
const (
	childEnv = "UPOOL_PROC_TEST_CHILD"
	holdEnv  = "UPOOL_PROC_TEST_HOLD"
)

// held is the file a child holds open until it exits, kept here so that no finalizer closes it.
var held *os.File

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		if path := os.Getenv(holdEnv); path != "" {
			f, err := os.Open(path)
			if err != nil {
				os.Exit(2)
			}
			held = f
			fmt.Println("holding")
		}
		// EOF or an error both mean the parent is done with us; either way, exit.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startChild re-executes the test binary as a child that runs until stdin, which it returns, is
// closed. It also returns the times just before and just after the child was started.
func startChild(t *testing.T) (cmd *exec.Cmd, stdin io.WriteCloser, before, after time.Time) {
	t.Helper()
	cmd = exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	before = time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	after = time.Now()
	t.Cleanup(func() {
		// A test that already reaped the child makes both of these fail harmlessly.
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return cmd, stdin, before, after
}

func TestStartIsWhenTheChildStarted(t *testing.T) {
	cmd, _, before, after := startChild(t)
	start, err := Start(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	// The kernel records the start to the microsecond, so before is truncated to match.
	if start.Before(before.Truncate(time.Microsecond)) || start.After(after) {
		t.Errorf("Start = %v, want between %v and %v", start, before, after)
	}
	if start.Location() != time.UTC {
		t.Errorf("Start location = %v, want UTC", start.Location())
	}
	if ns := start.Nanosecond() % int(time.Microsecond); ns != 0 {
		t.Errorf("Start has %d ns below the microsecond, want 0", ns)
	}
}

func TestAliveFalseForReapedChild(t *testing.T) {
	cmd, stdin, _, _ := startChild(t)
	pid := cmd.Process.Pid
	start, err := Start(pid)
	if err != nil {
		t.Fatal(err)
	}
	if alive, err := Alive(pid, start); err != nil || !alive {
		t.Fatalf("running child: Alive = %v, %v, want true, nil", alive, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if alive, err := Alive(pid, start); err != nil || alive {
		t.Errorf("reaped child: Alive = %v, %v, want false, nil", alive, err)
	}
}

func TestStartOfReapedChildIsErrNoProcess(t *testing.T) {
	cmd, stdin, _, _ := startChild(t)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(cmd.Process.Pid); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Start error = %v, want ErrNoProcess", err)
	}
}

func TestAliveTrueForThisProcess(t *testing.T) {
	start, err := Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if alive, err := Alive(os.Getpid(), start); err != nil || !alive {
		t.Errorf("Alive = %v, %v, want true, nil", alive, err)
	}
}

// TestAliveFalseForWrongStartTime checks that start times compare exactly, at Start's precision: a
// microsecond either way is another process (docs/contracts.md, A7).
func TestAliveFalseForWrongStartTime(t *testing.T) {
	start, err := Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, wrong := range []time.Time{start.Add(time.Microsecond), start.Add(-time.Microsecond)} {
		if alive, err := Alive(os.Getpid(), wrong); err != nil || alive {
			t.Errorf("Alive(start %+v) = %v, %v, want false, nil", wrong.Sub(start), alive, err)
		}
	}
}

func TestSystemAgreesWithStartAndAlive(t *testing.T) {
	var c Checker = System{}
	want, err := Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Start(os.Getpid())
	if err != nil || !got.Equal(want) {
		t.Errorf("System.Start = %v, %v, want %v, nil", got, err, want)
	}
	if alive, err := c.Alive(os.Getpid(), want); err != nil || !alive {
		t.Errorf("System.Alive = %v, %v, want true, nil", alive, err)
	}
}

// Name is the base name of the process's executable, as the kernel records it, cut to 16 bytes:
// the test binary's for a child of it, and ErrNoProcess once the child is reaped.
func TestNameIsTheExecutablesBaseName(t *testing.T) {
	cmd, stdin, _, _ := startChild(t)
	want := filepath.Base(os.Args[0])
	if len(want) > commSize-1 {
		want = want[:commSize-1]
	}
	if got, err := Name(cmd.Process.Pid); err != nil || got != want {
		t.Errorf("Name = %q, %v; want %q", got, err, want)
	}
	if got, err := (System{}).Name(cmd.Process.Pid); err != nil || got != want {
		t.Errorf("System.Name = %q, %v; want %q", got, err, want)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := Name(cmd.Process.Pid); !errors.Is(err, ErrNoProcess) {
		t.Errorf("Name of a reaped child: %v, want ErrNoProcess", err)
	}
}

// unityChild starts a copy of the test binary named Unity, as the kernel names a running Editor,
// as a child with args, holding the file at hold open when hold is not empty. It returns once the
// child holds it, and the child runs until the test ends.
func unityChild(t *testing.T, hold string, args ...string) int {
	t.Helper()
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), editorName)
	if err := os.WriteFile(bin, self, 0o755); err != nil {
		t.Fatal(err)
	}
	return child(t, bin, "", hold, args...)
}

// child starts bin as a child with args, as startChild does, in the directory dir when it is not
// empty, holding the file at hold open when hold is not empty, and returns its PID once it holds
// it.
func child(t *testing.T, bin, dir, hold string, args ...string) int {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), childEnv+"=1", holdEnv+"="+hold)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	if hold != "" {
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "holding\n" {
			t.Fatalf("the child did not hold %s: %q, %v", hold, line, err)
		}
	}
	return cmd.Process.Pid
}

// pidsNamed finds this process by its own name, which shows kp_proc.p_pid read where it lies.
func TestPIDsNamedFindsThisProcess(t *testing.T) {
	name, err := Name(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pids, err := pidsNamed(name)
	if err != nil || !slices.Contains(pids, os.Getpid()) {
		t.Errorf("pidsNamed(%q) = %v, %v; want it to hold this process, %d", name, pids, err,
			os.Getpid())
	}
}

// procArgs reads a child's arguments as it was started with them, and ErrNoProcess once it is
// reaped.
func TestProcArgsAreTheChildsArguments(t *testing.T) {
	pid := child(t, os.Args[0], "", "", "-projectPath", "/some project/dir", "")
	got, err := procArgs(pid)
	want := []string{os.Args[0], "-projectPath", "/some project/dir", ""}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("procArgs = %q, %v; want %q", got, err, want)
	}
	if _, err := procArgs(1 << 30); !errors.Is(err, ErrNoProcess) {
		t.Errorf("procArgs of no process: %v, want ErrNoProcess", err)
	}
}

// vnodePaths lists the files a child holds open, by their real paths.
func TestVnodePathsHoldTheFileAChildOpened(t *testing.T) {
	file := filepath.Join(t.TempDir(), "held")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	pid := child(t, os.Args[0], "", file)
	got, err := vnodePaths(pid)
	if err != nil || !slices.Contains(got, real) {
		t.Errorf("vnodePaths = %q, %v; want it to hold %s", got, err, real)
	}
}

// EditorsAt finds a process named Unity that has the checkout as its -projectpath, whatever the
// flag's case, and one that holds the checkout's Temp/UnityLockfile open, and neither for another
// checkout, nor a process of another name that does the same (A49; #255).
func TestEditorsAtFindsAnEditorByItsArgumentsOrItsLockfile(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	lock := filepath.Join(dir, filepath.FromSlash(lockFile))
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	byArgs := unityChild(t, "", "-projectpath", dir)
	byCase := unityChild(t, "", "-projectPath", dir+"/")
	byLock := unityChild(t, lock)
	child(t, os.Args[0], "", lock, "-projectpath", dir)
	got, err := EditorsAt(dir)
	slices.Sort(got)
	want := []int{byArgs, byCase, byLock}
	slices.Sort(want)
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("EditorsAt(dir) = %v, %v; want %v", got, err, want)
	}
	if got, err := EditorsAt(other); err != nil || len(got) != 0 {
		t.Errorf("EditorsAt(another checkout) = %v, %v; want none", got, err)
	}
	if got, err := (System{}).EditorsAt(dir); err != nil || len(got) != 3 {
		t.Errorf("System.EditorsAt = %v, %v; want the three", got, err)
	}
}

// procCwd reads a child's working directory, by its real path.
func TestProcCwdIsTheChildsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := procCwd(child(t, os.Args[0], dir, "")); err != nil || got != real {
		t.Errorf("procCwd = %q, %v; want %s", got, err, real)
	}
	if _, err := procCwd(1 << 30); !errors.Is(err, ErrNoProcess) {
		t.Errorf("procCwd of no process: %v, want ErrNoProcess", err)
	}
}

// A relative -projectpath names the project in the Editor's own working directory, not in
// upool's: a stand-in started in dir with -projectPath . has dir open, and not the directory this
// test runs in (A49; #255).
func TestEditorsAtReadsARelativeProjectPathFromTheEditorsDirectory(t *testing.T) {
	dir := t.TempDir()
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), editorName)
	if err := os.WriteFile(bin, self, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := child(t, bin, dir, "", "-projectPath", ".")
	if got, err := EditorsAt(dir); err != nil || !slices.Equal(got, []int{pid}) {
		t.Errorf("EditorsAt(the Editor's directory) = %v, %v; want [%d]", got, err, pid)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := EditorsAt(wd); err != nil || slices.Contains(got, pid) {
		t.Errorf("EditorsAt(this test's directory) = %v, %v; want the stand-in left out", got, err)
	}
}
