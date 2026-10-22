package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"slices"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/unity"
)

// closeUsage is close's usage line, which its handler also names when it refuses its arguments.
const closeUsage = "close [--json]"

// The waits of `upool close`.
const (
	// closeTimeout bounds `unity close`, which returns once the Editor has exited: a graceful close
	// took 0.99 s after a save, and 12.1 s with a scene left dirty (spike §8 C.4, C.5).
	closeTimeout = 2 * time.Minute
	// exitTimeout is how long close waits for the Editor's process to be gone once `unity close`
	// has returned, checking every exitPollInterval.
	exitTimeout      = 30 * time.Second
	exitPollInterval = time.Second
)

// closeCommand is `upool close`: from inside a slot, it saves every open scene of the slot's
// Editor with save_all, and only then closes the Editor with `unity close <slot>`, which on its
// own exits without saving (docs/design.md, "Editors: budget by RAM, serialize cold imports as a
// courtesy, target by variable and by path": "release is always `save_all` then `close`"; the
// `open` and `close` row of "One binary, a dozen commands, one JSON file per pool").
var closeCommand = command{
	usage: closeUsage,
	setup: func(*flag.FlagSet) handler {
		return func(inv *invocation) (any, []string, error) {
			if len(inv.args) > 0 {
				return nil, nil, fmt.Errorf("close takes no arguments; usage: upool %s",
					closeUsage)
			}
			return closeEditor(inv)
		}
	},
}

// closeReport is what close did: the data of its --json envelope.
type closeReport struct {
	Slot string `json:"slot"`
	Path string `json:"path"`
	// PID is the Editor's process, as `unity close` reported it.
	PID int `json:"pid"`
	// Method is how `unity close` closed it, such as graceful.
	Method string `json:"method"`
}

// Human returns one line saying which Editor was saved and closed.
func (r closeReport) Human() string {
	return fmt.Sprintf("saved and closed %s's Editor, process %d", r.Slot, r.PID)
}

// closeEditor is close's handler.
//
//  1. It saves every open scene through the save_all wrapper, which re-queries after a first-call
//     timeout (A29). If the save fails, it stops there and exits 1: a close after a failed save
//     would discard the work.
//  2. It runs `unity close <slot>`, the slot positional (docs/contracts.md (c)).
//  3. It waits for the Editor's process to be gone, both the PID the ledger recorded and the one
//     `unity close` reports, and then takes the slot's Editor out of the ledger.
func closeEditor(inv *invocation) (any, []string, error) {
	ctx := context.Background()
	_, s, err := slotHere(ctx, inv, "close")
	if err != nil {
		return nil, nil, err
	}
	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	if err := client.SaveAll(ctx, s.Path); err != nil {
		code := output.CodeCommandFailed
		var f *output.Failure
		if errors.As(err, &f) {
			code = f.Code()
		}
		return nil, nil, output.Fail(code, "not closing the Editor of %s, since saving its "+
			"scenes failed: %v", s.Path, err)
	}

	closeCtx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	reply, err := client.Call(closeCtx, unity.VerbClose, s.Path)
	if err == nil {
		err = reply.Err()
	}
	if err != nil {
		return nil, nil, err
	}
	var data struct {
		PID    int    `json:"pid"`
		Closed bool   `json:"closed"`
		Method string `json:"method"`
	}
	if json.Unmarshal(reply.Response.Data, &data) != nil || !data.Closed {
		return nil, reply.Response.Warnings, output.Fail(output.CodeCommandFailed,
			"unity close %s did not report the Editor closed", s.Path)
	}

	procs := inv.checker()
	pids, err := editorPIDs(inv.home, s.Path, data.PID)
	if err != nil {
		return nil, reply.Response.Warnings, err
	}
	if err := waitGone(inv, procs, s.Path, pids); err != nil {
		return nil, reply.Response.Warnings, err
	}
	if _, err := ledger.Remove(ctx, inv.home, procs, s.Path); err != nil {
		return nil, reply.Response.Warnings, err
	}
	return closeReport{Slot: s.Name, Path: s.Path, PID: data.PID, Method: data.Method},
		reply.Response.Warnings, nil
}

// editorPIDs returns the processes of slot's Editor: those the ledger records for it and reported,
// the PID `unity close` reported, when it is one.
func editorPIDs(home, slot string, reported int) ([]int, error) {
	l, err := ledger.Load(home)
	if err != nil {
		return nil, err
	}
	key, err := paths.Key(slot)
	if err != nil {
		return nil, err
	}
	var pids []int
	if reported > 0 {
		pids = append(pids, reported)
	}
	for _, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return nil, err
		}
		if ek == key && !slices.Contains(pids, e.PID) {
			pids = append(pids, e.PID)
		}
	}
	return pids, nil
}

// waitGone waits for each of pids to be no process, checking every exitPollInterval for at most
// exitTimeout. A process still running then fails close, with the ledger left as it is: the
// Editor is still up and still charged.
func waitGone(inv *invocation, procs proc.Checker, slot string, pids []int) error {
	deadline := inv.now().Add(exitTimeout)
	for {
		var running []int
		for _, pid := range pids {
			_, err := procs.Start(pid)
			switch {
			case errors.Is(err, proc.ErrNoProcess):
			case err != nil:
				return fmt.Errorf("checking the Editor of %s, process %d: %w", slot, pid, err)
			default:
				running = append(running, pid)
			}
		}
		if len(running) == 0 {
			return nil
		}
		if !inv.now().Before(deadline) {
			return output.Fail(output.CodeCommandFailed, "unity close %s returned, but the "+
				"Editor's process %v was still running %s later", slot, running, exitTimeout)
		}
		pids = running
		time.Sleep(exitPollInterval)
	}
}
