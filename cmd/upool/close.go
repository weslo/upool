package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/timing"
	"github.com/weslo/upool/internal/unity"
)

// closeUsage is close's usage line, which its handler also names when it refuses its arguments.
const closeUsage = "close [--force] [--json]"

// closeCommand is `upool close`: from inside a slot, it saves every open scene of the slot's
// Editor that has a path, and only then closes the Editor with `unity close <slot>`, which on its
// own exits without saving (docs/design.md, "Editors: budget by RAM, serialize cold imports as a
// courtesy, target by variable and by path": "release is always `save_all` then `close`"; the
// `open` and `close` row of "One binary, a dozen commands, one JSON file per pool"). With --force
// it saves nothing and closes with `unity close <slot> --force`, the way out of an Editor that
// will not close when nothing in it is unsaved (#280). It times the save and the close as its
// steps (#266).
var closeCommand = command{
	usage: closeUsage,
	setup: func(fs *flag.FlagSet) handler {
		force := fs.Bool("force", false, "close without saving, with unity close --force")
		return func(inv *invocation) (any, []string, error) {
			if len(inv.args) > 0 {
				return nil, nil, fmt.Errorf("close takes no arguments; usage: upool %s",
					closeUsage)
			}
			return closeEditor(inv, *force)
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
	// Saved is whether close saved the Editor's scenes first: false for `close --force`.
	Saved bool `json:"saved"`
	// SceneListRestored is whether close put back the scene list `upool open --headless` kept
	// (A8).
	SceneListRestored bool `json:"scene_list_restored"`
}

// Human returns one line saying which Editor was closed, and whether it was saved first.
func (r closeReport) Human() string {
	if !r.Saved {
		return fmt.Sprintf("closed %s's Editor without saving, process %d", r.Slot, r.PID)
	}
	return fmt.Sprintf("saved and closed %s's Editor, process %d", r.Slot, r.PID)
}

// closeEditor is close's handler.
//
//  1. It saves the open scenes (saveScenes): those that have a path, each said on stderr if it has
//     none. If the save fails, it stops there and exits 1: a close after a failed save would
//     discard the work. A save that timed out says that a native panel is the likely cause, and
//     what to do (#280).
//  2. It runs `unity close <slot>`, the slot positional (docs/contracts.md (c)).
//  3. It waits for the Editor's process to be gone, both the PID the ledger recorded and the one
//     `unity close` reports, and then takes the slot's Editor out of the ledger.
//  4. When that Editor was one `upool open --headless` opened, it puts back the scene list open
//     kept, which the headless Editor emptied as it closed, and deletes the copy (A8).
//
// With force, it skips step 1, runs `unity close <slot> --force` in step 2, and once the Editor
// is gone removes the instance files a forced close leaves, as A45 has it.
func closeEditor(inv *invocation, force bool) (any, []string, error) {
	inv.timer = timing.Start(inv.now, "close")
	ctx := context.Background()
	rp, s, err := slotHere(ctx, inv, "close")
	if err != nil {
		return nil, nil, err
	}
	inv.timer.Pool(inv.home, rp.repo.Key)
	inv.timer.Slot(s.Name)
	var r closeReport
	var warnings []string
	if force {
		err = inv.timer.Step("close", "", func() error {
			var err error
			r, warnings, err = shutSlotEditor(ctx, inv, rp, s, true)
			return err
		})
	} else {
		r, warnings, err = closeSlotEditor(ctx, inv, rp, s, false, "close")
	}
	if err != nil {
		return nil, warnings, err
	}
	return r, warnings, nil
}

// closeSlotEditor is close's work on slot s of the pool rp, which release also does for an Editor
// upool opened, and refresh for the Editor of a re-warm: it returns what it did and the warnings,
// the CLI's and those of the save. It times closeEditor's step 1 as save_all and steps 2 to 4 as
// close (#266). again is the command, close or release, that a save that timed out is to be run
// again with.
//
// With retry, the Editor is the re-warm's, one upool opened headless at base and that holds no
// work: its scenes are saved with save_all alone, unlisted, and a plain close that leaves it
// running is sent again and then forced, as A48 has it (#273).
func closeSlotEditor(ctx context.Context, inv *invocation, rp registeredPool,
	s pool.Slot, retry bool, again string) (closeReport, []string, error) {
	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	var warnings []string
	err := inv.timer.Step("save_all", "", func() error {
		if retry {
			return client.SaveAll(ctx, s.Path)
		}
		var err error
		warnings, err = saveScenes(ctx, client, s)
		return err
	})
	if err != nil {
		return closeReport{}, warnings, saveFailure(s, retry, again, err)
	}
	var r closeReport
	var more []string
	err = inv.timer.Step("close", "", func() error {
		var err error
		if retry {
			r, more, err = shutSlotEditorRetrying(ctx, inv, rp, s)
		} else {
			r, more, err = shutSlotEditor(ctx, inv, rp, s, false)
		}
		return err
	})
	warnings = append(warnings, more...)
	if err != nil {
		return r, warnings, err
	}
	r.Saved = true
	return r, warnings, nil
}

// saveScenes saves the scenes open in slot s's Editor that have a path, and returns a warning for
// each it did not save (#280):
//
//   - It lists the open scenes (unity.Client.ListScenes). A listing that timed out is the save's
//     failure. One that failed any other way is a warning, and save_all saves every scene, as
//     before the listing.
//   - When every scene has a path, save_all saves them.
//   - Otherwise, save_all cannot be told to pass a scene over, since it takes no parameters
//     (testdata/captures/unity/command-list-query-save.json), and its save of a scene with no
//     path opens a native Save panel that holds the Editor. So each scene with a path and unsaved
//     changes is saved on its own (unity.Client.SaveScene), and each with none is a warning, and
//     not saved: closing the Editor then discards it.
func saveScenes(ctx context.Context, client unity.Client, s pool.Slot) ([]string, error) {
	scenes, err := client.ListScenes(ctx, s.Path)
	var timedOut *unity.TimedOutError
	if errors.As(err, &timedOut) {
		return nil, err
	}
	if err != nil {
		return []string{fmt.Sprintf("could not list the scenes open in %s's Editor, so save_all "+
			"saves them all: %v", s.Name, err)}, client.SaveAll(ctx, s.Path)
	}
	if !slices.ContainsFunc(scenes, func(sc unity.OpenScene) bool { return sc.Path == "" }) {
		return nil, client.SaveAll(ctx, s.Path)
	}
	var warnings []string
	for i, sc := range scenes {
		if sc.Path != "" {
			continue
		}
		changes := "it has no unsaved changes"
		if sc.Dirty {
			changes = "its unsaved changes are discarded when the Editor closes"
		}
		warnings = append(warnings, fmt.Sprintf("not saving open scene %d of %d in %s's Editor, "+
			"which is Untitled: it has no path, and saving it would open a Save panel; %s", i+1,
			len(scenes), s.Name, changes))
	}
	for _, sc := range scenes {
		if sc.Path == "" || !sc.Dirty {
			continue
		}
		if err := client.SaveScene(ctx, s.Path, sc.Path); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// saveFailure is close's failure for a save of slot s's scenes that failed with err: it names the
// slot and the cause. A save that timed out names a native panel as the likely cause and says what
// to do, `upool <again>` once the panel is dismissed or `upool close --force` (#280); a re-warm's,
// with retry, whose headless Editor has no window to dismiss a panel in, does not.
func saveFailure(s pool.Slot, retry bool, again string, err error) error {
	code := output.CodeCommandFailed
	var f *output.Failure
	if errors.As(err, &f) {
		code = f.Code()
	}
	var timedOut *unity.TimedOutError
	if !retry && errors.As(err, &timedOut) {
		return output.Fail(code, "not closing the Editor of %s, since saving its scenes timed "+
			"out: a native panel open in the Editor, such as the Save panel a scene with no "+
			"path opens, is the likely cause. Press Cancel in %s's Editor, then run `upool %s` "+
			"again; if nothing in it is unsaved, `upool close --force` in the slot closes it "+
			"without saving (%v)", s.Path, s.Name, again, err)
	}
	return output.Fail(code, "not closing the Editor of %s, since saving its scenes failed: %v",
		s.Path, err)
}

// shutSlotEditor is closeSlotEditor's work after the save, steps 2 to 4: `unity close <slot>`, the
// wait for the Editor's process to be gone, the ledger, and the scene list. refresh runs it alone
// on a re-warm Editor that was not ready in time, which holds no work to save, with force, since
// the CLI may not reach such an Editor (A40; #224). A forced close terminates the Editor, so once
// it is gone the instance files it left in the slot are removed (clone.ClearLeftovers; A45).
func shutSlotEditor(ctx context.Context, inv *invocation, rp registeredPool,
	s pool.Slot, force bool) (closeReport, []string, error) {
	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	closed, warnings, err := client.Shut(ctx, s.Path, force)
	if err != nil {
		return closeReport{}, warnings, err
	}
	r, more, err := shutDone(ctx, inv, client, rp, s, closed, force)
	return r, append(warnings, more...), err
}

// shutSlotEditorRetrying is shutSlotEditor for a ready Editor upool opened headless: the plain
// close is sent again while it leaves the Editor running, and the Editor is closed with --force
// once unity.CloseRetryTimeout has passed (unity.ShutRetrying; A48; #273). The process it watches
// is the one the ledger records for the slot; with none recorded, the close is plain and sent
// once.
func shutSlotEditorRetrying(ctx context.Context, inv *invocation, rp registeredPool,
	s pool.Slot) (closeReport, []string, error) {
	pids, _, err := editorPIDs(inv.home, s.Path, 0)
	if err != nil {
		return closeReport{}, nil, err
	}
	if len(pids) == 0 {
		return shutSlotEditor(ctx, inv, rp, s, false)
	}
	client := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}
	closed, forced, warnings, err := client.ShutRetrying(ctx, inv.checker(), s.Path, pids[0],
		inv.now)
	if err != nil {
		return closeReport{}, warnings, err
	}
	r, more, err := shutDone(ctx, inv, client, rp, s, closed, forced)
	return r, append(warnings, more...), err
}

// shutDone is shutSlotEditor's steps 3 and 4 once `unity close` has reported the Editor closed:
// the wait for its processes to be gone, the ledger, the instance files a forced close left (A45),
// and the scene list of a headless Editor upool opened (A8).
func shutDone(ctx context.Context, inv *invocation, client unity.Client, rp registeredPool,
	s pool.Slot, closed unity.Closed, forced bool) (closeReport, []string, error) {
	procs := inv.checker()
	pids, headless, err := editorPIDs(inv.home, s.Path, closed.PID)
	if err != nil {
		return closeReport{}, nil, err
	}
	if err := unity.WaitGone(procs, s.Path, pids, inv.now); err != nil {
		return closeReport{}, nil, err
	}
	if _, err := ledger.Remove(ctx, inv.home, procs, s.Path); err != nil {
		return closeReport{}, nil, err
	}
	var warnings []string
	if forced {
		warnings = append(warnings, clone.ClearLeftovers(ctx, client, s.Path, pids)...)
	}
	r := closeReport{Slot: s.Name, Path: s.Path, PID: closed.PID, Method: closed.Method}
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
