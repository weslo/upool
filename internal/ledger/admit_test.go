package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/config"
)

// The tests of admission against the RAM budget (docs/contracts.md (h); A9), on editors.json in a
// temporary home, with a liveProcs standing in for the operating system's process table.

// budgetAt is the budget config.Load gives with ramGB of physical RAM and the user file's keys
// set as given ("" for none), so that the defaults and the derivation are config's own.
func budgetAt(t *testing.T, ramGB uint64, userFile string) Budget {
	t.Helper()
	home := tempDir(t)
	if userFile != "" {
		path := config.UserPath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(userFile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.Load(config.Inputs{Getenv: func(string) string { return "" }, Home: home,
		Memory: func() (uint64, error) { return ramGB << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return NewBudget(c)
}

// slot is the path of a slot the tests' Editors have open.
func slot(n string) string { return filepath.Join(repo.Path, ".claude", "worktrees", "slot-"+n) }

// admit admits e, failing the test unless the result is want: nil, or a Refusal naming that limit.
func admit(t *testing.T, home string, c liveProcs, b Budget, e Editor, wantLimit string) Editor {
	t.Helper()
	got, err := Admit(context.Background(), home, c, b, e)
	var r *Refusal
	switch {
	case wantLimit == "" && err != nil:
		t.Fatalf("Admit(%s) = %v, want it admitted", e.Project, err)
	case wantLimit != "" && (!errors.As(err, &r) || r.Limit != wantLimit):
		t.Fatalf("Admit(%s) = %v, want a refusal by %s", e.Project, err, wantLimit)
	}
	return got
}

// projects lists the ledger's Editors by project, in order.
func projects(l Ledger) []string {
	var out []string
	for _, e := range l.Editors {
		out = append(out, e.Project)
	}
	return out
}

// The acceptance test of #85, with 64 GB of RAM: three long-lived Editors are admitted, each
// charged editor_ram_gb, and a fourth is refused by max_editors, which (h) derives as
// floor((16 − 2 − 4.5) / 2.5) = 3.
func TestAdmitThreeLongLivedEditorsAt64GB(t *testing.T) {
	b := budgetAt(t, 64, "")
	if b.RAMGB != 16 || b.MaxEditorsAllowed() != 3 {
		t.Fatalf("budget %+v, max_editors %d; want 16 GB and 3", b, b.MaxEditorsAllowed())
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{101: true, 102: true, 103: true, 104: true}}
	for i, n := range []string{"1", "2", "3"} {
		got := admit(t, home, c, b, Editor{Project: slot(n), PID: 101 + i, Mode: "headless",
			OpenedByUpool: true}, "")
		if got.ChargeGB != 2.5 || got.ColdImport {
			t.Errorf("slot-%s charged %v, cold %t; want 2.5 and long-lived", n, got.ChargeGB,
				got.ColdImport)
		}
	}
	admit(t, home, c, b, Editor{Project: slot("4"), PID: 104, Mode: "gui"}, "max_editors")
	l := load(t, home)
	if got := projects(l); len(got) != 3 || got[2] != slot("3") {
		t.Errorf("editors = %v, want slot-1 to slot-3", got)
	}
	if got := l.Charge(b); got != 9.5 {
		t.Errorf("charge = %v GB, want 2 + 3 × 2.5 = 9.5", got)
	}
}

// With max_cold_imports 1, a second cold import is refused while the first runs, and charged
// 4.5 GB instead of editor_ram_gb (A9); a long-lived Editor is still admitted beside it.
func TestAdmitRefusesASecondConcurrentColdImport(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{201: true, 202: true, 203: true}}
	got := admit(t, home, c, b, Editor{Project: slot("1"), PID: 201, ColdImport: true}, "")
	if got.ChargeGB != ColdImportGB {
		t.Errorf("the cold import is charged %v, want %v", got.ChargeGB, ColdImportGB)
	}
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 202, ColdImport: true},
		"max_cold_imports")
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 203}, "")
	if got := load(t, home).Charge(b); got != 2+4.5+2.5 {
		t.Errorf("charge = %v GB, want 2 + 4.5 + 2.5", got)
	}
}

