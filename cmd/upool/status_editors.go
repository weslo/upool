package main

import (
	"fmt"
	"strings"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/paths"
)

// editorStatus is an Editor the ledger charges in a pool, with its charge, as status reports it
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `status` row;
// docs/contracts.md (e), the Editor ledger; #287). It is what editors.json records, read without
// the lock: an Editor whose process has gone is still listed until the next admission or doctor
// --fix takes it out.
type editorStatus struct {
	// Slot is the name of the slot the Editor has open, or "" for an Editor in the pool's main
	// checkout, which a seed refresh opens (#98).
	Slot    string `json:"slot,omitempty"`
	Project string `json:"project"`
	PID     int    `json:"pid"`
	Mode    string `json:"mode"`
	// ColdImport is whether the Editor holds a cold-import place, charged as a cold import.
	ColdImport bool `json:"cold_import"`
	// ChargeGB is what the ledger charges it: its mode's charge, or a cold import's (A9; #287).
	ChargeGB float64 `json:"charge_gb"`
}

// human is the Editor's line in status's table output, such as "slot-1's Editor: gui, process
// 51207, charged 6.0 GB".
func (e editorStatus) human() string {
	name := e.Project
	if e.Slot != "" {
		name = e.Slot
	}
	line := fmt.Sprintf("%s's Editor: %s, process %d, charged %.1f GB", name, e.Mode, e.PID,
		e.ChargeGB)
	if e.ColdImport {
		line += " as a cold import"
	}
	return line
}

// editorsIn returns the Editors of l in the pool p: those whose project is one of p's slots or its
// main checkout, in the ledger's order, compared as canonical paths. An Editor in a pool
// registered inside p's checkout is that pool's, not p's.
func editorsIn(p poolStatus, l ledger.Ledger) ([]editorStatus, error) {
	repo, err := paths.Key(p.Repo)
	if err != nil {
		return nil, err
	}
	slots := map[string]string{}
	for _, s := range p.Slots {
		key, err := paths.Key(s.Path)
		if err != nil {
			return nil, err
		}
		slots[key] = s.Name
	}
	var in []editorStatus
	for _, e := range l.Editors {
		key, err := paths.Key(e.Project)
		if err != nil {
			return nil, err
		}
		name, isSlot := slots[key]
		if !isSlot && key != repo {
			continue
		}
		in = append(in, editorStatus{Slot: name, Project: e.Project, PID: e.PID, Mode: e.Mode,
			ColdImport: e.ColdImport, ChargeGB: e.ChargeGB})
	}
	return in, nil
}

// writeEditors writes a line for each of editors to b, as Human does after a pool's table.
func writeEditors(b *strings.Builder, editors []editorStatus) {
	for _, e := range editors {
		b.WriteString(e.human() + "\n")
	}
}
