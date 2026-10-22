package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
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
	// SceneListRestored is whether close put back the scene list `upool open --headless` kept
	// (A8).
	SceneListRestored bool `json:"scene_list_restored"`
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
//  4. When that Editor was one `upool open --headless` opened, it puts back the scene list open
//     kept, which the headless Editor emptied as it closed, and deletes the copy (A8).
func closeEditor(inv *invocation) (any, []string, error) {
	ctx := context.Background()
	rp, s, err := slotHere(ctx, inv, "close")
	if err != nil {
		return nil, nil, err
	}
	r, warnings, err := closeSlotEditor(ctx, inv, rp, s)
	if err != nil {
		return nil, warnings, err
	}
	return r, warnings, nil
}

// closeSlotEditor is close's work on slot s of the pool rp, which release also does for an Editor
// upool opened: it returns what it did and the CLI's warnings.
func closeSlotEditor(ctx context.Context, inv *invocation, rp registeredPool,
	s pool.Slot) (closeReport, []string, error) {
	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	if err := client.SaveAll(ctx, s.Path); err != nil {
		code := output.CodeCommandFailed
		var f *output.Failure
		if errors.As(err, &f) {
			code = f.Code()
		}
		return closeReport{}, nil, output.Fail(code, "not closing the Editor of %s, since "+
			"saving its scenes failed: %v", s.Path, err)
	}

	closeCtx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	reply, err := client.Call(closeCtx, unity.VerbClose, s.Path)
	if err == nil {
		err = reply.Err()
	}
	if err != nil {
		return closeReport{}, nil, err
	}
	warnings := reply.Response.Warnings
	var data struct {
		PID    int    `json:"pid"`
		Closed bool   `json:"closed"`
		Method string `json:"method"`
	}
	if json.Unmarshal(reply.Response.Data, &data) != nil || !data.Closed {
		return closeReport{}, warnings, output.Fail(output.CodeCommandFailed,
			"unity close %s did not report the Editor closed", s.Path)
	}

	procs := inv.checker()
	pids, headless, err := editorPIDs(inv.home, s.Path, data.PID)
	if err != nil {
		return closeReport{}, warnings, err
	}
	if err := waitGone(inv, procs, s.Path, pids); err != nil {
		return closeReport{}, warnings, err
	}
	if _, err := ledger.Remove(ctx, inv.home, procs, s.Path); err != nil {
		return closeReport{}, warnings, err
	}
	r := closeReport{Slot: s.Name, Path: s.Path, PID: data.PID, Method: data.Method}
	if headless {
		if r.SceneListRestored, err = restoreSceneList(ctx, inv.home, rp.repo.Key,
			s); err != nil {
			return closeReport{}, warnings, err
		}
	}
	return r, warnings, nil
}

// editorPIDs returns the processes of slot's Editor: those the ledger records for it and reported,
// the PID `unity close` reported, when it is one. It also reports whether the ledger records a
// headless Editor that upool opened in slot.
func editorPIDs(home, slot string, reported int) ([]int, bool, error) {
	l, err := ledger.Load(home)
	if err != nil {
		return nil, false, err
	}
	key, err := paths.Key(slot)
	if err != nil {
		return nil, false, err
	}
	var pids []int
	if reported > 0 {
		pids = append(pids, reported)
	}
	headless := false
	for _, e := range l.Editors {
		ek, err := paths.Key(e.Project)
		if err != nil {
			return nil, false, err
		}
		if ek != key {
			continue
		}
		if !slices.Contains(pids, e.PID) {
			pids = append(pids, e.PID)
		}
		headless = headless || e.Mode == modeHeadless && e.OpenedByUpool
	}
	return pids, headless, nil
}

// restoreSceneList writes the scene list `upool open --headless` kept for slot s back into its
// Library/, now that the headless Editor has exited, and deletes the copy (A8). It reports whether
// there was one to restore.
func restoreSceneList(ctx context.Context, home, repoKey string, s pool.Slot) (bool, error) {
	data, ok, err := pool.SavedSceneList(home, repoKey, s.Name)
	if err != nil || !ok {
		return false, err
	}
	path := filepath.Join(s.Path, "Library", pool.SceneListFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return false, fmt.Errorf("restoring %s: %w", path, err)
	}
	return true, pool.DropSceneList(ctx, home, repoKey, s.Name)
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
