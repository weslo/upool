// Package ledger is the Editor ledger, ~/.upool/editors.json: every Unity Editor upool is charging
// against the machine's RAM budget, across all pools (docs/contracts.md (e) State schemas,
// `~/.upool/editors.json`; docs/design.md, "Editors: budget by RAM, serialize cold imports as a
// courtesy, target by variable and by path").
//
// The keys are exactly the snake_case names in (e), and the types its JSON types (A7). The helper
// charge, 2 GB once any Editor is up, is computed from the list and never stored (D-editors). The
// document is read and written only through internal/statefile, under ~/.upool/lock.
//
// The home directory is always a parameter; nothing here reads the environment (CONTRIBUTING.md,
// "Process state comes in through run").
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/weslo/upool/internal/statefile"
)

// SchemaVersion is the editors.json format this package reads and writes: a document with a
// higher schema_version is refused, and one with a lower version is written at this one (A7).
const SchemaVersion = 1

const (
	// file is editors.json, relative to ~/.upool as internal/statefile takes it.
	file = "editors.json"
	// shown is how messages name the file.
	shown = "~/.upool/editors.json"
)

// Ledger is the editors.json document.
type Ledger struct {
	SchemaVersion int      `json:"schema_version"`
	Editors       []Editor `json:"editors"`
}

// Editor is one Editor upool is charging for.
type Editor struct {
	// Project is the absolute path of the slot the Editor has open.
	Project string `json:"project"`
	// PID is the Editor's process; a dead one is reclaimed.
	PID int `json:"pid"`
	// Mode is how `upool open` launched it, "headless" or "gui".
	Mode string `json:"mode"`
	// OpenedByUpool is whether upool opened it: release closes only the Editors upool opened.
	OpenedByUpool bool `json:"opened_by_upool"`
	// ColdImport holds one of max_cold_imports while true.
	ColdImport bool `json:"cold_import"`
	// ChargeGB is the Editor's charge against the budget: 2.5 when long-lived, 4.5 while a cold
	// import runs.
	ChargeGB float64 `json:"charge_gb"`
}

// Load returns the ledger in ~/.upool/editors.json under home, without the lock (see
// statefile.Read). A missing file is a ledger with no Editors.
func Load(home string) (Ledger, error) {
	data, err := statefile.Read(home, file)
	if err != nil {
		return Ledger{}, err
	}
	return decode(data)
}

// Update rewrites ~/.upool/editors.json under home under the lock: fn gets the ledger as it is (no
// Editors when the file is missing) and changes it in place, and the result is written at
// SchemaVersion. When fn returns an error, Update returns it as it is and writes nothing. Waiting
// for the lock gives up when ctx ends, as statefile.Update does.
//
// fn runs while ~/.upool/lock is held, so it must not call anything that takes the lock, such as
// another Update.
func Update(ctx context.Context, home string, fn func(*Ledger) error) error {
	return statefile.Update(ctx, home, file, func(old []byte) ([]byte, error) {
		l, err := decode(old)
		if err != nil {
			return nil, err
		}
		if err := fn(&l); err != nil {
			return nil, err
		}
		return encode(l)
	})
}

// decode parses an editors.json document. Empty data, a missing file, is a ledger with no Editors.
//
// The version is read first, leniently, so that a newer document is refused with a message to
// upgrade rather than an error about the keys it added (A7). The document is then decoded with
// unknown keys as errors, so a rewrite never silently drops a field this upool does not know.
func decode(data []byte) (Ledger, error) {
	if len(data) == 0 {
		return Ledger{}, nil
	}
	var version struct {
		SchemaVersion int `json:"schema_version"`
	}
	// Unmarshal also rejects anything after the document, which the Decoder below would not.
	if err := json.Unmarshal(data, &version); err != nil {
		return Ledger{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	if version.SchemaVersion > SchemaVersion {
		return Ledger{}, fmt.Errorf("%s has schema_version %d, newer than the %d this upool "+
			"reads: upgrade upool", shown, version.SchemaVersion, SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l Ledger
	if err := dec.Decode(&l); err != nil {
		return Ledger{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	return l, nil
}

// encode writes l at SchemaVersion, indented, with a final newline, and editors as an array even
// when there are none (docs/contracts.md (e)).
func encode(l Ledger) ([]byte, error) {
	l.SchemaVersion = SchemaVersion
	if l.Editors == nil {
		l.Editors = []Editor{}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", shown, err)
	}
	return append(data, '\n'), nil
}
