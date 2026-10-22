package clone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/unity"
)

// headless is the mode the ledger records for the Editor a refresh opens (docs/contracts.md (e),
// editors[].mode).
const headless = "headless"

// SeedRefresher refreshes a stale seed before the recipe copies it ((g) step 1; docs/design.md,
// "Slot lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without
// `-x`": a stale source is refreshed before cloning; #98). It opens the seed's Editor headless
// under the ledger, waits until it is ready, which an Editor is only once it has imported and
// compiled what its scripts changed (A39), and closes it. A seed never has a live Editor ((g) step
// 0), so the design's `unity recompile` case does not arise.
type SeedRefresher struct {
	// Unity runs the Editor's calls; its environment's PATH is where unity is found.
	Unity unity.Client
	// Proc says whether the Editor's process, and those the ledger records, are running.
	Proc proc.Checker
	// Budget admits the Editor to the ledger under Home; a refusal fails the refresh.
	Budget ledger.Budget
	Home   string
	// OwnerPID is the refreshing process, which the Editor is admitted under until it launches.
	OwnerPID int
	// Now is the clock the wait for the Editor's exit runs on.
	Now func() time.Time
	// Say, when not nil, is told that a refresh starts, which takes a while.
	Say func(line string)
}

// Refresh refreshes the seed at seed and returns the CLI's warnings.
//
//  1. It admits the Editor to the ledger under OwnerPID, charged as any Editor is: a refresh
//     brings the Library current with its own commit, which is no cold import (A20).
//  2. It runs `unity open <seed> --args -batchmode` (A33) and records the Editor's PID in place of
//     OwnerPID (A12).
//  3. It waits with `unity status --until-ready` for A37's timeout, checking the Editor's process.
//  4. It closes the Editor with `unity close <seed>`, without save_all, since an Editor upool
//     opened only to refresh holds no work, waits for its process to exit, and takes it out of the
//     ledger.
//
// An Editor that is not ready in time is closed the same way, as A40 does for a re-warm, and the
// refresh fails; one that exited is withdrawn from the ledger. If the close fails, the Editor stays
// in the ledger. The seed's Library/LastSceneManagerSetup.txt, which a headless Editor empties as
// it closes, is put back as it was (A8).
func (r SeedRefresher) Refresh(ctx context.Context, seed string) ([]string, error) {
	if r.Say != nil {
		r.Say("the Library in " + seed + " is stale; refreshing it with a headless Editor first")
	}
	scenes := filepath.Join(seed, "Library", pool.SceneListFile)
	kept, err := os.ReadFile(scenes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := ledger.Admit(ctx, r.Home, r.Proc, r.Budget, ledger.Editor{Project: seed,
		PID: r.OwnerPID, Mode: headless, OpenedByUpool: true}); err != nil {
		return nil, fmt.Errorf("opening the Editor of %s: %w", seed, err)
	}
	pid, warnings, err := r.Unity.Launch(ctx, seed, true)
	if err == nil {
		err = ledger.Launched(ctx, r.Home, seed, r.OwnerPID, pid)
	}
	if err != nil {
		return warnings, r.withdraw(ctx, seed, r.OwnerPID, err)
	}
	ready := r.Unity.WaitReady(ctx, r.Proc, seed, pid, unity.ReadyTimeout)
	var notReady *unity.NotReadyError
	if ready != nil && !errors.As(ready, &notReady) {
		return warnings, r.withdraw(ctx, seed, pid, ready)
	}
	closed, more, err := r.Unity.Shut(ctx, seed)
	warnings = append(warnings, more...)
	if err == nil {
		err = unity.WaitGone(r.Proc, seed, []int{pid, closed.PID}, r.Now)
	}
	if err == nil {
		_, err = ledger.Remove(ctx, r.Home, r.Proc, seed)
	}
	if err == nil && kept != nil {
		err = os.WriteFile(scenes, kept, 0o644)
	}
	if err != nil {
		err = fmt.Errorf("closing the Editor of %s: %w", seed, err)
	}
	return warnings, errors.Join(ready, err)
}

// withdraw takes the Editor recorded for seed under pid out of the ledger after err, and returns
// err, with the withdrawal's own error if it failed.
func (r SeedRefresher) withdraw(ctx context.Context, seed string, pid int, err error) error {
	if _, werr := ledger.Withdraw(ctx, r.Home, seed, pid); werr != nil {
		return errors.Join(err, werr)
	}
	return err
}
