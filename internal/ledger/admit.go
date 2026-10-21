package ledger

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/proc"
)

// ColdImportGB is an Editor's charge while its cold import runs, instead of the long-lived charge:
// the spike measured a cold-importing Editor's whole process at 4.2 to 4.5 GB, so it is not
// charged on top of it. It is a constant, not a setting (docs/contracts.md (h); A9).
const ColdImportGB = 4.5

// Budget is what an admission is checked against: the settings of docs/contracts.md (h).
type Budget struct {
	// RAMGB is editor_ram_budget_gb, the RAM Editors may be charged in all.
	RAMGB float64
	// EditorGB is editor_ram_gb, a long-lived Editor's charge.
	EditorGB float64
	// HelperGB is helper_ram_gb, charged once while any Editor is up.
	HelperGB float64
	// MaxEditors is max_editors, when MaxEditorsSet says a layer set it; otherwise the cap is
	// derived from the others (MaxEditorsAllowed).
	MaxEditors    int
	MaxEditorsSet bool
	// MaxColdImports is max_cold_imports, the cold imports that may run at once.
	MaxColdImports int
}

// NewBudget is the budget c's settings give.
func NewBudget(c config.Config) Budget {
	b := Budget{RAMGB: c.EditorRAMBudgetGB.Value, EditorGB: c.EditorRAMGB.Value,
		HelperGB: c.HelperRAMGB.Value, MaxColdImports: c.MaxColdImports.Value}
	if c.MaxEditors.Set() {
		b.MaxEditors, b.MaxEditorsSet = c.MaxEditors.Value, true
	}
	return b
}

// MaxEditorsAllowed is max_editors, or when it is unset the Editors that fit beside the helper
// charge and room for every cold import max_cold_imports allows:
// floor((editor_ram_budget_gb − helper_ram_gb − 4.5 × max_cold_imports) / editor_ram_gb), 3 at
// 64 GB of RAM (docs/contracts.md (h); A9).
func (b Budget) MaxEditorsAllowed() int {
	if b.MaxEditorsSet {
		return b.MaxEditors
	}
	if b.EditorGB <= 0 {
		return 0
	}
	n := (b.RAMGB - b.HelperGB - ColdImportGB*float64(b.MaxColdImports)) / b.EditorGB
	return max(0, int(math.Floor(n+epsilon)))
}

// epsilon absorbs the float error of sums like 2 + 2.5 + 2.5 + 2.5 against a budget of 9.5.
const epsilon = 1e-9

// Charge is what l's Editors charge against the budget: each one's charge_gb, 4.5 while it cold
// imports and editor_ram_gb otherwise (A9), and the helper charge once if any Editor is up. The
// helper charge is computed here, never stored (D-editors).
func (l Ledger) Charge(b Budget) float64 {
	if len(l.Editors) == 0 {
		return 0
	}
	total := b.HelperGB
	for _, e := range l.Editors {
		total += e.ChargeGB
	}
	return total
}

// coldImports counts the Editors holding a cold-import place.
func (l Ledger) coldImports() int {
	n := 0
	for _, e := range l.Editors {
		if e.ColdImport {
			n++
		}
	}
	return n
}

// Refusal is Admit's error when a limit refuses an Editor; Limit is the setting that did.
type Refusal struct {
	// Limit is the setting: max_cold_imports, max_editors or editor_ram_budget_gb.
	Limit string
	// Why says what the Editor would have taken past it.
	Why string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("%s refuses another Editor: %s", r.Limit, r.Why)
}

// Admit adds e to the ledger under home if b admits it, and returns the Editor as recorded, its
// charge_gb set: ColdImportGB while e.ColdImport, else b.EditorGB. Under ~/.upool/lock it first
// drops every Editor whose PID c finds no process for, then refuses, with a *Refusal naming the
// limit, an Editor that would take the cold imports past max_cold_imports, the Editors past
// MaxEditorsAllowed, or the charge, the helper's included, past editor_ram_budget_gb
// (docs/contracts.md (h); docs/design.md, "Editors: budget by RAM, serialize cold imports as a
// courtesy, target by variable and by path"). A refusal writes only the dropped Editors.
func Admit(ctx context.Context, home string, c proc.Checker, b Budget, e Editor) (Editor, error) {
	e.ChargeGB = b.EditorGB
	if e.ColdImport {
		e.ChargeGB = ColdImportGB
	}
	var refused error
	err := Update(ctx, home, func(l *Ledger) error {
		if err := l.dropDead(c); err != nil {
			return err
		}
		if refused = b.refuse(*l, e); refused != nil {
			return nil // keep the drops
		}
		l.Editors = append(l.Editors, e)
		return nil
	})
	if err != nil {
		return Editor{}, err
	}
	if refused != nil {
		return Editor{}, refused
	}
	return e, nil
}

