package block

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// current is the block the tests upsert, and older a copy of it from an earlier version.
var (
	current = Block{Version: 2, Body: "## upool\n\nTake a slot with upool.\n"}
	older   = Block{Version: 1, Body: "## upool\n\nAn older text.\n"}
)

// before and after are a file's text around a block.
const (
	before = "# Project\n\nProject guidance.\n\n"
	after  = "\n## Testing\n\nRun the tests.\n"
)

// mustApply applies b to doc and fails the test on an error.
func mustApply(t *testing.T, doc string, b Block) (string, Action) {
	t.Helper()
	out, action, err := Apply([]byte(doc), b)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return string(out), action
}

func TestRenderPutsTheBodyBetweenMarkersThatCarryTheVersion(t *testing.T) {
	want := "<!-- upool:begin v2 -->\n## upool\n\nTake a slot with upool.\n<!-- upool:end v2 -->\n"
	if got := string(current.Render()); got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	noNewline := Block{Version: 1, Body: "text"}
	if got, want := string(noNewline.Render()),
		"<!-- upool:begin v1 -->\ntext\n<!-- upool:end v1 -->\n"; got != want {
		t.Errorf("Render() of a body with no final newline = %q, want %q", got, want)
	}
}

func TestInsertingTwiceLeavesOneBlock(t *testing.T) {
	doc := "# Project\n\nProject guidance.\n"
	once, action := mustApply(t, doc, current)
	if action != Inserted {
		t.Errorf("first action = %q, want %q", action, Inserted)
	}
	twice, action := mustApply(t, once, current)
	if action != Unchanged {
		t.Errorf("second action = %q, want %q", action, Unchanged)
	}
	if twice != once {
		t.Errorf("after the second upsert = %q, want it unchanged: %q", twice, once)
	}
	if n := strings.Count(twice, Begin(current.Version)); n != 1 {
		t.Errorf("begin markers = %d, want 1 in %q", n, twice)
	}
}

// The file is a prefix of the result, and the block starts after a blank line on its own.
func TestInsertAppendsAfterABlankLineAndKeepsTheFile(t *testing.T) {
	block := string(current.Render())
	tests := []struct{ name, doc, sep string }{
		{"empty", "", ""},
		{"no final newline", "# Project", "\n\n"},
		{"a final newline", "# Project\n", "\n"},
		{"a blank line already", "# Project\n\n", ""},
		{"CRLF line endings", "# Project\r\n", "\n"},
		{"text that only mentions a marker", "Do not edit `" + Begin(1) + "` by hand.\n", "\n"},
		{"an indented marker", "    " + Begin(1) + "\n", "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, action := mustApply(t, tt.doc, current)
			if action != Inserted {
				t.Errorf("action = %q, want %q", action, Inserted)
			}
			if want := tt.doc + tt.sep + block; got != want {
				t.Errorf("Apply = %q, want %q", got, want)
			}
		})
	}
}

