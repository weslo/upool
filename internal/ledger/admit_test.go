package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/config"
)

// The tests of admission against the RAM budget (docs/contracts.md (h); A9; #287), on
// editors.json in a temporary home, with a liveProcs standing in for the operating system's
// process table.

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

// refusal admits e, failing the test unless a Refusal naming wantLimit refuses it, and returns
// the refusal's message.
func refusal(t *testing.T, home string, c liveProcs, b Budget, e Editor, wantLimit string) string {
	t.Helper()
	_, err := Admit(context.Background(), home, c, b, e)
	var r *Refusal
	if !errors.As(err, &r) || r.Limit != wantLimit {
		t.Fatalf("Admit(%s) = %v, want a refusal by %s", e.Project, err, wantLimit)
	}
	return err.Error()
}

// projects lists the ledger's Editors by project, in order.
func projects(l Ledger) []string {
	var out []string
	for _, e := range l.Editors {
		out = append(out, e.Project)
	}
	return out
}

// The acceptance test of #287: an Editor is charged by its mode, editor_ram_gb_gui (6) for gui and
// editor_ram_gb_headless (2.5) for headless, and a cold import 4.5 or its mode's charge if larger,
// so that a GUI Editor is not charged less while it imports than after.
func TestAdmitChargesEachEditorByItsMode(t *testing.T) {
	b := budgetAt(t, 64, "")
	if b.GUIGB != 6 || b.HeadlessGB != 2.5 || b.HelperGB != 2 {
		t.Fatalf("budget %+v, want the GUI charge 6, the headless 2.5 and the helper 2", b)
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{101: true, 102: true, 103: true, 104: true}}
	for _, tt := range []struct {
		e    Editor
		want float64
	}{
		{Editor{Project: slot("1"), PID: 101, Mode: "gui"}, 6},
		{Editor{Project: slot("2"), PID: 102, Mode: "headless"}, 2.5},
		{Editor{Project: slot("3"), PID: 103, Mode: "headless", ColdImport: true}, ColdImportGB},
	} {
		if got := admit(t, home, c, b, tt.e, ""); got.ChargeGB != tt.want {
			t.Errorf("%s %s, cold %t: charged %v, want %v", tt.e.Project, tt.e.Mode,
				tt.e.ColdImport, got.ChargeGB, tt.want)
		}
	}
	if got := load(t, home).Charge(b); got != 2+6+2.5+4.5 {
		t.Errorf("charge = %v GB, want 2 + 6 + 2.5 + 4.5", got)
	}
	if got := b.EditorCharge("gui", true); got != 6 {
		t.Errorf("a cold-importing GUI Editor is charged %v, want 6, its mode's charge", got)
	}
}

// The acceptance test of #85 as #287 revisits it, with 64 GB of RAM: the budget, a third of it,
// admits three GUI Editors, 2 + 3 × 6 = 20 GB against 21.3, and refuses a fourth Editor of either
// mode by editor_ram_budget_gb, naming the charge.
func TestAdmitThreeGUIEditorsAt64GB(t *testing.T) {
	b := budgetAt(t, 64, "")
	if b.RAMGB != 64.0/3 || b.MaxEditorsAllowed() != 6 {
		t.Fatalf("budget %+v, max_editors %d; want 64 / 3 GB and 6", b, b.MaxEditorsAllowed())
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{101: true, 102: true, 103: true, 104: true}}
	for i, n := range []string{"1", "2", "3"} {
		got := admit(t, home, c, b, Editor{Project: slot(n), PID: 101 + i, Mode: "gui",
			OpenedByUpool: true}, "")
		if got.ChargeGB != 6 || got.ColdImport {
			t.Errorf("slot-%s charged %v, cold %t; want 6 and long-lived", n, got.ChargeGB,
				got.ColdImport)
		}
	}
	for _, tt := range []struct {
		mode, want string
	}{
		{"gui", "editor_ram_budget_gb refuses another Editor, a gui one charged 6.0 GB: the " +
			"Editors would be charged 26.0 GB, over its 21.3 GB"},
		{"headless", "editor_ram_budget_gb refuses another Editor, a headless one charged 2.5 " +
			"GB: the Editors would be charged 22.5 GB, over its 21.3 GB"},
	} {
		got := refusal(t, home, c, b, Editor{Project: slot("4"), PID: 104, Mode: tt.mode},
			"editor_ram_budget_gb")
		if got != tt.want {
			t.Errorf("refusal = %q, want %q", got, tt.want)
		}
	}
	l := load(t, home)
	if got := projects(l); len(got) != 3 || got[2] != slot("3") {
		t.Errorf("editors = %v, want slot-1 to slot-3", got)
	}
	if got := l.Charge(b); got != 20 {
		t.Errorf("charge = %v GB, want 2 + 3 × 6 = 20", got)
	}
}

