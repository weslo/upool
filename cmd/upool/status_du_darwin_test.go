package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// The test of `upool status --du` (#99), on APFS clones made with cp -c, as a pool's slots are:
// macOS only, since only APFS reports a private size (A32).

// newDuHome registers one pool, down, under a new HOME. Its main checkout has two 1 MB files in
// its Library. slot-1 clones both and then rewrites the first 64 KB of one, which unshares those
// blocks alone; slot-2 is an empty directory; slot-3's directory is gone.
func newDuHome(t *testing.T) (home, repo string) {
	t.Helper()
	home = tempDir(t)
	repo = filepath.Join(home, "Documents", "down")
	write := func(path string, b byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte{b}, 1<<20), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, "Library", "a.bin"), 'a')
	write(filepath.Join(repo, "Library", "b.bin"), 'b')
	slot := func(name string) string { return filepath.Join(repo, ".claude", "worktrees", name) }
	if err := os.MkdirAll(filepath.Join(slot("slot-1"), "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if err := (clone.CP{}).Copy(t.Context(), filepath.Join(repo, "Library", name),
			filepath.Join(slot("slot-1"), "Library", name)); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(filepath.Join(slot("slot-1"), "Library", "b.bin"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{'z'}, 64<<10), 0); err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(slot("slot-2"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Add(t.Context(), home, repo)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.Update(t.Context(), home, reg.Key, func(p *pool.Pool) error {
		p.Repo, p.Base = repo, "main"
		for _, s := range []struct {
			name  string
			state pool.State
		}{{"slot-1", pool.StateReady}, {"slot-2", pool.StateReady},
			{"slot-3", pool.StateParked}} {
			p.Slots = append(p.Slots, pool.Slot{Name: s.name, Path: slot(s.name), State: s.state,
				LastUsed: clock})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return home, repo
}

// The acceptance test of #99: status --du gives each slot its private size, the bytes no other
// clone shares, beside its apparent size, and the pool their sums: slot-1's two 1 MB clones cost
// the 64 KB rewritten in one of them, though they are 2 MB apparent; slot-2 costs nothing; slot-3,
// whose directory is gone, shows "-". --all sums each pool the same way.
func TestStatusDu(t *testing.T) {
	home, repo := newDuHome(t)
	r := runStatus(t, home, repo, "--du")
	checkSucceeded(t, r)
	checkGolden(t, "status-du.stdout", home, r.stdout)
	r = runStatus(t, home, repo, "--du", "--json")
	checkSucceeded(t, r)
	checkGolden(t, "status-du.json", home, r.stdout)

	r = runStatus(t, home, tempDir(t), "--all", "--du")
	checkSucceeded(t, r)
	checkGolden(t, "status-du.stdout", home, r.stdout)
}
