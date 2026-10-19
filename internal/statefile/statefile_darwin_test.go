//go:build darwin

package statefile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// childEnv, when set to a home directory, makes the test binary a child for the tests below instead
// of running them: the child takes the lock under that home, prints "locked", holds the lock until
// its stdin closes, then releases it and exits (CONTRIBUTING.md, "Other processes by re-exec").
const childEnv = "UPOOL_STATEFILE_TEST_CHILD"

func TestMain(m *testing.M) {
	if home := os.Getenv(childEnv); home != "" {
		os.Exit(child(home))
	}
	os.Exit(m.Run())
}

func child(home string) int {
	l, err := Lock(context.Background(), home)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("locked")
	// EOF or an error both mean the parent is done with us; either way, let go.
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := l.Unlock(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// holdingChild re-executes the test binary as a child that holds the lock under home, and returns
// once the child has it. Closing the returned stdin makes the child release the lock; wait then
// reaps the child and fails the test unless it exited 0.
func holdingChild(t *testing.T, home string) (stdin io.WriteCloser, wait func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"="+home)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
	var once sync.Once
	wait = func() {
		once.Do(func() {
			_ = stdin.Close()                  // closing twice fails harmlessly
			_, _ = io.Copy(io.Discard, stdout) // the child prints nothing more
			if err := cmd.Wait(); err != nil {
				t.Errorf("child: %v, stderr %q", err, stderr.String())
			}
		})
	}
	t.Cleanup(wait)
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		_ = stdin.Close() // the child is reaped by wait; the failure is the line
		t.Fatalf("child printed %q, %v, want %q", line, err, "locked\n")
	}
	return stdin, wait
}

// TestLockExcludesSecondProcess is issue #36's acceptance test: while a child process holds
// ~/.upool/lock, this process cannot take it, and its Lock waits until the child releases it.
func TestLockExcludesSecondProcess(t *testing.T) {
	home := t.TempDir()
	stdin, wait := holdingChild(t, home)

	if l, err := Lock(cancelled(), home); !errors.Is(err, context.Canceled) {
		if err == nil {
			_ = l.Unlock() // the lock should never have been ours; the error below says so
		}
		t.Fatalf("Lock while the child holds it: error = %v, want context.Canceled", err)
	}

	var released atomic.Bool
	type result struct {
		l        *Locked
		err      error
		released bool
	}
	got := make(chan result, 1)
	go func() {
		l, err := Lock(context.Background(), home)
		got <- result{l, err, released.Load()}
	}()
	released.Store(true)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.err != nil {
		t.Fatalf("Lock error = %v, want nil", r.err)
	}
	if err := r.l.Unlock(); err != nil {
		t.Error(err)
	}
	if !r.released {
		t.Error("Lock returned before the child was told to release the lock, want it to wait")
	}
	wait()
}

// TestGivingUpLeavesNoLockBehind checks that a Lock whose context ends while it waits holds nothing
// afterwards: once the holder lets go, the next Lock gets the lock.
func TestGivingUpLeavesNoLockBehind(t *testing.T) {
	home := t.TempDir()
	held, err := Lock(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := Lock(cancelled(), home); !errors.Is(err, context.Canceled) {
		if err == nil {
			_ = l.Unlock() // the lock should never have been ours; the error below says so
		}
		t.Fatalf("Lock while held: error = %v, want context.Canceled", err)
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	l, err := Lock(context.Background(), home)
	if err != nil {
		t.Fatalf("Lock after the holder let go: error = %v, want nil", err)
	}
	if err := l.Unlock(); err != nil {
		t.Error(err)
	}
}

// TestConcurrentUpdatesLoseNothing runs many read-modify-writes at once, each adding one to a
// counter. Each Update opens the lock file anew, so they exclude one another as separate processes
// would; a lost update leaves the counter short.
func TestConcurrentUpdatesLoseNothing(t *testing.T) {
	home := t.TempDir()
	increment := func(old []byte) ([]byte, error) {
		count := 0
		if len(old) > 0 {
			var err error
			if count, err = strconv.Atoi(string(old)); err != nil {
				return nil, err
			}
		}
		return []byte(strconv.Itoa(count + 1)), nil
	}
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if err := Update(context.Background(), home, "editors.json", increment); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, err := Read(home, "editors.json")
	if err != nil || string(got) != strconv.Itoa(n) {
		t.Errorf("counter = %q, %v, want %q, nil", got, err, strconv.Itoa(n))
	}
}