// With 64 GB of RAM, max_editors derives as the headless Editors the budget holds beside a cold
// import's extra 2 GB, floor((21.3 − 2 − 2) / 2.5) = 6: six headless Editors are admitted, and a
// seventh is refused by max_editors, naming its charge.
func TestAdmitSixHeadlessEditorsAt64GB(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{}}
	for i := range 7 {
		c.pids[201+i] = true
	}
	for i, n := range []string{"1", "2", "3", "4", "5", "6"} {
		admit(t, home, c, b, Editor{Project: slot(n), PID: 201 + i, Mode: "headless"}, "")
	}
	got := refusal(t, home, c, b, Editor{Project: slot("7"), PID: 207, Mode: "headless"},
		"max_editors")
	want := "max_editors refuses another Editor, a headless one charged 2.5 GB: 6 Editors are " +
		"open, and it allows 6"
	if got != want {
		t.Errorf("refusal = %q, want %q", got, want)
	}
	if got := load(t, home).Charge(b); got != 17 {
		t.Errorf("charge = %v GB, want 2 + 6 × 2.5 = 17", got)
	}
}

// max_editors, unset, is derived from the budget at the smaller of the two charges, with room for
// each cold import max_cold_imports allows to cost its extra over that charge (docs/contracts.md
// (h); #287). The cases include the manual gate's budget of 10 GB, where a second GUI Editor is
// the budget's to refuse, and the machines below 20 GB of RAM, whose default admits none.
func TestBudgetDerivesMaxEditors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		ramGB    uint64
		userFile string
		want     int
	}{
		{"64 GB", 64, "", 6},
		{"32 GB", 32, "", 2},
		{"128 GB", 128, "", 15},
		{"16 GB", 16, "", 0},
		{"a budget of 10 GB", 64, "editor_ram_budget_gb = 10\n", 2},
		{"a budget of 10 GB, both charges 2.5", 64,
			"editor_ram_budget_gb = 10\neditor_ram_gb_gui = 2.5\n", 2},
		{"no cold imports", 64, "editor_ram_budget_gb = 10\nmax_cold_imports = 0\n", 3},
		{"the smaller charge over the cold import's", 64,
			"editor_ram_budget_gb = 20\neditor_ram_gb_gui = 6\neditor_ram_gb_headless = 6\n", 3},
		{"set", 64, "max_editors = 9\n", 9},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := budgetAt(t, tt.ramGB, tt.userFile).MaxEditorsAllowed(); got != tt.want {
				t.Errorf("max_editors = %d, want %d", got, tt.want)
			}
		})
	}
}

