package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
)

// runUsage is run's usage line, which its handler also names when it refuses its arguments.
const runUsage = "run <name> [--lane script|scene] [--json] -- <cmd...>"

// runCommand is `upool run <name> -- <cmd...>`, the agent-agnostic launcher: it takes a slot as
// `take <name>` does, with its own process as the lease's holder, runs the command in the slot
// with UNITY_PROJECT_PATH set to it, releases the slot when the command exits, and ends with the
// command's exit status (docs/design.md, "One binary, a dozen commands, one JSON file per pool",
// the `run` row; docs/contracts.md (f), transition 5 or 19, then 11 or 12). For `claude -p`
// sessions, which fire no WorktreeRemove, and for agents with no worktree event at all, its
// release is the only one (docs/design.md, "Environment, confirmation and release"). --lane is
// take's, passed on.
var runCommand = command{
	usage: runUsage,
	setup: func(fs *flag.FlagSet) handler {
		lane := fs.String("lane", pool.LaneScript, "the lease's lane: script, or scene, which "+
			"one lease of the project holds at a time")
		return func(inv *invocation) (any, []string, error) { return runInSlot(inv, *lane) }
	},
}

// signalStatusBase is what run adds to the number of the signal that ended its command to make the
// status it exits with, as a shell reports such a command's status.
const signalStatusBase = 128

// runReport is what run did: the data of its --json envelope. Its human form is empty, since
// stdout is the command's; run says what it took and released on stderr.
type runReport struct {
	Path    string `json:"path"`
	Slot    string `json:"slot"`
	Branch  string `json:"branch"`
	LeaseID string `json:"lease_id"`
	Repo    string `json:"repo"`
	// Command is the command run ran, its name first.
	Command []string `json:"command"`
	// Status is the command's exit status, which run exits with; 0 if it did not start.
	Status int `json:"status"`
	// State is what release left the slot as: ready, or parked when the command left it dirty;
	// empty if the release failed.
	State pool.State `json:"state,omitempty"`
}

// Human returns nothing: the command's output is what stdout holds.
func (runReport) Human() string { return "" }

// runInSlot is run's handler, taking the slot in lane. Whatever becomes of the command, it
// releases the slot afterwards, with --if-lease-id of its own lease, so that it never releases a
// lease another process holds.
func runInSlot(inv *invocation, lane string) (any, []string, error) {
	if inv.dash != 1 || len(inv.args) < 2 || inv.args[0] == "" {
		return nil, nil, fmt.Errorf("run needs a branch name, then -- and the command to run; "+
			"usage: upool %s", runUsage)
	}
	name, argv := inv.args[0], inv.args[1:]
	opts := takeOptions{lane: lane}
	pid := inv.pid
	switch start, err := (proc.System{}).Start(pid); {
	case err == nil:
		opts.holderPID, opts.holderStart = &pid, &start
	case !errors.Is(err, errors.ErrUnsupported):
		return nil, nil, fmt.Errorf("reading the start time of upool's own process: %w", err)
	}
	// Off macOS process start times are unsupported (internal/proc), so the lease records no
	// holder, as a bare take's does, and is never reclaimed: only this release ends it.

	t, err := takeSlot(inv, name, opts)
	if err != nil {
		return nil, nil, err
	}
	say(inv.stderr, fmt.Sprintf("running %s in %s, %s, on branch %s", argv[0], t.Slot, t.Path,
		t.Branch))
	status, runErr := runChild(inv, t.Path, argv)

	r := runReport{Path: t.Path, Slot: t.Slot, Branch: t.Branch, LeaseID: t.LeaseID,
		Repo: t.Repo, Command: argv, Status: status}
	rel := *inv
	rel.args = []string{t.Path}
	data, warnings, relErr := release(&rel, t.LeaseID, false)
	if relErr != nil {
		relErr = fmt.Errorf("releasing %s: %w; release it with upool release %s", t.Slot,
			relErr, t.Path)
	} else if rr, ok := data.(releaseReport); ok {
		r.State = rr.State
		say(inv.stderr, rr.Human())
	}
	switch {
	case runErr != nil:
		return r, warnings, errors.Join(runErr, relErr)
	case status != output.ExitOK && relErr != nil:
		return r, warnings, output.Exited(status, "%s exited with status %d, and %v", argv[0],
			status, relErr)
	case status != output.ExitOK:
		return r, warnings, output.Exited(status, "%s exited with status %d", argv[0], status)
	case relErr != nil:
		return r, warnings, relErr
	}
	return r, warnings, nil
}

// runChild runs argv in dir with upool's standard streams and its environment, UNITY_PROJECT_PATH
// and PWD set to dir, and returns its exit status. A name without a slash is looked up on PATH;
// one with a slash is taken as it is, relative to dir. SIGINT and SIGTERM that reach upool while
// the command runs go to the command instead. A command started in the same process group as upool
// also gets a SIGINT the terminal sends to the group itself, so it may see that one twice.
func runChild(inv *invocation, dir string, argv []string) (int, error) {
	path := argv[0]
	if !strings.ContainsRune(path, os.PathSeparator) {
		found, ok := lookPath(path, inv.getenv("PATH"))
		if !ok {
			return 0, fmt.Errorf("%s is not on PATH", path)
		}
		path = found
	}
	cmd := &exec.Cmd{Path: path, Args: argv, Dir: dir, Env: childEnv(inv.environ(), dir),
		Stdin: inv.stdin, Stdout: inv.stdout, Stderr: inv.stderr}

	signals := make(chan os.Signal, 1)
	if inv.notify != nil {
		inv.notify(signals)
		defer inv.stopNotify(signals)
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-signals:
				// A command that has just exited cannot take the signal, and needs none.
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	err := cmd.Wait()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return output.ExitOK, nil
	case errors.As(err, &exitErr):
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return signalStatusBase + int(ws.Signal()), nil
		}
		return exitErr.ExitCode(), nil
	}
	return 0, fmt.Errorf("running %s: %w", argv[0], err)
}

// childEnv returns environ, "KEY=value" entries, with UNITY_PROJECT_PATH and PWD set to dir: the
// variable targets the slot's Editor (docs/contracts.md (c)), and PWD keeps a shell's idea of its
// directory true.
func childEnv(environ []string, dir string) []string {
	env := make([]string, 0, len(environ)+2)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "UNITY_PROJECT_PATH=") && !strings.HasPrefix(kv, "PWD=") {
			env = append(env, kv)
		}
	}
	return append(env, "UNITY_PROJECT_PATH="+dir, "PWD="+dir)
}
