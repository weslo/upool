// Package clone is the whole-checkout clone recipe of docs/contracts.md (g): choosing and checking
// the seed, copying it into a slot, stripping the Editor's instance files and moving the slot to
// base (docs/design.md, "Repository, CI and Homebrew distribution", the internal/clone row of the
// layout).
//
// It holds steps 1 to 8: Recipe.Clone, which copies the whole checkout rather than checking out
// and cloning only the Library, so a slot costs about no disk and carries the gitignored agent
// files (docs/design.md, "Why clone the whole checkout rather than check out and clone the
// Library"), and step 1's staleness check. Still to come are choosing a seed other than init's
// main checkout (step 0, A19), refreshing a stale seed (M4) and comparing the cache key (#63).
//
// A clone inherits whatever its seed's Library holds, so a seed whose Library is behind its
// scripts hands every clone a recompile: the spike's first three clones each reimported 10 assets
// instead of 1 for that reason (docs/design.md, "Slot lifecycle: seed only from a Library current
// at HEAD, refresh on handout, reset without `-x`"; spike §1).
package clone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/weslo/upool/internal/git"
)

// The paths the staleness check reads, relative to the seed (docs/contracts.md (g) step 1).
const (
	// scriptsDir is searched for scripts, `Assets/**/*.cs`.
	scriptsDir = "Assets"
	// assembliesDir is where the Editor writes the assemblies it compiles those scripts into.
	assembliesDir = "Library/ScriptAssemblies"
)

// scriptExt is the extension of the scripts the check compares, as `Assets/**/*.cs` spells it.
const scriptExt = ".cs"

// Staleness is what docs/contracts.md (g) step 1 reads from a seed: its newest script against its
// compiled assemblies, and its HEAD and whether it has uncommitted changes, which a clone records
// as the slot's seed_head and seed_dirty ((e) State schemas).
type Staleness struct {
	// NewestScript is the `Assets/**/*.cs` file with the newest mtime, slash-separated and
	// relative to the seed, and ScriptTime that mtime. NewestScript is "" when there is none.
	NewestScript string
	ScriptTime   time.Time
	// AssembliesMissing is true when the seed has no Library/ScriptAssemblies: its Library has
	// never finished compiling.
	AssembliesMissing bool
	// AssembliesTime is the newest mtime of Library/ScriptAssemblies and the entries directly in
	// it: when the Editor last wrote an assembly there, whether it replaced the file or rewrote it.
	// It is zero when AssembliesMissing.
	AssembliesTime time.Time
	// Head is the seed's `git rev-parse HEAD`: the commit its Library matches, when it is not
	// stale.
	Head string
	// Dirty is true when the seed's `git status --porcelain` prints anything.
	Dirty bool
}

// Stale reports whether the seed's Library is behind its scripts: it has no ScriptAssemblies, or
// a script is newer than the newest of them, which is how the spike found its stale main checkout
// (spike §1). Whether the seed is dirty does not matter: a clone reverts what a dirty seed left in
// the slot ((g) step 7).
func (s Staleness) Stale() bool {
	return s.AssembliesMissing || s.ScriptTime.After(s.AssembliesTime)
}

// Reason says why the Library is stale, for a message about the seed, and is "" when it is not.
func (s Staleness) Reason() string {
	switch {
	case s.AssembliesMissing:
		return "it has no " + assembliesDir
	case s.Stale():
		return fmt.Sprintf("%s (modified %s) is newer than %s (modified %s)", s.NewestScript,
			s.ScriptTime.UTC().Format(time.RFC3339), assembliesDir,
			s.AssembliesTime.UTC().Format(time.RFC3339))
	}
	return ""
}

// CheckStaleness runs docs/contracts.md (g) step 1 on seed, an absolute path to a checkout: the
// newest mtime under `Assets/**/*.cs` against Library/ScriptAssemblies, then
// `git -C <seed> rev-parse HEAD` and `git -C <seed> status --porcelain` through g. It changes
// nothing; refreshing a stale seed arrives in M4.
func CheckStaleness(ctx context.Context, g git.Client, seed string) (Staleness, error) {
	var s Staleness
	var err error
	if s.NewestScript, s.ScriptTime, err = newestScript(seed); err != nil {
		return Staleness{}, err
	}
	if s.AssembliesTime, s.AssembliesMissing, err = assembliesTime(seed); err != nil {
		return Staleness{}, err
	}
	if s.Head, err = g.Head(ctx, seed); err != nil {
		return Staleness{}, err
	}
	status, err := g.Status(ctx, seed)
	if err != nil {
		return Staleness{}, err
	}
	s.Dirty = len(status) > 0
	return s, nil
}

// newestScript returns the `Assets/**/*.cs` file under seed with the newest mtime, slash-separated
// and relative to seed, and that mtime; "" and the zero time when there is none, or no Assets.
// Symbolic links are neither followed nor counted.
func newestScript(seed string) (string, time.Time, error) {
	root := filepath.Join(seed, scriptsDir)
	var newest string
	var newestTime time.Time
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), scriptExt) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if newest == "" || info.ModTime().After(newestTime) {
			rel, err := filepath.Rel(seed, path)
			if err != nil {
				return err
			}
			newest, newestTime = filepath.ToSlash(rel), info.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("finding the newest script under %s: %w", root, err)
	}
	return newest, newestTime, nil
}

// assembliesTime returns the newest mtime of seed's Library/ScriptAssemblies and the entries
// directly in it, or true when it does not exist. Both count because the directory's mtime moves
// only when an entry is added, removed or renamed, and a file's only when it is written.
func assembliesTime(seed string) (time.Time, bool, error) {
	dir := filepath.Join(seed, filepath.FromSlash(assembliesDir))
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, true, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("checking %s: %w", dir, err)
	}
	if !info.IsDir() {
		return time.Time{}, false, fmt.Errorf("%s is not a directory", dir)
	}
	newest := info.ModTime()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return time.Time{}, false, fmt.Errorf("checking %s: %w", filepath.Join(dir, e.Name()),
				err)
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, false, nil
}
