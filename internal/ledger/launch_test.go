package ledger

import (
	"context"
	"errors"
	"testing"
)

// `upool open` admits an Editor under its own PID, then Launched records the Editor's: only the
// entry with the admitting PID changes, and one that is not there is ErrNotInLedger.
func TestLaunchedReplacesTheAdmittingPID(t *testing.T) {
	home := tempDir(t)
	b := budgetAt(t, 64, "")
	live := liveProcs{pids: map[int]bool{100: true, 200: true}}
	admit(t, home, live, b, Editor{Project: slot("1"), PID: 200, Mode: "gui"}, "")
	admit(t, home, live, b, Editor{Project: slot("2"), PID: 100, Mode: "headless"}, "")
	if err := Launched(context.Background(), home, slot("2"), 100, 4321); err != nil {
		t.Fatal(err)
	}
	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Editors) != 2 || l.Editors[0].PID != 200 || l.Editors[1].PID != 4321 {
		t.Errorf("editors = %+v, want slot-1 under 200 and slot-2 under 4321", l.Editors)
	}
	err = Launched(context.Background(), home, slot("2"), 100, 5)
	if !errors.Is(err, ErrNotInLedger) {
		t.Errorf("Launched for an entry no longer there = %v, want ErrNotInLedger", err)
	}
}

// Withdraw removes the entry with the project and the PID, and leaves another Editor of the same
// project, which Remove would take too.
func TestWithdrawRemovesOneEntry(t *testing.T) {
	home := tempDir(t)
	b := budgetAt(t, 64, "")
	live := liveProcs{pids: map[int]bool{100: true, 200: true}}
	admit(t, home, live, b, Editor{Project: slot("1"), PID: 200, Mode: "gui"}, "")
	admit(t, home, live, b, Editor{Project: slot("1"), PID: 100, Mode: "gui"}, "")
	found, err := Withdraw(context.Background(), home, slot("1"), 100)
	if err != nil || !found {
		t.Fatalf("Withdraw = %t, %v; want it found", found, err)
	}
	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Editors) != 1 || l.Editors[0].PID != 200 {
		t.Errorf("editors = %+v, want slot-1's other Editor alone", l.Editors)
	}
	if found, err := Withdraw(context.Background(), home, slot("1"), 100); err != nil || found {
		t.Errorf("Withdraw again = %t, %v; want not found", found, err)
	}
}