// refuse returns the Refusal for adding e to l, or nil if b admits it.
func (b Budget) refuse(l Ledger, e Editor) error {
	if cold := l.coldImports(); e.ColdImport && cold+1 > b.MaxColdImports {
		return &Refusal{Limit: "max_cold_imports", Why: fmt.Sprintf(
			"%d cold imports are running, and it allows %d at once", cold, b.MaxColdImports)}
	}
	if n, most := len(l.Editors), b.MaxEditorsAllowed(); n+1 > most {
		return &Refusal{Limit: "max_editors", Why: fmt.Sprintf(
			"%d Editors are open, and it allows %d", n, most)}
	}
	with := Ledger{Editors: append(l.Editors[:len(l.Editors):len(l.Editors)], e)}
	if charge := with.Charge(b); charge > b.RAMGB+epsilon {
		return &Refusal{Limit: "editor_ram_budget_gb", Why: fmt.Sprintf(
			"the Editors would be charged %.1f GB, over its %.1f GB", charge, b.RAMGB)}
	}
	return nil
}

// Remove drops the Editor that has project open, compared by canonical path, from the ledger
// under home, with every Editor whose PID c finds no process for, under ~/.upool/lock. It reports
// whether project's Editor was there.
func Remove(ctx context.Context, home string, c proc.Checker, project string) (bool, error) {
	key, err := paths.Key(project)
	if err != nil {
		return false, err
	}
	found := false
	err = Update(ctx, home, func(l *Ledger) error {
		if err := l.dropDead(c); err != nil {
			return err
		}
		kept := l.Editors[:0]
		for _, e := range l.Editors {
			ek, err := paths.Key(e.Project)
			if err != nil {
				return err
			}
			if ek == key {
				found = true
				continue
			}
			kept = append(kept, e)
		}
		l.Editors = kept
		return nil
	})
	return found, err
}

// ErrNotInLedger is EndColdImport's error when no Editor in the ledger has the project open.
var ErrNotInLedger = errors.New("no Editor in " + shown + " has it open")

// EndColdImport marks the cold import of the Editor that has project open as done, under
// ~/.upool/lock, after dropping every Editor whose PID c finds no process for: it gives back its
// cold-import place and is charged b.EditorGB from then on (A9).
func EndColdImport(ctx context.Context, home string, c proc.Checker, b Budget,
	project string) error {
	key, err := paths.Key(project)
	if err != nil {
		return err
	}
	return Update(ctx, home, func(l *Ledger) error {
		if err := l.dropDead(c); err != nil {
			return err
		}
		for i := range l.Editors {
			ek, err := paths.Key(l.Editors[i].Project)
			if err != nil {
				return err
			}
			if ek == key {
				l.Editors[i].ColdImport, l.Editors[i].ChargeGB = false, b.EditorGB
				return nil
			}
		}
		return fmt.Errorf("ending the cold import of %s: %w", project, ErrNotInLedger)
	})
}

// dropDead removes every Editor whose PID c finds no process for: its charge and any cold-import
// place it held go with it (docs/contracts.md (e), editors[].pid). The ledger keeps no start time,
// so any process at the PID keeps its Editor, the safe mistake LiveIn makes too.
func (l *Ledger) dropDead(c proc.Checker) error {
	kept := l.Editors[:0]
	for _, e := range l.Editors {
		_, err := c.Start(e.PID)
		if errors.Is(err, proc.ErrNoProcess) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking the Editor of %s, process %d: %w", e.Project, e.PID, err)
		}
		kept = append(kept, e)
	}
	l.Editors = kept
	return nil
}
