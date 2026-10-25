// Package block keeps upool's marked block in an instruction file such as CLAUDE.md: the block's
// body between a begin and an end marker that carry its version, inserted once, replaced in place
// by a later version, and with every byte outside the markers left as it was (docs/design.md, "One
// binary, a dozen commands, one JSON file per pool", the `init` row; "Hooks route, skills teach,
// leases and lanes keep agents from colliding": a marked, idempotent block). An earlier upool's
// `init` upserted it into a project's CLAUDE.md and AGENTS.md (A26), and `init` and `deinit` now
// strip it from them (A43).
//
// A marker is a line of its own, an HTML comment so that it does not show when the Markdown is
// rendered: `<!-- upool:begin v1 -->` and `<!-- upool:end v1 -->`. A file holds at most one block;
// one whose markers do not pair up is refused rather than guessed at.
package block

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strconv"
)

// Block is a block's body and its version, which its markers carry.
type Block struct {
	// Version is at least 1. A copy of a lower version is replaced, and one of a higher version
	// kept.
	Version int
	// Body is the text between the markers. A missing final newline is supplied.
	Body string
}

// Action is what an upsert or a removal did.
type Action string

// The actions of an upsert, as init's --json reports them.
const (
	// Created means the file did not exist, and now holds the block alone.
	Created Action = "created"
	// Inserted means the file had no block, and the block was appended after a blank line.
	Inserted Action = "inserted"
	// Replaced means the file had a block of the same or a lower version that differed, and the
	// block took its place.
	Replaced Action = "replaced"
	// Unchanged means the file already had the block, byte for byte.
	Unchanged Action = "unchanged"
	// KeptNewer means the file had a block of a higher version, which was kept: a newer upool
	// wrote it.
	KeptNewer Action = "kept_newer"
)

// The actions of a removal, as deinit's --json reports them.
const (
	// Removed means the file had a block, of any version, which is gone.
	Removed Action = "removed"
	// Deleted means the file held the block alone, as an upsert that created it leaves it, and is
	// gone.
	Deleted Action = "deleted"
	// Absent means the file had no block, or did not exist, and was not written.
	Absent Action = "absent"
)

// markerLine matches a marker line, without its line ending.
var markerLine = regexp.MustCompile(`^<!-- upool:(begin|end) v([1-9][0-9]{0,8}) -->$`)

// Begin and End return the begin and the end marker of version v, without a line ending.
func Begin(v int) string { return fmt.Sprintf("<!-- upool:begin v%d -->", v) }

// End returns the end marker of version v; see Begin.
func End(v int) string { return fmt.Sprintf("<!-- upool:end v%d -->", v) }

// Render returns b as it appears in a file: the begin marker, the body, and the end marker, each
// ending in a newline.
func (b Block) Render() []byte {
	var out bytes.Buffer
	out.WriteString(Begin(b.Version) + "\n")
	out.WriteString(b.Body)
	if b.Body != "" && b.Body[len(b.Body)-1] != '\n' {
		out.WriteByte('\n')
	}
	out.WriteString(End(b.Version) + "\n")
	return out.Bytes()
}

// Upsert upserts b into file, as Apply does, creating the file if it is missing, and writes it
// only if that changed it. It writes in place, so a symlinked file stays a link and its target
// takes the block.
func Upsert(file string, b Block) (Action, error) {
	doc, err := os.ReadFile(file)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	out, action, err := Apply(doc, b)
	if err != nil {
		return "", fmt.Errorf("upserting upool's block into %s: %w", file, err)
	}
	if action == Unchanged || action == KeptNewer {
		return action, nil
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", file, err)
	}
	if missing {
		return Created, nil
	}
	return action, nil
}

// Apply returns doc with b upserted, and what that did. With no block in doc, b is appended after
// a blank line, doc staying a prefix of the result. A block of b's version or a lower one is
// replaced in place by b, keeping every byte before its begin marker and after its end marker's
// line; one of a higher version is kept. Markers that do not pair into one block are an error
// naming their lines, and doc is not changed.
func Apply(doc []byte, b Block) ([]byte, Action, error) {
	if b.Version < 1 {
		return nil, "", fmt.Errorf("block version %d is below 1", b.Version)
	}
	if ms := markers([]byte(b.Body)); len(ms) > 0 {
		return nil, "", fmt.Errorf("the block's body has a marker on line %d", ms[0].line)
	}
	begin, end, found, err := find(doc)
	if err != nil {
		return nil, "", err
	}
	if !found {
		out := append([]byte(nil), doc...)
		switch {
		case len(doc) == 0:
		case doc[len(doc)-1] != '\n':
			out = append(out, "\n\n"...)
		case !bytes.HasSuffix(doc, []byte("\n\n")):
			out = append(out, '\n')
		}
		return append(out, b.Render()...), Inserted, nil
	}
	if begin.version > b.Version {
		return doc, KeptNewer, nil
	}
	out := append([]byte(nil), doc[:begin.start]...)
	out = append(out, b.Render()...)
	out = append(out, doc[end.next:]...)
	if bytes.Equal(out, doc) {
		return doc, Unchanged, nil
	}
	return out, Replaced, nil
}