func TestAnOlderBlockIsReplacedInPlace(t *testing.T) {
	tests := []struct{ name, before, after string }{
		{"between text", before, after},
		{"at the start", "", after},
		{"at the end", before, ""},
		{"alone", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := tt.before + string(older.Render()) + tt.after
			got, action := mustApply(t, doc, current)
			if action != Replaced {
				t.Errorf("action = %q, want %q", action, Replaced)
			}
			if want := tt.before + string(current.Render()) + tt.after; got != want {
				t.Errorf("Apply =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

// What is around the block stays byte for byte, CRLF line endings and all; the block's own lines
// are the block's.
func TestTheTextAroundAReplacedBlockIsUnchanged(t *testing.T) {
	crlfBefore := "# Project\r\n\r\nGuidance.\r\n\r\n"
	crlfAfter := "\r\n## Testing\r\n"
	old := Begin(1) + "\r\nOld text.\r\n" + End(1) + "\r\n"
	got, action := mustApply(t, crlfBefore+old+crlfAfter, current)
	if action != Replaced {
		t.Errorf("action = %q, want %q", action, Replaced)
	}
	if want := crlfBefore + string(current.Render()) + crlfAfter; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}

	// An end marker on the last line, with no newline after it, gains one.
	got, _ = mustApply(t, before+Begin(1)+"\nOld text.\n"+End(1), current)
	if want := before + string(current.Render()); got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

// A block of the current version whose body was edited by hand is put back.
func TestAnEditedBlockOfTheSameVersionIsReplaced(t *testing.T) {
	edited := Block{Version: current.Version, Body: current.Body + "A line added by hand.\n"}
	got, action := mustApply(t, before+string(edited.Render())+after, current)
	if action != Replaced {
		t.Errorf("action = %q, want %q", action, Replaced)
	}
	if want := before + string(current.Render()) + after; got != want {
		t.Errorf("Apply = %q, want %q", got, want)
	}
}

// A newer upool wrote a newer block; an older one does not downgrade it.
func TestANewerBlockIsKept(t *testing.T) {
	newer := Block{Version: current.Version + 1, Body: "A newer text.\n"}
	doc := before + string(newer.Render()) + after
	got, action := mustApply(t, doc, current)
	if action != KeptNewer {
		t.Errorf("action = %q, want %q", action, KeptNewer)
	}
	if got != doc {
		t.Errorf("Apply = %q, want it unchanged: %q", got, doc)
	}
}

func TestMarkersThatDoNotMakeOneBlockAreRefused(t *testing.T) {
	block := string(older.Render())
	tests := []struct{ name, doc, want string }{
		{"a begin marker alone", "# Project\n" + Begin(1) + "\ntext\n",
			"line 2 begins a upool block that no line after it ends"},
		{"an end marker alone", "# Project\n" + End(1) + "\n",
			"line 2 ends a upool block, but no block is open there"},
		{"an end marker before the block", End(1) + "\n" + block,
			"line 1 ends a upool block, but no block is open there"},
		{"two blocks", block + "\n" + block,
			"line 7 begins a second upool block after the one on lines 1 to 5: keep one"},
		{"a block inside a block", Begin(1) + "\n" + block + End(1) + "\n",
			"line 2 begins a upool block inside the one line 1 begins"},
		{"versions that differ", Begin(1) + "\ntext\n" + End(2) + "\n",
			"line 3 ends v2 of the upool block that line 1 begins as v1"},
		{"an end marker after the block", block + End(1) + "\n",
			"line 6 ends a upool block, but no block is open there"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, action, err := Apply([]byte(tt.doc), current)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Apply error = %v, want %q", err, tt.want)
			}
			if out != nil || action != "" {
				t.Errorf("Apply = %q, %q, want nothing with the error", out, action)
			}
		})
	}
}

func TestABlockThatCannotBeWrittenIsRefused(t *testing.T) {
	tests := []struct {
		name string
		b    Block
		want string
	}{
		{"version 0", Block{Version: 0, Body: "text\n"}, "block version 0 is below 1"},
		{"a marker in the body", Block{Version: 1, Body: "text\n" + End(1) + "\n"},
			"the block's body has a marker on line 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Apply([]byte("# Project\n"), tt.b); err == nil ||
				err.Error() != tt.want {
				t.Errorf("Apply error = %v, want %q", err, tt.want)
			}
		})
	}
}

// readFile returns file's contents and fails the test if it cannot be read.
func readFile(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpsertCreatesAMissingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "CLAUDE.md")
	action, err := Upsert(file, current)
	if err != nil || action != Created {
		t.Fatalf("Upsert = %q, %v, want %q", action, err, Created)
	}
	if got, want := readFile(t, file), string(current.Render()); got != want {
		t.Errorf("CLAUDE.md = %q, want the block alone: %q", got, want)
	}
}

func TestUpsertWritesTheFileOnlyWhenItChanges(t *testing.T) {
	file := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(file, []byte(before+string(older.Render())+after), 0o600); err != nil {
		t.Fatal(err)
	}
	if action, err := Upsert(file, current); err != nil || action != Replaced {
		t.Fatalf("Upsert = %q, %v, want %q", action, err, Replaced)
	}
	want := before + string(current.Render()) + after
	if got := readFile(t, file); got != want {
		t.Errorf("CLAUDE.md = %q, want %q", got, want)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v, want the file's own 0600 kept", info.Mode().Perm(), err)
	}

	// A read-only file that already has the block is not written, so the upsert succeeds.
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	if action, err := Upsert(file, current); err != nil || action != Unchanged {
		t.Errorf("second Upsert = %q, %v, want %q", action, err, Unchanged)
	}
	if got := readFile(t, file); got != want {
		t.Errorf("CLAUDE.md after the second upsert = %q, want %q", got, want)
	}
}

// Many repositories link CLAUDE.md to AGENTS.md; the link stays, and the file it names takes the
// block.
func TestUpsertWritesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(target, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", link); err != nil {
		t.Fatal(err)
	}
	if action, err := Upsert(link, current); err != nil || action != Inserted {
		t.Fatalf("Upsert = %q, %v, want %q", action, err, Inserted)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("CLAUDE.md: mode %v, %v, want it still a symlink", info.Mode(), err)
	}
	if got, want := readFile(t, target), before+string(current.Render()); got != want {
		t.Errorf("AGENTS.md = %q, want %q", got, want)
	}
}

