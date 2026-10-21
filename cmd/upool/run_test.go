package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
)

// The tests of `upool run`, on the harness pool, with /bin/sh as the command: the shell is what an
// agent's launcher most often runs, and it is on every machine CI uses.

// sh is the shell the tests' commands run in.
const sh = "/bin/sh"

// signalSource stands in for signal.Notify and signal.Stop: it hands the channel run asks for
// signals on to the test, and records that run stopped asking.
type signalSource struct {
	asked   chan chan<- os.Signal
	mu      sync.Mutex
	stopped bool
}

func newSignalSource() *signalSource {
	return &signalSource{asked: make(chan chan<- os.Signal, 1)}
}

func (s *signalSource) notify(c chan<- os.Signal) { s.asked <- c }

func (s *signalSource) stop(chan<- os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
}

// runUpool runs `upool <args>` in p from dir as runIn does, with stdout written to out and signals
// from src.
func runUpool(t *testing.T, p harnessPool, dir string, out io.Writer, src *signalSource,
	args ...string) result {
	t.Helper()
	var errOut bytes.Buffer
	d := dispatcher{
		commands:   commandTable,
		hooks:      hookTable,
		stdin:      strings.NewReader(""),
		stdout:     out,
		stderr:     &errOut,
		getenv:     p.Getenv,
		environ:    p.Environ,
		getwd:      func() (string, error) { return dir, nil },
		now:        func() time.Time { return clock },
		pid:        os.Getpid(),
		executable: os.Executable,
		notify:     src.notify,
		stopNotify: src.stop,
	}
	code := d.run(args)
	return result{code: code, stdout: "", stderr: errOut.String()}
}

// runSh runs `upool run <branch> -- /bin/sh -c script` in p's main checkout, and returns what it
// printed, stdout included.
func runSh(t *testing.T, p harnessPool, branch, script string, flags ...string) result {
	t.Helper()
	var out bytes.Buffer
	args := append(append([]string{"run", branch}, flags...), "--", sh, "-c", script)
	r := runUpool(t, p, p.Repo, &out, newSignalSource(), args...)
	r.stdout = out.String()
	return r
}

