// Package clone is the whole-checkout clone recipe of docs/contracts.md (g): choosing and checking
// the seed, copying it into a slot, stripping the Editor's instance files and moving the slot to
// base (docs/design.md, "Repository, CI and Homebrew distribution", the internal/clone row of the
// layout).
//
// It holds steps 0 to 8: Seeder, which chooses the seed (step 0, A19); Recipe.Clone, which copies
// the whole checkout rather than checking out and cloning only the Library, so a slot costs about
// no disk and carries the gitignored agent files (docs/design.md, "Why clone the whole checkout
// rather than check out and clone the Library"); and step 1's staleness check. Still to come are
// refreshing a stale seed (M4) and comparing the cache key (#63).
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
	"path"
	"path/filepath"
	"regexp"
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
	// buildCacheDir is where Unity's build program keeps what it builds, a directory per DAG
	// (spike §3).
	buildCacheDir = "Library/Bee/artifacts"
)

// editorDAG matches the name of the Editor's DAG directory under buildCacheDir: a lowercase hex
// hash, E, then the configuration, as in 200b0aEDbg.dag. A player build's DAG has P where the
// Editor's has E (200b0aP.dag, 200b0aPDevDbg.dag) and compiles the same scripts into player
// assemblies, which say nothing of the Editor's (#144).
var editorDAG = regexp.MustCompile(`^[0-9a-f]+E[A-Za-z]*\.dag$`)

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
	// CompiledTime is the newest mtime of an assembly in the build cache's Editor DAG, and
	// CompiledIn that DAG's directory, slash-separated and relative to the seed; zero and "" when
	// there is none. The build writes an assembly there on every compile, but the Editor copies it
	// into ScriptAssemblies only when its content changed, so after a compile that changed nothing,
	// such as one of scripts a checkout gave new mtimes, only CompiledTime moves (#144).
	CompiledTime time.Time
	CompiledIn   string
	// Head is the seed's `git rev-parse HEAD`: the commit its Library matches, when it is not
	// stale.
	Head string
	// Dirty is true when the seed's `git status --porcelain` prints anything.
	Dirty bool
}

// Stale reports whether the seed's Library is behind its scripts: it has no ScriptAssemblies, or
// a script is newer than the Editor's last compile, which is how the spike found its stale main
// checkout (spike §1). Whether the seed is dirty does not matter: a clone reverts what a dirty
// seed left in the slot ((g) step 7).
func (s Staleness) Stale() bool {
	return s.AssembliesMissing || s.ScriptTime.After(s.Compiled())
}

// Compiled is when the Editor last compiled the seed's scripts, as far as its Library shows: the
// newer of AssembliesTime and CompiledTime.
func (s Staleness) Compiled() time.Time {
	if s.CompiledTime.After(s.AssembliesTime) {
		return s.CompiledTime
	}
	return s.AssembliesTime
}

// Reason says why the Library is stale, for a message about the seed, and is "" when it is not.
func (s Staleness) Reason() string {
	switch {
	case s.AssembliesMissing:
		return "it has no " + assembliesDir
	case s.Stale():
		against := assembliesDir
		if s.CompiledTime.After(s.AssembliesTime) {
			against = s.CompiledIn
		}
		return fmt.Sprintf("%s (modified %s) is newer than %s (modified %s)", s.NewestScript,
			s.ScriptTime.UTC().Format(time.RFC3339), against,
			s.Compiled().UTC().Format(time.RFC3339))
	}
	return ""
}

// CheckStaleness runs docs/contracts.md (g) step 1 on seed, an absolute path to a checkout: the
// newest mtime under `Assets/**/*.cs` against the Editor's last compile, the newer of
// Library/ScriptAssemblies and the build cache's Editor DAG (#144), then
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
	if s.CompiledTime, s.CompiledIn, err = compiledTime(seed); err != nil {
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

// compiledTime returns the newest mtime of a .dll in the build cache's Editor DAG under seed, and
// that DAG's directory, slash-separated and relative to seed; zero and "" when the seed has no
// build cache or no Editor DAG in it. Only files count: a clone gives every directory a new
// mtime, while its files keep theirs.
func compiledTime(seed string) (time.Time, string, error) {
	cache := filepath.Join(seed, filepath.FromSlash(buildCacheDir))
	dags, err := os.ReadDir(cache)
	if errors.Is(err, fs.ErrNotExist) {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", fmt.Errorf("reading %s: %w", cache, err)
	}
	var newest time.Time
	var in string
	for _, dag := range dags {
		if !dag.IsDir() || !editorDAG.MatchString(dag.Name()) {
			continue
		}
		dir := filepath.Join(cache, dag.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".dll") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return time.Time{}, "", fmt.Errorf("checking %s: %w", filepath.Join(dir, e.Name()),
					err)
			}
			if info.ModTime().After(newest) {
				newest, in = info.ModTime(), path.Join(buildCacheDir, dag.Name())
			}
		}
	}
	return newest, in, nil
}
