// Package paths gives every path upool compares one canonical form, its key: a lease's worktree
// path, stdin cwd against the registry, the working directory in `which` (docs/design.md,
// "Repository, CI and Homebrew distribution", the first of the five rules after the layout).
//
// A plain string comparison would leak leases, for three reasons. macOS spells one directory as
// /var and /private/var, or /tmp and /private/tmp, and the Unity CLI reports the second
// (testdata/captures/unity/test-not-a-unity-project.json). APFS is case-insensitive by default. And
// the desktop app stores lowercased paths (${repo_lc} in testdata/README.md). So a key has its
// symlinks resolved and is folded to lower case when its volume ignores case.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Key returns p's key: absolute, symlinks resolved, cleaned, and lowercased when CaseSensitive says
// the volume holding it is case-insensitive. p need not exist: the symlinks in its longest existing
// prefix are resolved, and the rest is joined on unchanged, so a slot not yet cloned has the key it
// will have once it is. A relative p is made absolute against the working directory.
func Key(p string) (string, error) {
	return KeyWith(p, CaseSensitive)
}

// KeyWith is Key with caseSensitive in place of CaseSensitive, so a test can stand in a volume of
// either kind on any operating system. caseSensitive is asked about the deepest existing directory
// or file in p, after symlinks.
func KeyWith(p string, caseSensitive func(path string) (bool, error)) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("making %s absolute: %w", p, err)
		}
		// Not filepath.Join, which would clean away a ".." before resolve sees the symlink it follows.
		p = wd + string(filepath.Separator) + p
	}
	existing, rest, err := resolve(p)
	if err != nil {
		return "", err
	}
	sensitive, err := caseSensitive(existing)
	if err != nil {
		return "", fmt.Errorf("checking case sensitivity of %s: %w", existing, err)
	}
	key := filepath.Join(append([]string{existing}, rest...)...)
	if !sensitive {
		key = strings.ToLower(key)
	}
	return key, nil
}

// resolve splits the absolute path p into its longest existing prefix, with symlinks resolved, and
// the elements after it. The prefix goes to filepath.EvalSymlinks uncleaned, so a ".." after a
// symlink leaves the symlink's target, not the directory holding it; a ".." in rest follows a
// directory that does not exist, so cleaning it lexically is safe.
func resolve(p string) (existing string, rest []string, err error) {
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			return resolved, rest, nil
		}
		// ENOTDIR: an element of p is a file, so nothing below it can exist either.
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", nil, fmt.Errorf("resolving symlinks in %s: %w", p, err)
		}
		dir, elem := filepath.Split(trimSeparators(p))
		if elem == "" {
			// p is the root, which always exists where upool runs; this only ends the loop.
			return "", nil, fmt.Errorf("resolving symlinks in %s: %w", p, err)
		}
		rest = append([]string{elem}, rest...)
		p = trimSeparators(dir)
	}
}

// trimSeparators drops the separators that end p, unless p is the root.
func trimSeparators(p string) string {
	if trimmed := strings.TrimRight(p, string(filepath.Separator)); trimmed != "" {
		return trimmed
	}
	return p[:1]
}

// Within reports whether the key child is the key parent or lies under it. Both must be keys, from
// Key; Within compares them as strings, element by element, so slot-1 does not contain slot-10.
func Within(child, parent string) bool {
	if !strings.HasPrefix(child, parent) {
		return false
	}
	if len(child) == len(parent) || strings.HasSuffix(parent, string(filepath.Separator)) {
		return true
	}
	return child[len(parent)] == filepath.Separator
}
