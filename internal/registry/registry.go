// Package registry is ~/.upool/registry.json: every pooled repository on the machine, by the
// canonical path of its main checkout, and the <repo-key> naming its ~/.upool/pools/<repo-key>/
// directory (docs/contracts.md (e) State schemas, `~/.upool/registry.json`).
//
// The create hook routes on this file, not on .upool.toml, because from the desktop app's
// disclaimed helper it cannot read files under ~/Documents (docs/design.md, "One binary, a dozen
// commands, one JSON file per pool", the `init` row). The file is the source of truth: lookups go
// by path, and the key's derivation matters only so that `init` finds an existing pool again after
// the registry is lost (docs/contracts.md (e); A7).
//
// The home directory is always a parameter; nothing here reads the environment (CONTRIBUTING.md,
// "Process state comes in through run").
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/statefile"
)

// SchemaVersion is the registry.json format this package reads and writes: a document with a
// higher schema_version is refused, and one with a lower version is written at this one (A7).
const SchemaVersion = 1

const (
	// file is registry.json, relative to ~/.upool as internal/statefile takes it.
	file = "registry.json"
	// shown is how messages name the file.
	shown = "~/.upool/registry.json"
)

// Registry is the registry.json document, with exactly the keys in docs/contracts.md (e).
type Registry struct {
	SchemaVersion int    `json:"schema_version"`
	Repos         []Repo `json:"repos"`
}

// Repo is one pooled repository.
type Repo struct {
	// Path is the main checkout's canonical path, from paths.Key, so that Lookup compares keys
	// without touching the filesystem for every entry.
	Path string `json:"path"`
	// Key is the <repo-key> naming ~/.upool/pools/<repo-key>/, Key(Path).
	Key string `json:"key"`
}

// Key returns the <repo-key> for canonicalPath, which must already be a key from paths.Key: its
// last element, a "-", and the first 8 hex digits of the SHA-256 of the whole path, such as
// down-3f9a1c2e (docs/contracts.md (e); A7). The hash keeps two checkouts with the same name apart.
func Key(canonicalPath string) string {
	sum := sha256.Sum256([]byte(canonicalPath))
	return filepath.Base(canonicalPath) + "-" + hex.EncodeToString(sum[:4])
}

// Lookup returns the registered repository whose path is path's key, and whether there is one. A
// missing registry has no repositories. The match is exact, so a directory inside a registered
// checkout is not found; Containing is the create hook's wider match (A13).
func Lookup(home, path string) (Repo, bool, error) {
	canonical, err := paths.Key(path)
	if err != nil {
		return Repo{}, false, err
	}
	reg, err := load(home)
	if err != nil {
		return Repo{}, false, err
	}
	repo, ok := find(reg, canonical)
	return repo, ok, nil
}

// worktreesDir is where a repository's slots and agent worktrees live, relative to its main
// checkout: Claude Code's managed worktree directory.
const worktreesDir = ".claude/worktrees"

// Containing returns the registered repository whose main checkout is path or contains it, and
// whether there is one: the create hook's match for a request's cwd (A13). Where registered
// repositories nest, the innermost wins. A path inside a repository's .claude/worktrees is a slot
// or an agent worktree, not that main checkout, so it is not counted as inside that repository.
// The match compares internal/paths keys, so it needs no git call, which matters in the desktop
// app's restricted hook helper. A missing registry has no repositories.
func Containing(home, path string) (Repo, bool, error) {
	canonical, err := paths.Key(path)
	if err != nil {
		return Repo{}, false, err
	}
	reg, err := load(home)
	if err != nil {
		return Repo{}, false, err
	}
	var found Repo
	ok := false
	for _, repo := range reg.Repos {
		worktrees := filepath.Join(repo.Path, filepath.FromSlash(worktreesDir))
		if !paths.Within(canonical, repo.Path) || paths.Within(canonical, worktrees) {
			continue
		}
		if !ok || len(repo.Path) > len(found.Path) {
			found, ok = repo, true
		}
	}
	return found, ok, nil
}

// List returns every registered repository, in the order they were added. A missing registry has
// none. It takes no lock, as statefile.Read does not: a caller that also reads a pool's state.json
// and needs the two to agree, as `upool which` does, holds statefile.Lock around both reads.
func List(home string) ([]Repo, error) {
	reg, err := load(home)
	if err != nil {
		return nil, err
	}
	return reg.Repos, nil
}

// load reads and decodes registry.json under home, without the lock.
func load(home string) (Registry, error) {
	data, err := statefile.Read(home, file)
	if err != nil {
		return Registry{}, err
	}
	return decode(data)
}

// Add registers path's key under the lock and returns its entry. Adding a path that is already
// registered changes nothing and returns the entry it has. A key that another path already has is
// refused, since the two would share one pool directory. Waiting for the lock gives up when ctx
// ends, as statefile.Update does.
func Add(ctx context.Context, home, path string) (Repo, error) {
	canonical, err := paths.Key(path)
	if err != nil {
		return Repo{}, err
	}
	var added Repo
	err = statefile.Update(ctx, home, file, func(old []byte) ([]byte, error) {
		reg, err := decode(old)
		if err != nil {
			return nil, err
		}
		if repo, ok := find(reg, canonical); ok {
			added = repo
			return encode(reg)
		}
		repo := Repo{Path: canonical, Key: Key(canonical)}
		for _, r := range reg.Repos {
			if r.Key == repo.Key {
				return nil, fmt.Errorf("repo key %s for %s is already %s's in %s",
					repo.Key, canonical, r.Path, shown)
			}
		}
		reg.Repos = append(reg.Repos, repo)
		added = repo
		return encode(reg)
	})
	if err != nil {
		return Repo{}, err
	}
	return added, nil
}

// find returns the entry of reg whose path is the key canonical.
func find(reg Registry, canonical string) (Repo, bool) {
	for _, r := range reg.Repos {
		if r.Path == canonical {
			return r, true
		}
	}
	return Repo{}, false
}

// decode parses a registry.json document. Empty data, a missing file, is an empty registry.
//
// The version is read first, leniently, so that a newer document is refused with a message to
// upgrade rather than an error about the keys it added (A7). The document is then decoded with
// unknown keys as errors, so a rewrite never silently drops a field this upool does not know.
func decode(data []byte) (Registry, error) {
	if len(data) == 0 {
		return Registry{}, nil
	}
	var version struct {
		SchemaVersion int `json:"schema_version"`
	}
	// Unmarshal also rejects anything after the document, which the Decoder below would not.
	if err := json.Unmarshal(data, &version); err != nil {
		return Registry{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	if version.SchemaVersion > SchemaVersion {
		return Registry{}, fmt.Errorf("%s has schema_version %d, newer than the %d this upool "+
			"reads: upgrade upool", shown, version.SchemaVersion, SchemaVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var reg Registry
	if err := dec.Decode(&reg); err != nil {
		return Registry{}, fmt.Errorf("decoding %s: %w", shown, err)
	}
	return reg, nil
}

// encode writes reg at SchemaVersion, indented, with a final newline.
func encode(reg Registry) ([]byte, error) {
	reg.SchemaVersion = SchemaVersion
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding %s: %w", shown, err)
	}
	return append(data, '\n'), nil
}
