package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/unity"
)

// openUsage is open's usage line, which its handler also names when it refuses its arguments.
const openUsage = "open [--headless|--gui] [--json]"

// The modes an Editor is opened in, as editors.json records them (docs/contracts.md (e),
// editors[].mode).
const (
	modeHeadless = "headless"
	modeGUI      = "gui"
)

// The waits of `upool open` (docs/contracts.md A37). An Editor charged as a cold import gets
// coldReadyTimeout to be ready, and any other readyTimeout: GUI Editors were ready in 11 to 21 s
// and headless ones within 57 s on warm Libraries, and a cold import took 235 to 259 s (spike §8
// C.1, B.1; spike §2 T5.5). A max_cold_imports refusal waits coldReadyTimeout too (A38).
const (
	readyTimeout     = 120 * time.Second
	coldReadyTimeout = 300 * time.Second
	// launchTimeout bounds `unity open`, which returns once it has launched the Editor.
	launchTimeout = time.Minute
	// statusGrace is how long upool gives `unity status --until-ready` past its own --timeout
	// before it ends the call, so that a CLI that hangs cannot hang open.
	statusGrace = 30 * time.Second
	// editorPollInterval is how often open checks that the Editor it launched is still running
	// while it waits for it to be ready.
	editorPollInterval = time.Second
)

// openCommand is `upool open [--headless|--gui]`: from inside a slot, it charges the Editor
// ledger, opens the slot's Editor and waits until the Editor is ready or has exited
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `open` row;
// "Editors: budget by RAM, serialize cold imports as a courtesy, target by variable and by
// path"). The Editor is headless unless --gui (A36).
var openCommand = command{
	usage: openUsage,
	setup: func(fs *flag.FlagSet) handler {
		headless := fs.Bool("headless", false, "open the Editor in batch mode, with no window "+
			"(the default)")
		gui := fs.Bool("gui", false, "open the Editor with its window")
		return func(inv *invocation) (any, []string, error) {
			if len(inv.args) > 0 || *headless && *gui {
				return nil, nil, fmt.Errorf("open takes no arguments and at most one of "+
					"--headless and --gui; usage: upool %s", openUsage)
			}
			mode := modeHeadless
			if *gui {
				mode = modeGUI
			}
			return openEditor(inv, mode)
		}
	},
}

// openReport is what open did: the data of its --json envelope.
type openReport struct {
	Slot string `json:"slot"`
	Path string `json:"path"`
	// PID is the Editor's process, as `unity open` reported it.
	PID  int    `json:"pid"`
	Mode string `json:"mode"`
	// ColdImport is whether the open was charged as a cold import, which has ended now that the
	// Editor is ready (A39).
	ColdImport bool `json:"cold_import"`
	// ChargeGB is the Editor's charge in the ledger now.
	ChargeGB float64 `json:"charge_gb"`
}

// Human returns one line saying which Editor is ready.
func (r openReport) Human() string {
	return fmt.Sprintf("opened %s's Editor %s, process %d: ready, charged %.1f GB", r.Slot,
		r.Mode, r.PID, r.ChargeGB)
}

// openEditor is open's handler: it opens the Editor of the slot containing the working directory.
func openEditor(inv *invocation, mode string) (any, []string, error) {
	ctx := context.Background()
	rp, s, err := slotHere(ctx, inv, "open")
	if err != nil {
		return nil, nil, err
	}
	r, warnings, err := openSlotEditor(ctx, inv, rp, s, mode, false)
	if err != nil {
		return nil, warnings, err
	}
	return r, warnings, nil
}

