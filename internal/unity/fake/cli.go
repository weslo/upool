package fake

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// exitBroken is the exit code when the fake cannot answer: it found no script, or the call is
// beyond it. It is a plain failure, so the code under test takes its failure path; a call beyond
// the script also fails the test when it ends (Install).
const exitBroken = 1

// Main is TestMain for a package whose tests use the fake. When the test binary runs as `unity` or
// `claude`, it acts as that CLI and exits with the scripted code; otherwise it runs the tests.
func Main(m *testing.M) {
	if name := filepath.Base(os.Args[0]); name != unityName && name != claudeName {
		os.Exit(m.Run())
	}
	os.Exit(serve())
}

// serve answers one call. It is the fake CLI's main, so it is where the fake reads its arguments,
// environment and working directory, and writes its streams.
func serve() int {
	a, err := reply()
	if err != nil {
		// stderr is the only place to say it.
		_, _ = fmt.Fprintf(os.Stderr, "fake %s: %v\n", filepath.Base(os.Args[0]), err)
		return exitBroken
	}
	// A failed write is the caller's to notice, through the output it did not get.
	_, _ = os.Stdout.Write(a.Stdout)
	_, _ = os.Stderr.Write(a.Stderr)
	return a.Exit
}

// reply records this call in the log and returns the script's answer for it.
func reply() (answer, error) {
	// The symlink Install made, whether it was run by its path or found on PATH, as a shell does.
	self, err := exec.LookPath(os.Args[0])
	if err != nil {
		return answer{}, fmt.Errorf("finding the command this ran as: %w", err)
	}
	dir := filepath.Dir(self)
	raw, err := os.ReadFile(filepath.Join(dir, scriptFile))
	if err != nil {
		return answer{}, fmt.Errorf("reading the script fake.Install writes: %w", err)
	}
	var script []answer
	if err := json.Unmarshal(raw, &script); err != nil {
		return answer{}, fmt.Errorf("reading %s: %w", filepath.Join(dir, scriptFile), err)
	}

	wd, err := os.Getwd()
	if err != nil {
		return answer{}, fmt.Errorf("reading the working directory: %w", err)
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return answer{}, fmt.Errorf("resolving the working directory: %w", err)
	}
	projectPath, set := os.LookupEnv("UNITY_PROJECT_PATH")
	call := Call{Args: os.Args[1:], ProjectPath: projectPath, ProjectPathSet: set, Dir: wd}

	n, err := record(filepath.Join(dir, logFile), call)
	if err != nil {
		return answer{}, err
	}
	if n >= len(script) {
		return answer{}, fmt.Errorf("call %d is beyond the script's %d replies: %s %s",
			n+1, len(script), filepath.Base(os.Args[0]), strings.Join(call.Args, " "))
	}
	return script[n], nil
}

// record appends c to the log at path and returns how many calls the log held before it, which is
// the index of its reply. It holds an exclusive lock on the log while it counts and appends, so
// calls made at the same time never take the same reply.
func record(path string, c Call) (int, error) {
	line, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := appendLocked(f, append(line, '\n'))
	if err != nil {
		_ = f.Close() // the first error is the one worth reporting
		return 0, fmt.Errorf("recording the call in %s: %w", path, err)
	}
	// Closing f releases the lock.
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("recording the call in %s: %w", path, err)
	}
	return n, nil
}

// appendLocked takes an exclusive flock on f, counts the lines already in it, and appends line.
func appendLocked(f *os.File, line []byte) (int, error) {
	if err := flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	before, err := io.ReadAll(f)
	if err != nil {
		return 0, err
	}
	if _, err := f.Write(line); err != nil {
		return 0, err
	}
	return bytes.Count(before, []byte("\n")), nil
}

// flock is flock(2), retried when a signal interrupts the wait.
func flock(fd, how int) error {
	for {
		err := syscall.Flock(fd, how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
