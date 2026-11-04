package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/weslo/upool/internal/ledger"
	"github.com/weslo/upool/internal/paths"
)

// openLongAfter is how long an Editor may have been open before status flags it. On the first
// benchmark run no session kept an Editor open for its work longer than about an hour, and two
// then kept theirs open 2 h 50 min waiting on review; twice that hour flags none of the work and
// most of the wait (docs/contracts.md (h), the open-for flag; #281). It is a constant, not a
// setting, and it measures time since `upool open` saw the Editor ready, not time idle: upool
// hears none of a session's own Unity calls.
const openLongAfter = 2 * time.Hour

// editorStatus is an Editor the ledger charges in a pool, with its charge and how long it has been
// open, as status reports it (docs/design.md, "One binary, a dozen commands, one JSON file per
// pool", the `status` row; docs/contracts.md (e), the Editor ledger; #287; #281). It is what
// editors.json records, read without the lock: an Editor whose process has gone is still listed
// until the next admission or doctor --fix takes it out.
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
	// ReadyAt is when `upool open` saw the Editor ready, the ledger's ready_at, and absent when the
	// ledger records none: for an Editor still opening, one a seed refresh opened, or one recorded
	// before upool kept the time (#281).
	ReadyAt *time.Time `json:"ready_at,omitempty"`
	// OpenLong is set on an Editor that has been open longer than openLongAfter, counted in whole
	// minutes, from 2h1m (#281).
	OpenLong bool `json:"open_long,omitempty"`
	// openFor is how long the Editor has been open when status ran, when ReadyAt is known.
	openFor time.Duration
}

// human is the Editor's line in status's table output, such as "slot-1's Editor: gui, process
// 51207, charged 6.0 GB, open for 45m", and for an Editor open longer than openLongAfter,
// "…, open for 2h50m, over 2h: close it with upool close once its session's work is done".
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
	if e.ReadyAt != nil {
		line += ", open for " + leaseAge(e.openFor)
	}
	if e.OpenLong {
		line += ", over " + leaseAge(openLongAfter) +
			": close it with upool close once its session's work is done"
	}
	return line
}

// editorsIn returns the Editors of l in the pool p: those whose project is one of p's slots or its
// main checkout, in the ledger's order, compared as canonical paths, each with how long it has been
// open at now. An Editor in a pool registered inside p's checkout is that pool's, not p's.
func editorsIn(p poolStatus, l ledger.Ledger, now time.Time) ([]editorStatus, error) {
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
		es := editorStatus{Slot: name, Project: e.Project, PID: e.PID, Mode: e.Mode,
			ColdImport: e.ColdImport, ChargeGB: e.ChargeGB}
		if e.ReadyAt != nil {
			at := e.ReadyAt.UTC()
			es.ReadyAt, es.openFor = &at, now.Sub(at)
			// In the whole minutes the line shows it in, so that no Editor flagged as over 2h
			// shows as open for 2h.
			es.OpenLong = es.openFor.Truncate(time.Minute) > openLongAfter
		}
		in = append(in, es)
	}
	return in, nil
}

// writeEditors writes a line for each of editors to b, as Human does after a pool's table.
func writeEditors(b *strings.Builder, editors []editorStatus) {
	for _, e := range editors {
		b.WriteString(e.human() + "\n")
	}
}