// Remove removes upool's block from file, as Strip does, and writes the file only if that changed
// it, in place, as Upsert writes; a file left with nothing in it is deleted, since an upsert into
// a missing file creates it holding the block alone (`upool deinit`, #191).
func Remove(file string) (Action, error) {
	doc, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return Absent, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	out, found, err := Strip(doc)
	if err != nil {
		return "", fmt.Errorf("removing upool's block from %s: %w", file, err)
	}
	if !found {
		return Absent, nil
	}
	if len(out) == 0 {
		if err := os.Remove(file); err != nil {
			return "", fmt.Errorf("deleting %s: %w", file, err)
		}
		return Deleted, nil
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", file, err)
	}
	return Removed, nil
}

// Strip returns doc without its block, of whatever version, and whether it had one. It undoes
// Apply's insert: the block's lines go and, when nothing follows them, so does the blank line
// before them, so that a file the block was appended to after its last line reads as it did. Every
// other byte stays. Markers that do not pair into one block are an error, as for Apply.
func Strip(doc []byte) ([]byte, bool, error) {
	begin, end, found, err := find(doc)
	if err != nil || !found {
		return doc, false, err
	}
	head := doc[:begin.start]
	if end.next == len(doc) {
		head = bytes.TrimSuffix(head, []byte("\n"))
		if !bytes.HasSuffix(head, []byte("\n")) {
			head = doc[:begin.start]
		}
	}
	out := append(append([]byte(nil), head...), doc[end.next:]...)
	return out, true, nil
}

// marker is one marker line in a file.
type marker struct {
	begin   bool
	version int
	// line is the line's number, from 1; start is the offset of its first byte, and next the
	// offset just past its line ending.
	line        int
	start, next int
}

// markers returns the marker lines in doc, in order. A line ending may be "\n" or "\r\n"; the last
// line may have none.
func markers(doc []byte) []marker {
	var ms []marker
	for n, start := 1, 0; start < len(doc); n++ {
		next := len(doc)
		if i := bytes.IndexByte(doc[start:], '\n'); i >= 0 {
			next = start + i + 1
		}
		text := bytes.TrimSuffix(bytes.TrimSuffix(doc[start:next], []byte("\n")), []byte("\r"))
		if m := markerLine.FindSubmatch(text); m != nil {
			v, _ := strconv.Atoi(string(m[2])) // nine digits at most, so it fits
			ms = append(ms, marker{begin: string(m[1]) == "begin", version: v, line: n,
				start: start, next: next})
		}
		start = next
	}
	return ms
}

// find returns the begin and end markers of doc's block, and whether it has one, or an error if
// its markers do not make exactly one block: a begin marker, then an end marker of the same
// version.
func find(doc []byte) (begin, end marker, found bool, err error) {
	var open bool
	for _, m := range markers(doc) {
		switch {
		case m.begin && open:
			return marker{}, marker{}, false, fmt.Errorf("line %d begins a upool block inside "+
				"the one line %d begins", m.line, begin.line)
		case m.begin && found:
			return marker{}, marker{}, false, fmt.Errorf("line %d begins a second upool block "+
				"after the one on lines %d to %d: keep one", m.line, begin.line, end.line)
		case m.begin:
			begin, open = m, true
		case !open:
			return marker{}, marker{}, false, fmt.Errorf("line %d ends a upool block, but no "+
				"block is open there", m.line)
		case m.version != begin.version:
			return marker{}, marker{}, false, fmt.Errorf("line %d ends v%d of the upool block "+
				"that line %d begins as v%d", m.line, m.version, begin.line, begin.version)
		default:
			end, open, found = m, false, true
		}
	}
	if open {
		return marker{}, marker{}, false, fmt.Errorf("line %d begins a upool block that no line "+
			"after it ends", begin.line)
	}
	return begin, end, found, nil
}