// The acceptance test of #287, a legacy config: editor_ram_gb, the one charge before the split,
// is read as the GUI charge, and the headless charge keeps its default.
func TestBudgetReadsALegacyEditorRAMGBAsTheGUICharge(t *testing.T) {
	b := budgetAt(t, 64, "editor_ram_gb = 3\n")
	if b.GUIGB != 3 || b.HeadlessGB != 2.5 {
		t.Fatalf("budget %+v, want the GUI charge 3, from editor_ram_gb, and the headless 2.5", b)
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{301: true, 302: true}}
	if got := admit(t, home, c, b, Editor{Project: slot("1"), PID: 301, Mode: "gui"}, ""); got.
		ChargeGB != 3 {
		t.Errorf("the GUI Editor is charged %v, want 3", got.ChargeGB)
	}
	if got := admit(t, home, c, b, Editor{Project: slot("2"), PID: 302, Mode: "headless"},
		""); got.ChargeGB != 2.5 {
		t.Errorf("the headless Editor is charged %v, want 2.5", got.ChargeGB)
	}
}

// The acceptance test of #287, the user's override: charges and the budget the user file sets win
// over the defaults, and over editor_ram_gb in the same file.
func TestBudgetTakesTheUsersOverrides(t *testing.T) {
	b := budgetAt(t, 64, "editor_ram_budget_gb = 10\neditor_ram_gb = 3\neditor_ram_gb_gui = 4\n"+
		"editor_ram_gb_headless = 1.5\n")
	if b.RAMGB != 10 || b.GUIGB != 4 || b.HeadlessGB != 1.5 {
		t.Fatalf("budget %+v, want 10 GB, the GUI charge 4 and the headless 1.5", b)
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{401: true, 402: true, 403: true}}
	if got := admit(t, home, c, b, Editor{Project: slot("1"), PID: 401, Mode: "gui"}, ""); got.
		ChargeGB != 4 {
		t.Errorf("the GUI Editor is charged %v, want 4", got.ChargeGB)
	}
	if got := admit(t, home, c, b, Editor{Project: slot("2"), PID: 402, Mode: "headless"},
		""); got.ChargeGB != 1.5 {
		t.Errorf("the headless Editor is charged %v, want 1.5", got.ChargeGB)
	}
	// 2 + 4 + 1.5 + 4 = 11.5 GB is past the user's 10.
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 403, Mode: "gui"}, "editor_ram_budget_gb")
}

// With max_cold_imports 1, a second cold import is refused while the first runs, naming the charge
// it would have had as a cold import (A9); a long-lived Editor is still admitted beside it.
func TestAdmitRefusesASecondConcurrentColdImport(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{201: true, 202: true, 203: true}}
	got := admit(t, home, c, b, Editor{Project: slot("1"), PID: 201, Mode: "headless",
		ColdImport: true}, "")
	if got.ChargeGB != ColdImportGB {
		t.Errorf("the cold import is charged %v, want %v", got.ChargeGB, ColdImportGB)
	}
	msg := refusal(t, home, c, b, Editor{Project: slot("2"), PID: 202, Mode: "headless",
		ColdImport: true}, "max_cold_imports")
	want := "max_cold_imports refuses another Editor, a headless one charged 4.5 GB as a cold " +
		"import: 1 cold imports are running, and it allows 1 at once"
	if msg != want {
		t.Errorf("refusal = %q, want %q", msg, want)
	}
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 203, Mode: "headless"}, "")
	if got := load(t, home).Charge(b); got != 2+4.5+2.5 {
		t.Errorf("charge = %v GB, want 2 + 4.5 + 2.5", got)
	}
}

// An Editor whose PID is no longer a process is dropped before the decision: its charge and its
// place are released, so the Editor it was blocking is admitted, and the drop is written.
func TestAdmitReleasesADeadEditorsCharge(t *testing.T) {
	b := budgetAt(t, 64, "max_editors = 3\n")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{301: true, 302: true, 303: true, 304: true}}
	for i, n := range []string{"1", "2", "3"} {
		admit(t, home, c, b, Editor{Project: slot(n), PID: 301 + i, Mode: "headless"}, "")
	}
	delete(c.pids, 302)
	admit(t, home, c, b, Editor{Project: slot("4"), PID: 304, Mode: "headless"}, "")
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
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 401, Mode: "headless", ColdImport: true},
		"")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 402, Mode: "headless"}, "")
	delete(c.pids, 402)
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 403, Mode: "headless", ColdImport: true},
		"max_cold_imports")
	if got := projects(load(t, home)); len(got) != 1 || got[0] != slot("1") {
		t.Errorf("editors = %v, want slot-1 alone", got)
	}
}

