package clone

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The tests of DiskUse, on APFS clones made with cp -c in a temporary directory, as the probe
// behind A32 made them (docs/evidence/apfs-private-size/probe.txt).

// writeBytes writes n bytes of b to path, flushed to disk.
func writeBytes(t *testing.T, path string, b byte, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{b}, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cloneFile copies src to dst with cp -c, sharing every block.
func cloneFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (CP{}).Copy(t.Context(), src, dst); err != nil {
		t.Fatal(err)
	}
}

// rewrite overwrites the first n bytes of the file at path, which unshares the blocks they are in.
func rewrite(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{'z'}, n), 0); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
}

// A clone's private size is 0 and its apparent size its length; a clone with 64 KB rewritten has
// 64 KB private; a file of its own is private in full, in whole 4 KB blocks. A symbolic link is
// not followed, so the file it points at is not counted twice.
func TestDiskUseCountsPrivateBlocks(t *testing.T) {
	const mb = 1 << 20
	src := t.TempDir()
	writeBytes(t, filepath.Join(src, "a.bin"), 'a', mb)
	writeBytes(t, filepath.Join(src, "b.bin"), 'b', mb)
	slot := t.TempDir()
	cloneFile(t, filepath.Join(src, "a.bin"), filepath.Join(slot, "Library", "a.bin"))
	cloneFile(t, filepath.Join(src, "b.bin"), filepath.Join(slot, "Library", "b.bin"))
	rewrite(t, filepath.Join(slot, "Library", "b.bin"), 64<<10)
	writeBytes(t, filepath.Join(slot, "own.txt"), 'o', 10)
	if err := os.Symlink(filepath.Join(src, "a.bin"), filepath.Join(slot, "link")); err != nil {
		t.Fatal(err)
	}
	d, err := DiskUse(slot)
	if err != nil {
		t.Fatal(err)
	}
	want := Disk{Private: 64<<10 + 4<<10, Apparent: 2*mb + 10}
	if d != want {
		t.Errorf("DiskUse = %+v, want %+v", d, want)
	}
}

// A directory that does not exist is an error; an empty one uses nothing.
func TestDiskUseOfAnEmptyOrMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	if d, err := DiskUse(dir); err != nil || d != (Disk{}) {
		t.Errorf("DiskUse(empty) = %+v, %v, want nothing", d, err)
	}
	if _, err := DiskUse(filepath.Join(dir, "gone")); err == nil {
		t.Error("DiskUse(missing) succeeded, want an error")
	}
}
