package pool

import (
	"context"
	"slices"
	"strings"

	"github.com/weslo/upool/internal/git"
)

// SettingsAsset is the one file define-order noise can be in, relative to a slot.
const SettingsAsset = "ProjectSettings/ProjectSettings.asset"

// definesKey is the line, after its indentation, that opens the scriptingDefineSymbols mapping:
// one entry per platform below it, each `<platform>: <symbols separated by ;>`.
const definesKey = "scriptingDefineSymbols:"

// Noise reports whether the worktree at dir holds nothing but define-order noise: the one kind of
// tracked churn that release reverts rather than parking the slot (docs/design.md, "Slot
// lifecycle: seed only from a Library current at HEAD, refresh on handout, reset without `-x`",
// two kinds of churn; spike §8 D.3; spike §9 G). It does, and the slot is then clean once it is
// reverted (docs/contracts.md (f), the definition of clean), only when SettingsAsset is the only
// path `git status` reports, modified in the worktree and not in the index; every line that
// changed is an entry of its scriptingDefineSymbols mapping; and each platform's symbols are the
// same, in another order (testdata/captures/git/projectsettings-define-reorder.diff). Anything
// else, in that file or another, is not noise.
func Noise(ctx context.Context, g git.Client, dir string) (bool, error) {
	status, err := g.Status(ctx, dir)
	if err != nil {
		return false, err
	}
	if len(status) != 1 || status[0].XY != " M" || status[0].Path != SettingsAsset {
		return false, nil
	}
	index, worktree, ok, err := g.DiffSides(ctx, dir, SettingsAsset)
	if err != nil || !ok {
		return false, err
	}
	return definesReordered(string(index), string(worktree)), nil
}

// RevertNoise reverts define-order noise in the worktree at dir by checking SettingsAsset out, and
// reports whether it did. When Noise is false it changes nothing.
func RevertNoise(ctx context.Context, g git.Client, dir string) (bool, error) {
	noise, err := Noise(ctx, g, dir)
	if err != nil || !noise {
		return false, err
	}
	if err := g.CheckoutPath(ctx, dir, SettingsAsset); err != nil {
		return false, err
	}
	return true, nil
}

// definesReordered reports whether before and after differ, and only in the order of the symbols
// in their scriptingDefineSymbols entries: every other line is the same, line ending included, and
// each platform has the same symbols, as many times each. Either file without exactly one
// scriptingDefineSymbols mapping, or with a platform named twice, is not.
func definesReordered(before, after string) bool {
	if before == after {
		return false
	}
	restBefore, definesBefore, ok := splitDefines(before)
	if !ok {
		return false
	}
	restAfter, definesAfter, ok := splitDefines(after)
	if !ok || !slices.Equal(restBefore, restAfter) || len(definesBefore) != len(definesAfter) {
		return false
	}
	for platform, symbols := range definesBefore {
		other, found := definesAfter[platform]
		if !found || !slices.Equal(symbols, other) {
			return false
		}
	}
	return true
}

// splitDefines splits doc into its lines outside the entries of its scriptingDefineSymbols
// mapping, each with its line ending, and the entries, as each platform's symbols, sorted. The
// entries are the lines after the definesKey line that are indented further than it; the first
// line that is not ends the mapping. A platform is named with its indentation, so an entry that
// moved is not the same entry. It reports false when doc has no definesKey line or more than one,
// an entry that is not `<platform>:` and its symbols, or a platform named twice.
func splitDefines(doc string) (rest []string, defines map[string][]string, ok bool) {
	const (
		beforeKey = iota
		inMapping
		afterMapping
	)
	state, keyIndent := beforeKey, 0
	for _, line := range strings.SplitAfter(doc, "\n") {
		text := strings.TrimRight(line, "\r\n")
		body := strings.TrimLeft(text, " ")
		indent := len(text) - len(body)
		if state == inMapping {
			if indent > keyIndent && body != "" {
				platform, symbols, found := strings.Cut(text, ":")
				if _, named := defines[platform]; !found || named {
					return nil, nil, false
				}
				defines[platform] = sortedSymbols(symbols)
				continue
			}
			state = afterMapping
		}
		if body == definesKey {
			if state != beforeKey {
				return nil, nil, false
			}
			state, keyIndent, defines = inMapping, indent, map[string][]string{}
		}
		rest = append(rest, line)
	}
	return rest, defines, state != beforeKey
}

// sortedSymbols returns a platform's symbols, the text after `<platform>:` and the one space that
// follows it, split on ";" and sorted, so two orders of the same symbols compare equal and any
// other change, spacing included, does not.
func sortedSymbols(value string) []string {
	symbols := strings.Split(strings.TrimPrefix(value, " "), ";")
	slices.Sort(symbols)
	return symbols
}