// An Editor whose PID is no longer a process is dropped before the decision: its charge and its
// place are released, so the Editor it was blocking is admitted, and the drop is written.
func TestAdmitReleasesADeadEditorsCharge(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{301: true, 302: true, 303: true, 304: true}}
	for i, n := range []string{"1", "2", "3"} {
		admit(t, home, c, b, Editor{Project: slot(n), PID: 301 + i}, "")
	}
	delete(c.pids, 302)
	admit(t, home, c, b, Editor{Project: slot("4"), PID: 304}, "")
	if got := projects(load(t, home)); len(got) != 3 || got[0] != slot("1") ||
		got[1] != slot("3") || got[2] != slot("4") {
		t.Errorf("editors = %v, want slot-1, slot-3 and slot-4", got)
	}
}

// A refusal still writes the drops of dead Editors that came before it.
func TestAdmitRefusedStillDropsDeadEditors(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{401: true, 402: true}}
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 401, ColdImport: true}, "")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 402}, "")
	delete(c.pids, 402)
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 403, ColdImport: true},
		"max_cold_imports")
	if got := projects(load(t, home)); len(got) != 1 || got[0] != slot("1") {
		t.Errorf("editors = %v, want slot-1 alone", got)
	}
}

// The helper charge is counted once, however many Editors are up: with a 9.5 GB budget and
// max_editors set out of the way, three Editors fit (2 + 3 × 2.5) and a fourth is refused by the
// budget. Charged per Editor, the third would not have fit. An empty ledger charges nothing.
func TestAdmitCountsTheHelperChargeOnce(t *testing.T) {
	b := budgetAt(t, 64, "editor_ram_budget_gb = 9.5\nmax_editors = 10\n")
	if got := (Ledger{}).Charge(b); got != 0 {
		t.Errorf("an empty ledger charges %v GB, want 0", got)
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{501: true, 502: true, 503: true, 504: true}}
	for i, n := range []string{"1", "2", "3"} {
		admit(t, home, c, b, Editor{Project: slot(n), PID: 501 + i}, "")
	}
	admit(t, home, c, b, Editor{Project: slot("4"), PID: 504}, "editor_ram_budget_gb")
	if got := load(t, home).Charge(b); got != 9.5 {
		t.Errorf("charge = %v GB, want 9.5", got)
	}
}

// A max_editors a layer sets is the cap, and it is not derived.
func TestAdmitUsesMaxEditorsWhenSet(t *testing.T) {
	b := budgetAt(t, 64, "max_editors = 1\n")
	if b.MaxEditorsAllowed() != 1 {
		t.Fatalf("max_editors = %d, want 1", b.MaxEditorsAllowed())
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{601: true, 602: true}}
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 601}, "")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 602}, "max_editors")
}

// A process check that fails for any other reason than no process stops the admission and
// writes nothing.
func TestAdmitStopsWhenAProcessCannotBeChecked(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	admit(t, home, liveProcs{pids: map[int]bool{701: true}}, b,
		Editor{Project: slot("1"), PID: 701}, "")
	broken := liveProcs{err: errors.New("sysctl failed")}
	if _, err := Admit(context.Background(), home, broken, b,
		Editor{Project: slot("2"), PID: 702}); err == nil {
		t.Fatal("Admit succeeded with the process table unreadable")
	}
	if got := projects(load(t, home)); len(got) != 1 || got[0] != slot("1") {
		t.Errorf("editors = %v, want slot-1 alone", got)
	}
}

// Ending a cold import gives back its place and charges editor_ram_gb from then on, so another
// cold import is admitted; a project with no Editor is ErrNotInLedger.
func TestEndColdImport(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{801: true, 802: true}}
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 801, ColdImport: true}, "")
	if err := EndColdImport(context.Background(), home, c, b, slot("1")+"/"); err != nil {
		t.Fatal(err)
	}
	e := load(t, home).Editors[0]
	if e.ColdImport || e.ChargeGB != 2.5 {
		t.Errorf("slot-1 = %+v, want long-lived and charged 2.5", e)
	}
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 802, ColdImport: true}, "")
	err := EndColdImport(context.Background(), home, c, b, slot("9"))
	if !errors.Is(err, ErrNotInLedger) {
		t.Errorf("EndColdImport(slot-9) = %v, want ErrNotInLedger", err)
	}
}

// Remove drops the Editor that has the project open, however the path is spelled, and reports
// whether there was one.
func TestRemove(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{901: true, 902: true}}
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 901}, "")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 902}, "")
	for _, want := range []bool{true, false} {
		found, err := Remove(context.Background(), home, c, slot("1")+"/")
		if err != nil || found != want {
			t.Errorf("Remove(slot-1) = %t, %v; want %t", found, err, want)
		}
	}
	if got := projects(load(t, home)); len(got) != 1 || got[0] != slot("2") {
		t.Errorf("editors = %v, want slot-2 alone", got)
	}
}