func TestUpsertNamesTheFileItCannotUpdate(t *testing.T) {
	dir := t.TempDir()
	_, err := Upsert(dir, current)
	if err == nil || !strings.Contains(err.Error(), "reading "+dir) {
		t.Errorf("Upsert of a directory: error = %v, want it to say it was reading %s", err, dir)
	}

	file := filepath.Join(dir, "CLAUDE.md")
	doc := []byte("# Project\n" + Begin(1) + "\n")
	if err := os.WriteFile(file, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Upsert(file, current)
	want := "upserting upool's block into " + file + ": line 2 begins a upool block that no " +
		"line after it ends"
	if err == nil || err.Error() != want {
		t.Errorf("Upsert error = %v, want %q", err, want)
	}
	if got := readFile(t, file); got != string(doc) {
		t.Errorf("CLAUDE.md = %q, want it unchanged: %q", got, doc)
	}

	missingDir := filepath.Join(dir, "missing", "CLAUDE.md")
	if _, err := Upsert(missingDir, current); err == nil || !errors.Is(err, fs.ErrNotExist) ||
		!strings.Contains(err.Error(), "writing "+missingDir) {
		t.Errorf("Upsert in a missing directory: error = %v, want a not-exist error writing %s",
			err, missingDir)
	}
}

// Strip undoes an insert: a file the block was appended to, after a final newline, after none, or
// created holding the block alone, reads as before but for a missing final newline, which an
// insert supplies; the text around a block that is not last stays, as does a newer block's.
func TestStripUndoesAnInsert(t *testing.T) {
	tests := []struct{ name, doc, want string }{
		{"after a final newline", "# Project\n\nGuidance.\n", "# Project\n\nGuidance.\n"},
		{"after a blank line", "# Project\n\n", "# Project\n"},
		{"after no final newline", "# Project", "# Project\n"},
		{"alone", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, _ := mustApply(t, tt.doc, current)
			out, found, err := Strip([]byte(doc))
			if err != nil || !found || string(out) != tt.want {
				t.Errorf("Strip = %q, %v, %v; want %q, true, nil", out, found, err, tt.want)
			}
		})
	}
	for _, b := range []Block{older, {Version: 9, Body: "A newer text.\n"}} {
		out, found, err := Strip([]byte(before + string(b.Render()) + after))
		if err != nil || !found || string(out) != before+after {
			t.Errorf("Strip of v%d = %q, %v, %v; want %q", b.Version, out, found, err,
				before+after)
		}
	}
	if out, found, err := Strip([]byte(before)); err != nil || found || string(out) != before {
		t.Errorf("Strip with no block = %q, %v, %v; want the text as it is", out, found, err)
	}
	if _, _, err := Strip([]byte("# Project\n" + Begin(1) + "\n")); err == nil {
		t.Error("Strip of an unpaired marker: no error")
	}
}

// Remove writes a file it took the block out of, deletes one it left empty, and leaves a file with
// no block, or a missing one, unwritten.
func TestRemoveDeletesAFileLeftEmpty(t *testing.T) {
	dir := t.TempDir()
	kept, alone := filepath.Join(dir, "CLAUDE.md"), filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(kept, []byte("# Project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{kept, alone} {
		if _, err := Upsert(f, current); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		file string
		want Action
	}{{kept, Removed}, {alone, Deleted}, {kept, Absent}, {alone, Absent}} {
		if got, err := Remove(tt.file); err != nil || got != tt.want {
			t.Errorf("Remove(%s) = %q, %v; want %q", filepath.Base(tt.file), got, err, tt.want)
		}
	}
	if b, err := os.ReadFile(kept); err != nil || string(b) != "# Project\n" {
		t.Errorf("CLAUDE.md = %q, %v; want %q", b, err, "# Project\n")
	}
	if _, err := os.Lstat(alone); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("AGENTS.md: %v, want it deleted", err)
	}
}