// openSlotEditor is open's work on slot s of the pool rp, which refresh also does to re-warm a
// slot it moved to base (#97): it returns what it did and the CLI's warnings. With rewarm, for
// refresh, it waits coldReadyTimeout whatever the charge, and an Editor still running when the
// wait runs out stays in the ledger, with the scene list kept, for refresh to close: the error is
// then a *notReadyError, and the report names the Editor's PID (A40).
//
//  1. It admits the Editor to the ledger under upool's own PID, as a cold import when the slot has
//     no Library/ or its reimport_pending is set (A20). A refusal by max_cold_imports waits; any
//     other fails before anything is launched (A38).
//  2. It runs `unity open <slot>`, with `--args -batchmode` for headless (A33), reads launchedPid
//     from stdout's envelope (A12), and records it in place of its own.
//  3. It waits with `unity status --until-ready` while checking that the Editor is still running
//     (docs/design.md, the lifecycle paragraph): a dead Editor fails before the timeout.
//  4. Once the Editor is ready, it ends a cold import and clears reimport_pending (A39).
//
// Before a headless launch it keeps the slot's Library/LastSceneManagerSetup.txt in upool's state
// directory, since a headless Editor closed gracefully empties it, for `upool close` to restore
// (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout,
// reset without `-x`"; A8). A failure after admission withdraws the entry, so that the ledger
// charges nothing for an Editor that never became ready, and drops the kept scene list.
func openSlotEditor(ctx context.Context, inv *invocation, rp registeredPool, s pool.Slot,
	mode string, rewarm bool) (openReport, []string, error) {
	repo := rp.pool.Repo
	if repo == "" {
		repo = rp.repo.Path
	}
	cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: repo, Home: inv.home})
	if err != nil {
		return openReport{}, nil, err
	}
	budget := ledger.NewBudget(cfg)
	procs := inv.checker()

	cold := s.ReimportPending
	if _, err := os.Stat(filepath.Join(s.Path, "Library")); errors.Is(err, os.ErrNotExist) {
		cold = true
	}
	timeout := readyTimeout
	if cold || rewarm {
		timeout = coldReadyTimeout
	}
	e, err := admitEditor(ctx, inv, procs, budget, ledger.Editor{Project: s.Path, PID: inv.pid,
		Mode: mode, OpenedByUpool: true, ColdImport: cold})
	if err != nil {
		return openReport{}, nil, err
	}
	var warnings []string
	// fail undoes the admission of the Editor recorded under pid, and the kept scene list.
	fail := func(pid int, err error) (openReport, []string, error) {
		err = withdrawEditor(ctx, inv, s.Path, pid, err)
		if mode == modeHeadless {
			if dropErr := pool.DropSceneList(ctx, inv.home, rp.repo.Key, s.Name); dropErr != nil {
				err = errors.Join(err, dropErr)
			}
		}
		return openReport{}, warnings, err
	}
	if mode == modeHeadless {
		if err := keepSceneList(ctx, inv.home, rp.repo.Key, s); err != nil {
			return fail(inv.pid, err)
		}
	}

	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	pid, warnings, err := launchEditor(ctx, client, s.Path, mode)
	if err == nil {
		err = ledger.Launched(ctx, inv.home, s.Path, inv.pid, pid)
	}
	if err != nil {
		return fail(inv.pid, err)
	}
	if err := waitReady(ctx, procs, client, s.Path, pid, timeout); err != nil {
		var notReady *notReadyError
		if rewarm && errors.As(err, &notReady) {
			return openReport{Slot: s.Name, Path: s.Path, PID: pid, Mode: mode,
				ColdImport: cold, ChargeGB: e.ChargeGB}, warnings, err
		}
		return fail(pid, err)
	}

	r := openReport{Slot: s.Name, Path: s.Path, PID: pid, Mode: mode, ColdImport: cold,
		ChargeGB: e.ChargeGB}
	if cold {
		if err := ledger.EndColdImport(ctx, inv.home, procs, budget, s.Path); err != nil {
			return openReport{}, warnings, err
		}
		r.ChargeGB = budget.EditorGB
	}
	if s.ReimportPending {
		if err := clearReimportPending(ctx, inv.home, rp.repo.Key, s.Name); err != nil {
			return openReport{}, warnings, err
		}
	}
	return r, warnings, nil
}

// keepSceneList keeps slot s's Library/LastSceneManagerSetup.txt for the slot's `upool close` to
// restore, or with no such file drops any copy an earlier session left (A8).
func keepSceneList(ctx context.Context, home, repoKey string, s pool.Slot) error {
	data, err := os.ReadFile(filepath.Join(s.Path, "Library", pool.SceneListFile))
	if errors.Is(err, os.ErrNotExist) {
		return pool.DropSceneList(ctx, home, repoKey, s.Name)
	}
	if err != nil {
		return err
	}
	return pool.SaveSceneList(ctx, home, repoKey, s.Name, data)
}

// slotHere finds the slot containing the working directory, which open and close act on, and its
// pool. command names the command for the error when there is none.
func slotHere(ctx context.Context, inv *invocation, command string) (registeredPool, pool.Slot,
	error) {
	wd, err := inv.getwd()
	if err != nil {
		return registeredPool{}, pool.Slot{}, fmt.Errorf("reading the working directory: %w", err)
	}
	key, err := paths.Key(wd)
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	pools, err := readPools(ctx, inv.home,
		func(repo registry.Repo) bool { return paths.Within(key, repo.Path) })
	if err != nil {
		return registeredPool{}, pool.Slot{}, err
	}
	if len(pools) > 0 {
		rp := innermost(pools)
		for _, s := range rp.pool.Slots {
			sk, err := paths.Key(s.Path)
			if err != nil {
				return registeredPool{}, pool.Slot{}, err
			}
			if paths.Within(key, sk) {
				return rp, s, nil
			}
		}
	}
	return registeredPool{}, pool.Slot{}, output.Fail(output.CodeNotInSlot,
		"%s is not inside a slot; run upool %s in the slot whose Editor it is for", wd, command)
}

