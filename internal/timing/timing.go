// Package timing times the steps of the operations that move a slot or an Editor, take, take
// --confirm, release, refresh, open and close, and appends each operation's timings as one line to
// its pool's timings log, ~/.upool/pools/<repo-key>/timings.jsonl: beside state.json, and never in
// the slot, which is reset and cloned (#266; docs/contracts.md (e), "The timings log"). The command
// reports the same timings in its envelope, or on stderr (internal/output.Timings).
package timing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/weslo/upool/internal/output"
)

// LogFile is the timings log's name in ~/.upool/pools/<repo-key>/.
const LogFile = "timings.jsonl"

// Timer times one operation. A nil *Timer times nothing, so that work an operation shares with a
// command that reports no timings, such as the take and release `upool run` makes, runs as it is.
type Timer struct {
	now   func() time.Time
	start time.Time
	t     output.Timings
	// home and key name the pool whose log gets the operation's line; key is "" until the
	// operation has found its pool.
	home, key string
}

// Start starts timing operation, with now as the clock.
func Start(now func() time.Time, operation string) *Timer {
	return &Timer{now: now, start: now(), t: output.Timings{Operation: operation}}
}

// Pool names the pool the operation acts on, by the home directory its state is under and its
// repo key. An operation reports and logs its timings only once it has found its pool, so that
// a command line refused before then, such as one outside any pool, reports none.
func (t *Timer) Pool(home, key string) {
	if t != nil {
		t.home, t.key = home, key
	}
}

// Slot names the slot the operation acts on.
func (t *Timer) Slot(name string) {
	if t != nil {
		t.t.Slot = name
	}
}

// Step runs fn as the step name, on slot when that is not the operation's own slot, and records
// how long it took if fn succeeds: an operation reports the steps it completed.
func (t *Timer) Step(name, slot string, fn func() error) error {
	if t == nil {
		return fn()
	}
	begin := t.now()
	if err := fn(); err != nil {
		return err
	}
	t.Since(name, slot, begin)
	return nil
}

// Since records the step name, on slot when that is not the operation's own slot, which began at
// begin and has just completed. It is for a step whose slot is known only once it has run, such
// as a clone's.
func (t *Timer) Since(name, slot string, begin time.Time) {
	if t != nil {
		t.t.Steps = append(t.t.Steps, output.Step{Name: name, Slot: slot,
			MS: t.now().Sub(begin).Milliseconds()})
	}
}

// Finish ends the operation, which succeeded if ok: it returns its timings and appends them to its
// pool's log, returning the log's error as well. An operation that never found its pool, or a nil
// Timer, has no timings, and Finish returns nil and logs nothing.
func (t *Timer) Finish(ok bool) (*output.Timings, error) {
	if t == nil || t.key == "" {
		return nil, nil
	}
	timings := t.t
	if timings.Steps == nil {
		timings.Steps = []output.Step{}
	}
	timings.TotalMS = t.now().Sub(t.start).Milliseconds()
	line, err := json.Marshal(entry{Time: t.start.UTC(), Operation: timings.Operation,
		Slot: timings.Slot, OK: ok, Steps: timings.Steps, TotalMS: timings.TotalMS})
	if err != nil {
		return &timings, fmt.Errorf("encoding the timings: %w", err)
	}
	return &timings, appendLine(t.home, t.key, append(line, '\n'))
}

// entry is one line of the timings log: the operation's timings, with when it started and whether
// it succeeded.
type entry struct {
	Time      time.Time     `json:"time"`
	Operation string        `json:"operation"`
	Slot      string        `json:"slot,omitempty"`
	OK        bool          `json:"ok"`
	Steps     []output.Step `json:"steps"`
	TotalMS   int64         `json:"total_ms"`
}

// appendLine appends line to the timings log of the pool key under home. It writes the line in one
// write to a file opened for appending, so that the lines of operations that end together do not
// interleave, and takes no lock, as refresh.log is written: nothing reads the log back to rewrite
// it (docs/contracts.md (e), the timings log). It creates the log but not
// the pool's directory, which only the pool's state file makes, so that an operation that ends
// after `upool deinit` removed its pool leaves nothing behind.
func appendLine(home, key string, line []byte) error {
	path := filepath.Join(home, ".upool", "pools", key, LogFile)
	shown := "~/.upool/pools/" + key + "/" + LogFile
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", shown, err)
	}
	_, err = f.Write(line)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("appending to %s: %w", shown, err)
	}
	return nil
}
