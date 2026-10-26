//go:build darwin

package proc

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// childEnv, when set, makes the test binary a child for the tests below instead of running them:
// the child waits for its stdin to close and exits, so the test decides how long it lives
// (CONTRIBUTING.md, "Other processes by re-exec").
const childEnv = "UPOOL_PROC_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
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
