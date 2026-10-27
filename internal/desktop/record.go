// Package desktop reads the Claude desktop app's own record of the worktrees its sessions hold,
// ~/Library/Application Support/Claude/git-worktrees.json (docs/contracts.md A50; #192). The app
// keeps a session's entry until the session is archived or deleted, through a quit or a crash, and
// refuses the entry's path to every other session ("already in use by worktree …"). upool reads it
// only so as not to hand out, or reclaim, a slot the app still holds for a session. It is the app's
// private file: a missing file, one upool cannot read, or one in a shape upool does not know reads
// as unknown, never as an error.
package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/weslo/upool/internal/paths"
)

// RecordFile is the app's record, relative to the home directory.
const RecordFile = "Library/Application Support/Claude/git-worktrees.json"

// Record is what the app's record says of the worktrees its sessions hold.
type Record struct {
	// Known is whether the record was read; when it is false, Why says why not, and Holds finds
	// nothing.
	Known bool
	Why   string
	// worktrees maps the canonical key of each recorded worktree's path to it.
	worktrees map[string]Worktree
}

// Worktree is one entry of the record: the worktree's name, and the app's id of the session it is
// leased by, `local_<uuid>`, or "" when none is recorded.
type Worktree struct {
	Name, Session string
}

// String names the session the app records w for, and w by its name, as upool's messages give it.
func (w Worktree) String() string {
	if w.Session == "" {
		return "a session, as its worktree " + w.Name
	}
	return "session " + w.Session + ", as its worktree " + w.Name
}

// file is the part of the record upool reads: `worktrees`, an object keyed by worktree name whose
// entries hold the worktree's `name`, its absolute `path`, and `leasedBy`, the session's id or null
// (docs/evidence/session-end-probe/git-worktrees-shape.json).
type file struct {
	Worktrees map[string]struct {
		Name     string  `json:"name"`
		Path     string  `json:"path"`
		LeasedBy *string `json:"leasedBy"`
	} `json:"worktrees"`
}

// Load reads the record under home. Every entry counts, whatever its `leasedBy`, which names the
// session when it is set: the app refuses a recorded path by the entry alone.
func Load(home string) Record {
	path := filepath.Join(home, filepath.FromSlash(RecordFile))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{Why: path + " does not exist"}
	}
	if err != nil {
		return Record{Why: fmt.Sprintf("reading %s: %v", path, err)}
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return Record{Why: fmt.Sprintf("reading %s: %v", path, err)}
	}
	if f.Worktrees == nil {
		return Record{Why: path + " has no worktrees object"}
	}
	worktrees := map[string]Worktree{}
	for key, w := range f.Worktrees {
		if !filepath.IsAbs(w.Path) {
			continue
		}
		k, err := paths.Key(w.Path)
		if err != nil {
			continue
		}
		e := Worktree{Name: w.Name}
		if e.Name == "" {
			e.Name = key
		}
		if w.LeasedBy != nil {
			e.Session = *w.LeasedBy
		}
		worktrees[k] = e
	}
	return Record{Known: true, worktrees: worktrees}
}

// Holds returns the worktree the app records at path, if it records one.
func (r Record) Holds(path string) (Worktree, bool) {
	if len(r.worktrees) == 0 {
		return Worktree{}, false
	}
	k, err := paths.Key(path)
	if err != nil {
		return Worktree{}, false
	}
	w, ok := r.worktrees[k]
	return w, ok
}
