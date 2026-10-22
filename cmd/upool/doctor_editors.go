package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/unity"
)

// The names of the Editor checks.
const (
	checkLedgerEditors = "editors"
	checkLicence       = "unity-licence"
)

// unityCheckTimeout bounds each `unity status` and `unity license status` doctor runs, so that a
// CLI that hangs cannot hang doctor.
const unityCheckTimeout = 30 * time.Second

// editorChecks checks the ledger's Editors in the pool at repo against the process table
// (docs/design.md, "Editors: budget by RAM, serialize cold imports as a courtesy, target by
// variable and by path", crash recovery). An Editor whose process is gone fails, one line each,
// still charged though it runs no more. With fix, doctor takes its entry out of the ledger, and
// deletes its slot's Temp/, whose leftover UnityLockfile makes Unity refuse the project as already
// open, but only when `unity status` with the variable set to the slot reports no instance there:
// a dead PID alone does not prove no Editor has the project open.
func editorChecks(ctx context.Context, inv *invocation, repo string, slots []pool.Slot,
	fix bool) []doctorCheck {
	l, err := ledger.Load(inv.home)
	if err != nil {
		return []doctorCheck{failedCheck(checkLedgerEditors, err)}
	}
	repoKey, err := paths.Key(repo)
	if err != nil {
		return []doctorCheck{failedCheck(checkLedgerEditors, err)}
	}
	procs := inv.checker()
	var checks []doctorCheck
	running := 0
	for _, e := range l.Editors {
		key, err := paths.Key(e.Project)
		if err != nil {
			return []doctorCheck{failedCheck(checkLedgerEditors, err)}
		}
		if !paths.Within(key, repoKey) {
			continue // another pool's, which its own doctor checks
		}
		_, err = procs.Start(e.PID)
		switch {
		case err == nil:
			running++
			continue
		case !errors.Is(err, proc.ErrNoProcess):
			checks = append(checks, failedCheck(checkLedgerEditors, fmt.Errorf(
				"checking the Editor of %s, process %d: %w", e.Project, e.PID, err)))
			continue
		}
		checks = append(checks, deadEditor(ctx, inv, e, slotName(slots, key), fix))
	}
	if len(checks) > 0 {
		return checks
	}
	if running == 0 {
		return []doctorCheck{{checkLedgerEditors, checkPass,
			"the ledger charges no Editor in this pool"}}
	}
	return []doctorCheck{{checkLedgerEditors, checkPass, fmt.Sprintf(
		"every Editor the ledger charges in this pool is running (%d)", running)}}
}

// deadEditor is the check of e, an Editor in the ledger whose process is gone, in the slot named
// name; with fix, it repairs what is safe to repair.
func deadEditor(ctx context.Context, inv *invocation, e ledger.Editor, name string,
	fix bool) doctorCheck {
	reason := fmt.Sprintf("%s's Editor, process %d, is gone, but the ledger still charges it",
		name, e.PID)
	if !fix {
		return doctorCheck{checkLedgerEditors, checkFail, reason + "; upool doctor --fix " +
			"removes it, and its Temp/ when no Editor has the slot open"}
	}
	if _, err := ledger.Withdraw(ctx, inv.home, e.Project, e.PID); err != nil {
		return doctorCheck{checkLedgerEditors, checkFail, fmt.Sprintf("%s; removing it: %v", reason,
			err)}
	}
	fixed := fmt.Sprintf("fixed: removed %s's Editor, process %d, which was gone", name, e.PID)
	statusCtx, cancel := context.WithTimeout(ctx, unityCheckTimeout)
	defer cancel()
	n, err := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}.EditorsAt(statusCtx,
		e.Project)
	switch {
	case err != nil:
		return doctorCheck{checkLedgerEditors, checkWarn, fmt.Sprintf("%s; kept its Temp/, since "+
			"unity status could not say no Editor has it open: %v", fixed, err)}
	case n > 0:
		return doctorCheck{checkLedgerEditors, checkWarn, fmt.Sprintf("%s; kept its Temp/, since "+
			"unity status lists an Editor with it open", fixed)}
	}
	temp := filepath.Join(e.Project, "Temp")
	if err := os.RemoveAll(temp); err != nil {
		return doctorCheck{checkLedgerEditors, checkFail, fmt.Sprintf("%s; deleting %s: %v", fixed,
			temp, err)}
	}
	return doctorCheck{checkLedgerEditors, checkPass, fixed + ", and its Temp/, since unity " +
		"status lists no Editor with it open"}
}

// slotName is the name of the slot whose path has key, or the key itself when no slot has it.
func slotName(slots []pool.Slot, key string) string {
	for _, s := range slots {
		if sk, err := paths.Key(s.Path); err == nil && sk == key {
			return s.Name
		}
	}
	return key
}

// licenceCheck reports `unity license status` as information, run in the main checkout at repo:
// upool enforces nothing about the licence, since three Editors ran under one (docs/design.md,
// "Editors: budget by RAM, serialize cold imports as a courtesy, target by variable and by path").
// It passes with an active licence and warns otherwise, or when the licence cannot be read.
func licenceCheck(ctx context.Context, inv *invocation, repo string) doctorCheck {
	ctx, cancel := context.WithTimeout(ctx, unityCheckTimeout)
	defer cancel()
	l, err := unity.Client{Runner: unity.Exec{}, Env: inv.environ()}.LicenseStatus(ctx, repo)
	switch {
	case errors.Is(err, unity.ErrNoUnity):
		return doctorCheck{checkLicence, checkWarn,
			"unity is not on PATH, so the licence is not checked"}
	case err != nil:
		return doctorCheck{checkLicence, checkWarn, "the licence is not checked: " + err.Error()}
	}
	var products []string
	for _, p := range l.Licenses {
		products = append(products, p.Product)
	}
	held := "no licence"
	if len(products) > 0 {
		held = strings.Join(products, ", ")
	}
	signed := "signed out"
	if l.SignedIn {
		signed = "signed in (" + l.AuthMode + ")"
	}
	if !l.Active {
		return doctorCheck{checkLicence, checkWarn, fmt.Sprintf("not active, %s, holding %s; "+
			"upool enforces nothing about it", signed, held)}
	}
	return doctorCheck{checkLicence, checkPass, fmt.Sprintf("active, %s, holding %s", signed,
		held)}
}
