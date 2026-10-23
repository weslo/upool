//go:build darwin

package main

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/registry"
)

// The acceptance test of #191: init, then deinit --yes, leaves the repository's files byte for
// byte as they were before init, apart from the .gitignore line init added, with the registry
// entry and the pool's directory gone and doctor reporting the repository not registered. init
// clones with `cp -cR`, so this runs on macOS only.
func TestDeinitUndoesInit(t *testing.T) {
	e := newInitEnv(t)
	before := repoFiles(t, e.Repo)
	mustInit(t, e)
	gitignore := readFile(t, e, ".gitignore")
	if !strings.Contains(gitignore, worktreesLine) {
		t.Fatalf(".gitignore = %q after init, want %s in it", gitignore, worktreesLine)
	}
	key := repoKey(t, e)
	r := runIn(t, e, e.Repo, "deinit", "--yes")
	if r.code != output.ExitOK {
		t.Fatalf("deinit: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	after := repoFiles(t, e.Repo)
	if after[".gitignore"] != before[".gitignore"]+worktreesLine+"\n" {
		t.Errorf(".gitignore = %q, want what init left, %q", after[".gitignore"], gitignore)
	}
	delete(before, ".gitignore")
	delete(after, ".gitignore")
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if after[k] != before[k] {
			t.Errorf("%s = %q, want it as before init: %q", k, after[k], before[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(after)) {
		if _, ok := before[k]; !ok {
			t.Errorf("%s is left over: %q", k, after[k])
		}
	}
	for _, name := range skillFiles {
		dir := filepath.Dir(e.Path(name))
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %v, want the skill's directory gone", dir, err)
		}
	}
	if got := e.Git("worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Errorf("git worktree list = %q, want the main checkout alone", got)
	}
	if _, ok, err := registry.Lookup(e.Home, e.Repo); err != nil || ok {
		t.Errorf("registry lookup = %v, %v; want no entry", ok, err)
	}
	if _, err := os.Lstat(filepath.Join(e.Home, ".upool", "pools", key)); !errors.Is(err,
		fs.ErrNotExist) {
		t.Errorf("~/.upool/pools/%s is still there: %v", key, err)
	}
	d := runIn(t, e, e.Repo, "doctor")
	if d.code != output.ExitFailure ||
		!strings.Contains(d.stderr, "is not inside a registered pool") {
		t.Errorf("doctor: exit %d, stderr %q; want %d and the repository not registered", d.code,
			d.stderr, output.ExitFailure)
	}
	// A second deinit finds nothing of upool's left.
	if r := runIn(t, e, e.Repo, "deinit", "--yes"); r.code != output.ExitOK ||
		r.stdout != e.Repo+" has nothing of upool's to remove\n" {
		t.Errorf("second deinit: exit %d, stdout %q; want nothing to remove", r.code, r.stdout)
	}
}