// slotState returns p's slot name as state.json has it.
func slotState(t *testing.T, p harnessPool, name string) pool.Slot {
	t.Helper()
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pl.Slots {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("state.json has no %s", name)
	return pool.Slot{}
}

// checkReleased fails the test unless p's slot name is in state, with no lease.
func checkReleased(t *testing.T, p harnessPool, name string, state pool.State) {
	t.Helper()
	if s := slotState(t, p, name); s.State != state || s.Lease != nil {
		t.Errorf("%s: %s, lease %+v; want %s and no lease", name, s.State, s.Lease, state)
	}
}

// The acceptance test of #70, its first half: the command runs in the slot, with
// UNITY_PROJECT_PATH naming it and a lease that run's own process holds, its output is upool's
// stdout and nothing else is, and a clean exit returns the slot to ready, the branch kept.
func TestRunRunsTheCommandInTheSlot(t *testing.T) {
	p := newHarnessPool(t)
	out := tempDir(t)
	p.Setenv("OUT", out)
	p.Setenv("KEY", p.key)
	p.Setenv("UNITY_PROJECT_PATH", "/elsewhere")
	r := runSh(t, p, "feature", `pwd -P > "$OUT/cwd"
printf %s "$UNITY_PROJECT_PATH" > "$OUT/variable"
printf %s "$PWD" > "$OUT/pwd"
/bin/cat "$HOME/.upool/pools/$KEY/state.json" > "$OUT/state.json"
echo hello`)
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if r.stdout != "hello\n" {
		t.Errorf("stdout = %q, want the command's alone, %q", r.stdout, "hello\n")
	}
	slot := p.slot("slot-1")
	for file, want := range map[string]string{"cwd": slot + "\n", "variable": slot, "pwd": slot} {
		if b, err := os.ReadFile(filepath.Join(out, file)); err != nil || string(b) != want {
			t.Errorf("the command's %s = %q, %v; want %q", file, b, err, want)
		}
	}

	b, err := os.ReadFile(filepath.Join(out, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var during pool.Pool
	if err := json.Unmarshal(b, &during); err != nil {
		t.Fatal(err)
	}
	l := during.Slots[0].Lease
	if during.Slots[0].State != pool.StateLeased || l == nil || !l.Confirmed {
		t.Fatalf("slot-1 while the command ran = %+v, want leased and confirmed",
			during.Slots[0])
	}
	if runtime.GOOS == "darwin" {
		if l.HolderPID == nil || *l.HolderPID != os.Getpid() || l.HolderPIDStart == nil {
			t.Errorf("lease holder = %v, %v; want upool's own process, %d, with its start time",
				l.HolderPID, l.HolderPIDStart, os.Getpid())
		}
	} else if l.HolderPID != nil || l.HolderPIDStart != nil {
		t.Errorf("lease holder = %v, %v; want none where start times are unsupported",
			l.HolderPID, l.HolderPIDStart)
	}

	checkReleased(t, p, "slot-1", pool.StateReady)
	if !strings.Contains(r.stderr, "upool: released slot-1: ready") {
		t.Errorf("stderr = %q, want it to say slot-1 was released", r.stderr)
	}
	if got := p.Git("branch", "--list", "feature"); strings.TrimSpace(got) != "feature" {
		t.Errorf("git branch --list feature = %q, want the branch kept", got)
	}
	head := strings.TrimSpace(p.Git("-C", slot, "rev-parse", "--abbrev-ref", "HEAD"))
	if head != "HEAD" {
		t.Errorf("slot-1 is on %q, want detached at base", head)
	}
}

// The acceptance test of #70, its second half: a command that leaves an untracked file leaves the
// slot parked, the file kept, and --json reports it.
func TestRunParksTheSlotTheCommandLeftDirty(t *testing.T) {
	p := newHarnessPool(t)
	r := runSh(t, p, "feature", `echo work > new.txt`, "--json")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	env := decode(t, r.stdout)
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got runReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := runReport{Path: p.slot("slot-1"), Slot: "slot-1", Branch: "feature",
		LeaseID: got.LeaseID, Repo: p.Repo, Command: []string{sh, "-c", "echo work > new.txt"},
		Status: 0, State: pool.StateParked}
	if !env.Success || string(mustJSON(t, got)) != string(mustJSON(t, want)) || got.LeaseID == "" {
		t.Errorf("envelope = %+v, data %s; want success and %s", env, mustJSON(t, got),
			mustJSON(t, want))
	}
	checkReleased(t, p, "slot-1", pool.StateParked)
	if b, err := os.ReadFile(filepath.Join(p.slot("slot-1"), "new.txt")); err != nil ||
		string(b) != "work\n" {
		t.Errorf("new.txt = %q, %v; want the command's file kept", b, err)
	}
}

// upool ends with the command's exit status, a signal's as a shell reports it, and still releases
// the slot.
func TestRunExitsWithTheCommandsStatus(t *testing.T) {
	for _, tt := range []struct {
		script string
		want   int
	}{
		{"exit 3", 3},
		{"kill -KILL $$", signalStatusBase + int(syscall.SIGKILL)},
	} {
		t.Run(tt.script, func(t *testing.T) {
			p := newHarnessPool(t)
			r := runSh(t, p, "feature", tt.script)
			if r.code != tt.want {
				t.Errorf("exit = %d, want %d; stderr %q", r.code, tt.want, r.stderr)
			}
			if want := "exited with status " + strconv.Itoa(tt.want); !strings.Contains(r.stderr,
				want) {
				t.Errorf("stderr = %q, want it to say %q", r.stderr, want)
			}
			checkReleased(t, p, "slot-1", pool.StateReady)
		})
	}
}

// readyWriter is a stdout that says when the command has printed "ready".
type readyWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *readyWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(b)
	if strings.Contains(w.buf.String(), "ready\n") {
		w.once.Do(func() { close(w.ready) })
	}
	return n, err
}

// SIGINT and SIGTERM that reach upool while the command runs go to the command, and run stops
// taking them once it is done. The command loops on short sleeps rather than `wait`ing on a
// background job: bash 3.2, macOS's /bin/sh, runs the trap for a signal that lands just before
// `wait` begins only when the job exits, which a 60 s job turned into a test timeout (#195).
func TestRunForwardsSignals(t *testing.T) {
	for _, tt := range []struct {
		sig  syscall.Signal
		want int
	}{{syscall.SIGTERM, 7}, {syscall.SIGINT, 8}} {
		t.Run(tt.sig.String(), func(t *testing.T) {
			p := newHarnessPool(t)
			src := newSignalSource()
			w := &readyWriter{ready: make(chan struct{})}
			done := make(chan result, 1)
			go func() {
				done <- runUpool(t, p, p.Repo, w, src, "run", "feature", "--", sh, "-c",
					`trap 'exit 7' TERM
trap 'exit 8' INT
echo ready
while :; do /bin/sleep 0.1; done`)
			}()
			timeout := time.After(30 * time.Second)
			var signals chan<- os.Signal
			select {
			case signals = <-src.asked:
			case r := <-done:
				t.Fatalf("run returned %d before asking for signals; stderr %q", r.code, r.stderr)
			case <-timeout:
				t.Fatal("run never asked for signals")
			}
			select {
			case <-w.ready:
			case r := <-done:
				t.Fatalf("run returned %d before the command was ready; stderr %q", r.code,
					r.stderr)
			case <-timeout:
				t.Fatal("the command never said it was ready")
			}
			signals <- tt.sig
			var r result
			select {
			case r = <-done:
			case <-timeout:
				t.Fatal("run did not return after the signal")
			}
			if r.code != tt.want {
				t.Errorf("exit = %d, want %d, the command's trap; stderr %q", r.code, tt.want,
					r.stderr)
			}
			src.mu.Lock()
			defer src.mu.Unlock()
			if !src.stopped {
				t.Error("run did not stop taking signals")
			}
			checkReleased(t, p, "slot-1", pool.StateReady)
		})
	}
}

// A command that cannot start fails run, and the slot it took is released.
func TestRunReleasesTheSlotWhenTheCommandCannotStart(t *testing.T) {
	for _, tt := range []struct{ command, want string }{
		{"no-such-command", "no-such-command is not on PATH"},
		{"/no/such/command", "starting /no/such/command"},
	} {
		t.Run(tt.command, func(t *testing.T) {
			p := newHarnessPool(t)
			var out bytes.Buffer
			r := runUpool(t, p, p.Repo, &out, newSignalSource(), "run", "feature", "--",
				tt.command)
			if r.code != output.ExitFailure || !strings.Contains(r.stderr, tt.want) {
				t.Errorf("exit = %d, stderr %q; want %d and %q", r.code, r.stderr,
					output.ExitFailure, tt.want)
			}
			checkReleased(t, p, "slot-1", pool.StateReady)
		})
	}
}

// run needs a branch name, a "--" and a command, in that order, and takes no slot without them.
func TestRunRefusesItsArguments(t *testing.T) {
	p := newHarnessPool(t)
	for _, args := range [][]string{
		{"run"},
		{"run", "feature"},
		{"run", "feature", "--"},
		{"run", "--", sh},
		{"run", "feature", sh},
		{"run", "feature", "extra", "--", sh},
	} {
		var out bytes.Buffer
		r := runUpool(t, p, p.Repo, &out, newSignalSource(), args...)
		if r.code != output.ExitFailure || !strings.Contains(r.stderr, "usage: upool "+runUsage) {
			t.Errorf("%q: exit = %d, stderr %q; want %d and the usage", args, r.code, r.stderr,
				output.ExitFailure)
		}
		for _, name := range []string{"slot-1", "slot-2"} {
			checkReleased(t, p, name, pool.StateReady)
		}
	}
}
