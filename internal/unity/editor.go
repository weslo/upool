package unity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/proc"
)

// The wrappers around an Editor's life that `upool open`, `upool close`, refresh's re-warm and the
// clone recipe's stale-seed refresh share (docs/design.md, "Repository, CI and Homebrew
// distribution": internal/unity wraps open, status --until-ready and close).

// The waits for an Editor to be ready (docs/contracts.md A37). An Editor charged as a cold import
// gets ColdReadyTimeout, and any other ReadyTimeout: GUI Editors were ready in 11 to 21 s and
// headless ones within 57 s on warm Libraries, and a cold import took 235 to 259 s (spike §8 C.1,
// B.1; spike §2 T5.5).
const (
	ReadyTimeout     = 120 * time.Second
	ColdReadyTimeout = 300 * time.Second
)

// The waits of the wrappers below.
const (
	// launchTimeout bounds `unity open`, which returns once it has launched the Editor.
	launchTimeout = time.Minute
	// statusGrace is how long WaitReady gives `unity status --until-ready` past its own --timeout
	// before it ends the call, so that a CLI that hangs cannot hang the caller.
	statusGrace = 30 * time.Second
	// editorPollInterval is how often WaitReady checks that the Editor is still running while it
	// waits for it to be ready.
	editorPollInterval = time.Second
	// closeTimeout bounds `unity close`, which returns once the Editor has exited: a graceful close
	// took 0.99 s after a save, and 12.1 s with a scene left dirty (spike §8 C.4, C.5).
	closeTimeout = 2 * time.Minute
	// ExitTimeout is how long WaitGone waits for an Editor's process to be gone once `unity close`
	// has returned, checking every ExitPollInterval.
	ExitTimeout      = 30 * time.Second
	ExitPollInterval = time.Second
)

// Launch runs `unity open <project>`, with `--args -batchmode` for a headless Editor (A33), and
// returns the PID it launched, launchedPid in stdout's envelope (A12), and the CLI's warnings,
// such as its Hub-registry warning, which it repeats in the envelope.
func (c Client) Launch(ctx context.Context, project string, headless bool) (int, []string,
	error) {
	ctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	var args []string
	if headless {
		args = []string{"--args", "-batchmode"}
	}
	reply, err := c.Call(ctx, VerbOpen, project, args...)
	if err == nil {
		err = reply.Err()
	}
	if err != nil {
		return 0, nil, err
	}
	var data struct {
		LaunchedPID int `json:"launchedPid"`
	}
	if json.Unmarshal(reply.Response.Data, &data) != nil || data.LaunchedPID < 1 {
		return 0, reply.Response.Warnings, output.Fail(output.CodeCommandFailed,
			"unity open %s reported no launchedPid", project)
	}
	return data.LaunchedPID, reply.Response.Warnings, nil
}

// NotReadyError is WaitReady's error for an Editor that was not ready when the wait ran out and
// whose process, as far as could be told, was still running.
type NotReadyError struct {
	Project string
	Timeout time.Duration
	// Err is the status call's error.
	Err error
}

func (e *NotReadyError) Error() string {
	return fmt.Sprintf("the Editor of %s was not ready after %s: %v", e.Project, e.Timeout, e.Err)
}

func (e *NotReadyError) Unwrap() error { return e.Err }

// WaitReady waits for the Editor at pid to be ready in project: `unity status --until-ready
// --timeout <timeout>`, which waits the whole timeout when no Editor comes up, while it checks
// the Editor's process every editorPollInterval, so that an Editor that exits fails at once rather
// than at the timeout (docs/design.md, the lifecycle paragraph). An Editor that is not ready in
// time fails with a *NotReadyError, which wraps the status call's failure, exit 6 for
// STATUS_NO_INSTANCES (A3).
func (c Client) WaitReady(ctx context.Context, procs proc.Checker, project string, pid int,
	timeout time.Duration) error {
	exited := output.Fail(output.CodeCommandFailed,
		"the Editor of %s, process %d, exited before it was ready", project, pid)
	start, err := procs.Start(pid)
	if errors.Is(err, proc.ErrNoProcess) {
		return exited
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+statusGrace)
	// finished closes once the status call has returned, with its error in statusErr.
	finished := make(chan struct{})
	var statusErr error
	go func() {
		defer close(finished)
		reply, err := c.Call(ctx, VerbStatus, project, "--until-ready", "--timeout",
			strconv.Itoa(int(timeout/time.Second)))
		if err == nil {
			err = reply.Err()
		}
		statusErr = err
	}()
	// stop ends the status call, if it still runs, and waits for it; its result no longer counts.
	stop := func() {
		cancel()
		<-finished
	}
	ticker := time.NewTicker(editorPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			cancel()
			err := statusErr
			if err == nil {
				return nil
			}
			if alive, aliveErr := procs.Alive(pid, start); aliveErr == nil && !alive {
				return exited
			}
			return &NotReadyError{Project: project, Timeout: timeout, Err: err}
		case <-ticker.C:
			alive, err := procs.Alive(pid, start)
			if err != nil || !alive {
				stop()
				if err != nil {
					return err
				}
				return exited
			}
		}
	}
}

// Closed is what `unity close` reported.
type Closed struct {
	// PID is the Editor's process.
	PID int
	// Method is how it closed it, such as graceful.
	Method string
}

// Shut runs `unity close <project>`, the project positional (docs/contracts.md (c)), and returns
// what it reported and the CLI's warnings. `unity close` exits without saving: `upool close`
// saves with SaveAll first, and only an Editor that holds no work is shut without it (A40).
func (c Client) Shut(ctx context.Context, project string) (Closed, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	reply, err := c.Call(ctx, VerbClose, project)
	if err == nil {
		err = reply.Err()
	}
	if err != nil {
		return Closed{}, nil, err
	}
	warnings := reply.Response.Warnings
	var data struct {
		PID    int    `json:"pid"`
		Closed bool   `json:"closed"`
		Method string `json:"method"`
	}
	if json.Unmarshal(reply.Response.Data, &data) != nil || !data.Closed {
		return Closed{}, warnings, output.Fail(output.CodeCommandFailed,
			"unity close %s did not report the Editor closed", project)
	}
	return Closed{PID: data.PID, Method: data.Method}, warnings, nil
}

// WaitGone waits for each of pids to be no process, by procs, checking every ExitPollInterval for
// at most ExitTimeout by now. A process still running then is a failure naming it: the Editor of
// project is still up.
func WaitGone(procs proc.Checker, project string, pids []int, now func() time.Time) error {
	deadline := now().Add(ExitTimeout)
	for {
		var running []int
		for _, pid := range pids {
			_, err := procs.Start(pid)
			switch {
			case errors.Is(err, proc.ErrNoProcess):
			case err != nil:
				return fmt.Errorf("checking the Editor of %s, process %d: %w", project, pid, err)
			default:
				running = append(running, pid)
			}
		}
		if len(running) == 0 {
			return nil
		}
		if !now().Before(deadline) {
			return output.Fail(output.CodeCommandFailed, "unity close %s returned, but the "+
				"Editor's process %v was still running %s later", project, running, ExitTimeout)
		}
		pids = running
		time.Sleep(ExitPollInterval)
	}
}