// The helper charge is counted once, however many Editors are up: with a 9.5 GB budget and
// max_editors set out of the way, three headless Editors fit (2 + 3 × 2.5) and a fourth is
// refused by the budget. Charged per Editor, the third would not have fit. An empty ledger
// charges nothing.
func TestAdmitCountsTheHelperChargeOnce(t *testing.T) {
	b := budgetAt(t, 64, "editor_ram_budget_gb = 9.5\nmax_editors = 10\n")
	if got := (Ledger{}).Charge(b); got != 0 {
		t.Errorf("an empty ledger charges %v GB, want 0", got)
	}
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{501: true, 502: true, 503: true, 504: true}}
	for i, n := range []string{"1", "2", "3"} {
		admit(t, home, c, b, Editor{Project: slot(n), PID: 501 + i, Mode: "headless"}, "")
	}
	admit(t, home, c, b, Editor{Project: slot("4"), PID: 504, Mode: "headless"},
		"editor_ram_budget_gb")
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
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 601, Mode: "headless"}, "")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 602, Mode: "headless"}, "max_editors")
}

// A process check that fails for any other reason than no process stops the admission and
// writes nothing.
func TestAdmitStopsWhenAProcessCannotBeChecked(t *testing.T) {
	b := budgetAt(t, 64, "")
	home := tempDir(t)
	admit(t, home, liveProcs{pids: map[int]bool{701: true}}, b,
		Editor{Project: slot("1"), PID: 701, Mode: "headless"}, "")
	broken := liveProcs{err: errors.New("sysctl failed")}
	if _, err := Admit(context.Background(), home, broken, b,
		Editor{Project: slot("2"), PID: 702, Mode: "headless"}); err == nil {
		t.Fatal("Admit succeeded with the process table unreadable")
	}
	if got := projects(load(t, home)); len(got) != 1 || got[0] != slot("1") {
		t.Errorf("editors = %v, want slot-1 alone", got)
	}
}

// Ending a cold import gives back its place and charges the Editor's mode from then on, 2.5 for
// a headless one and 6 for a GUI one, so another cold import is admitted; a project with no
// Editor is ErrNotInLedger.
func TestEndColdImport(t *testing.T) {
	b := budgetAt(t, 64, "max_cold_imports = 2\n")
	home := tempDir(t)
	c := liveProcs{pids: map[int]bool{801: true, 802: true, 803: true}}
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 801, Mode: "headless", ColdImport: true},
		"")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 802, Mode: "gui", ColdImport: true}, "")
	for _, n := range []string{"1", "2"} {
		if err := EndColdImport(context.Background(), home, c, b, slot(n)+"/"); err != nil {
			t.Fatal(err)
		}
	}
	l := load(t, home)
	if e := l.Editors[0]; e.ColdImport || e.ChargeGB != 2.5 {
		t.Errorf("slot-1 = %+v, want long-lived and charged 2.5", e)
	}
	if e := l.Editors[1]; e.ColdImport || e.ChargeGB != 6 {
		t.Errorf("slot-2 = %+v, want long-lived and charged 6", e)
	}
	admit(t, home, c, b, Editor{Project: slot("3"), PID: 803, Mode: "headless", ColdImport: true},
		"")
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
	admit(t, home, c, b, Editor{Project: slot("1"), PID: 901, Mode: "headless"}, "")
	admit(t, home, c, b, Editor{Project: slot("2"), PID: 902, Mode: "headless"}, "")
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