// admitEditor admits e to the ledger. A refusal by max_cold_imports waits for the running cold
// import to end, retrying once a second and saying why on stderr, for at most coldReadyTimeout;
// any other refusal fails at once, naming its limit (A38).
func admitEditor(ctx context.Context, inv *invocation, procs proc.Checker, b ledger.Budget,
	e ledger.Editor) (ledger.Editor, error) {
	deadline := inv.now().Add(coldReadyTimeout)
	said := ""
	for {
		admitted, err := ledger.Admit(ctx, inv.home, procs, b, e)
		var refusal *ledger.Refusal
		switch {
		case !errors.As(err, &refusal):
			return admitted, err
		case refusal.Limit != "max_cold_imports":
			return ledger.Editor{}, output.Fail(output.CodeCommandFailed,
				"opening the Editor of %s: %v", e.Project, refusal)
		case !inv.now().Before(deadline):
			return ledger.Editor{}, output.Fail(output.CodeCommandFailed,
				"opening the Editor of %s: still refused after %s: %v", e.Project,
				coldReadyTimeout, refusal)
		}
		line := "waiting to open the Editor of " + e.Project + ": " + refusal.Error()
		if line != said {
			_, _ = fmt.Fprintln(inv.stderr, "upool: "+line) // progress only
			said = line
		}
		time.Sleep(retryInterval)
	}
}

// launchEditor runs `unity open <slot>`, with `--args -batchmode` for a headless Editor (A33), and
// returns the PID it launched and the CLI's warnings, such as its Hub-registry warning, which it
// repeats in the envelope (A12).
func launchEditor(ctx context.Context, c unity.Client, slot, mode string) (int, []string,
	error) {
	ctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	var args []string
	if mode == modeHeadless {
		args = []string{"--args", "-batchmode"}
	}
	reply, err := c.Call(ctx, unity.VerbOpen, slot, args...)
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
			"unity open %s reported no launchedPid", slot)
	}
	return data.LaunchedPID, reply.Response.Warnings, nil
}

// waitReady waits for the Editor at pid to be ready in slot: `unity status --until-ready
// --timeout <timeout>`, which waits the whole timeout when no Editor comes up, while it checks the
// Editor's process every editorPollInterval, so that an Editor that exits fails at once rather
// than at the timeout (docs/design.md, the lifecycle paragraph). An Editor that is not ready in
// time fails as the status call does, exiting 6 for STATUS_NO_INSTANCES (A3).
func waitReady(ctx context.Context, procs proc.Checker, c unity.Client, slot string, pid int,
	timeout time.Duration) error {
	exited := output.Fail(output.CodeCommandFailed,
		"the Editor of %s, process %d, exited before it was ready", slot, pid)
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
		reply, err := c.Call(ctx, unity.VerbStatus, slot, "--until-ready", "--timeout",
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
			return &notReadyError{slot: slot, timeout: timeout, err: err}
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

// notReadyError is waitReady's error for an Editor that was not ready when the wait ran out and
// whose process, as far as could be told, was still running.
type notReadyError struct {
	slot    string
	timeout time.Duration
	// err is the status call's error.
	err error
}

func (e *notReadyError) Error() string {
	return fmt.Sprintf("the Editor of %s was not ready after %s: %v", e.slot, e.timeout, e.err)
}

func (e *notReadyError) Unwrap() error { return e.err }

// withdrawEditor takes the Editor open recorded under pid out of the ledger after err, and
// returns err, with the withdrawal's own error if it failed.
func withdrawEditor(ctx context.Context, inv *invocation, slot string, pid int, err error) error {
	if _, werr := ledger.Withdraw(ctx, inv.home, slot, pid); werr != nil {
		return errors.Join(err, fmt.Errorf("taking the Editor of %s out of %s: %w", slot,
			"~/.upool/editors.json", werr))
	}
	return err
}

// clearReimportPending clears the reimport_pending of the slot name in the pool under repoKey,
// once its Editor has done the import the flag announced (A39).
func clearReimportPending(ctx context.Context, home, repoKey, name string) error {
	return pool.Update(ctx, home, repoKey, func(p *pool.Pool) error {
		for i := range p.Slots {
			if p.Slots[i].Name == name {
				p.Slots[i].ReimportPending = false
			}
		}
		return nil
	})
}
