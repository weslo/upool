package ledger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Once `upool open` sees an Editor ready, Ready records the time as its ready_at, in UTC: only on
// the entry with the project and the PID, so that another Editor of the same project keeps none,
// and one that is not there is ErrNotInLedger, with nothing written (#281).
func TestReadyRecordsWhenTheEditorWasReady(t *testing.T) {
	home := tempDir(t)
	b := budgetAt(t, 64, "")
	live := liveProcs{pids: map[int]bool{100: true, 200: true}}
	admit(t, home, live, b, Editor{Project: slot("1"), PID: 200, Mode: "gui"}, "")
	admit(t, home, live, b, Editor{Project: slot("1"), PID: 100, Mode: "headless"}, "")
	at := time.Date(2026, 11, 3, 11, 42, 30, 0, time.FixedZone("PST", -8*60*60))
	if err := Ready(context.Background(), home, slot("1"), 100, at); err != nil {
		t.Fatal(err)
	}
	l, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Editors) != 2 || l.Editors[0].ReadyAt != nil || l.Editors[1].ReadyAt == nil ||
		!l.Editors[1].ReadyAt.Equal(at) || l.Editors[1].ReadyAt.Location() != time.UTC {
		t.Errorf("editors = %+v, want the one under 100 alone ready at %s, in UTC", l.Editors,
			at.UTC())
	}
	before := readFile(t, home)
	if !strings.Contains(before, `"ready_at": "2026-11-03T19:42:30Z"`) {
		t.Errorf("editors.json = %s, want ready_at 2026-11-03T19:42:30Z", before)
	}
	err = Ready(context.Background(), home, slot("2"), 100, at)
	if !errors.Is(err, ErrNotInLedger) {
		t.Errorf("Ready for an entry not there = %v, want ErrNotInLedger", err)
	}
	if after := readFile(t, home); after != before {
		t.Errorf("editors.json after a failed Ready =\n%s\nwant it unchanged:\n%s", after, before)
	}
}

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
