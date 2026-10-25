// Package mirror keeps the gitignored files a project's .worktreeinclude names current in its
// slots: each free slot's copies are made the main checkout's before a handout, as Claude Code
// copies them into a worktree it makes with git and does not into one a WorktreeCreate hook makes
// (docs/contracts.md A44; #231; #233; [Claude Code worktrees], "Copy gitignored files into
// worktrees"). Git works out which files those are; only files git ignores are ever copied,
// overwritten or deleted, so a mirror never makes a slot dirty.
//
// [Claude Code worktrees]: https://code.claude.com/docs/en/worktrees
package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/git"
)

// File is Claude Code's list of the gitignored files to copy into a new worktree, at a
// checkout's root, in .gitignore syntax.
const File = ".worktreeinclude"

// Result is what Mirror did to one checkout, by slash-separated paths relative to it.
type Result struct {
	Copied  []string `json:"copied"`
	Deleted []string `json:"deleted"`
}

// Changed reports whether Mirror copied or deleted anything.
func (r Result) Changed() bool {
	return len(r.Copied) > 0 || len(r.Deleted) > 0
}

// Files returns the files in checkout that the patterns of the .worktreeinclude at patterns name
// and that git ignores there, by slash-separated paths relative to checkout, sorted; none when
// patterns does not exist. A file git tracks, or sees as untracked without ignoring it, is never
// one. They are what Claude Code copies, by its documented rule for a directory git ignores as a
// whole: a file in one is named only when some pattern reaches the directory (reaches).
func Files(ctx context.Context, g git.Client, patterns, checkout string) ([]string, error) {
	text, err := os.ReadFile(patterns)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", patterns, err)
	}
	listed, err := g.LsExcludedBy(ctx, checkout, patterns)
	if err != nil {
		return nil, err
	}
	// A directory listed is a nested repository, such as a slot, whose files are not checkout's.
	listed = slices.DeleteFunc(listed, func(f string) bool { return strings.HasSuffix(f, "/") })
	ignored, err := g.Ignored(ctx, checkout, listed)
	if err != nil {
		return nil, err
	}
	whole, err := g.IgnoredDirs(ctx, checkout)
	if err != nil {
		return nil, err
	}
	rules := parse(string(text))
	var files []string
	for _, f := range ignored {
		i := slices.IndexFunc(whole, func(d string) bool { return strings.HasPrefix(f, d) })
		if i < 0 || reaches(rules, whole[i]) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files, nil
}

// rule is one pattern of a .worktreeinclude, as reaches reads it: its first name, and whether it
// is anchored and so names the directories it goes through.
type rule struct {
	// names are an anchored pattern's names, from the root; first is the first name of any other
	// pattern, after a leading **/.
	names []string
	first string
}

// parse returns the rules of a .worktreeinclude's text: one for each line that is a pattern, but
// not a negated one, which names no file to copy. Blank lines and comments are none, and, as in
// .gitignore, a backslash makes a leading # or ! part of the name.
func parse(text string) []rule {
	var rules []rule
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		line = strings.TrimRight(line, " ")
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}
		body, rooted := strings.CutPrefix(strings.TrimSuffix(line, "/"), "/")
		switch {
		case strings.HasPrefix(body, "**/"):
			first, _, _ := strings.Cut(strings.TrimPrefix(body, "**/"), "/")
			rules = append(rules, rule{first: first})
		case !rooted && !strings.Contains(body, "/"):
			// A pattern with no slash but a last one matches at any depth, as **/ does, and Claude
			// Code reads it the same way (A44).
			rules = append(rules, rule{first: body})
		default:
			rules = append(rules, rule{names: strings.Split(body, "/")})
		}
	}
	return rules
}

// reaches reports whether one of rules reaches dir, a directory git ignores as a whole, ending in
// /, so that Claude Code copies the files in it that the .worktreeinclude names. Claude Code's
// worktrees documentation gives the rule for a pattern that starts with **/: it reaches dir when
// dir matches it, or when its first name after the **/ is one of the names in dir's path. A
// pattern with no slash but a last one goes by the same rule, and an anchored pattern reaches dir
// when its names, up to a **, match dir's from the root: Claude Code 2.1.305 copied the files a
// pattern of each kind named in an ignored directory only so (A44).
func reaches(rules []rule, dir string) bool {
	names := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	for _, r := range rules {
		if r.names == nil {
			if slices.ContainsFunc(names, func(n string) bool { return match(r.first, n) }) {
				return true
			}
			continue
		}
		if anchoredReaches(r.names, names) {
			return true
		}
	}
	return false
}

// anchoredReaches reports whether an anchored pattern whose names are pattern goes through, or
// names, the directory whose names are dir: each of its names up to a ** matches dir's in turn,
// until one of the two runs out.
func anchoredReaches(pattern, dir []string) bool {
	for i, n := range pattern {
		if n == "**" || i == len(dir) {
			return true
		}
		if !match(n, dir[i]) {
			return false
		}
	}
	return true
}

// match reports whether name matches glob, one name of a .gitignore pattern, as path.Match does
// with .gitignore's [!…] read as [^…]; a malformed glob matches nothing.
func match(glob, name string) bool {
	ok, err := path.Match(strings.ReplaceAll(glob, "[!", "[^"), name)
	return err == nil && ok
}

// Mirror makes the files in dst that the main checkout's .worktreeinclude names the main
// checkout's (#233). It copies each of main's Files that dst lacks or has otherwise, as an APFS
// clone through copier, after removing dst's copy, and deletes each of dst's that main no longer
// has. It copies only into a path git ignores in dst too, never into a directory or through a
// link, so a file dst tracks, or sees as untracked, is never overwritten; a slot's Library, which
// no pattern reaches, is never touched. Edits made in dst to a file main has are replaced.
func Mirror(ctx context.Context, g git.Client, copier clone.Copier, main,
	dst string) (Result, error) {
	patterns := filepath.Join(main, File)
	want, err := Files(ctx, g, patterns, main)
	if err != nil {
		return Result{}, err
	}
	have, err := Files(ctx, g, patterns, dst)
	if err != nil {
		return Result{}, err
	}
	r := Result{Copied: []string{}, Deleted: []string{}}
	wanted, had := set(want), set(have)
	var fresh []string
	for _, f := range want {
		if had[f] {
			continue
		}
		ok, err := copyable(dst, f)
		if err != nil {
			return r, err
		}
		if ok {
			fresh = append(fresh, f)
		}
	}
	// A path that is not among dst's own Files must be one git ignores there.
	fresh, err = g.Ignored(ctx, dst, fresh)
	if err != nil {
		return r, err
	}
	into := set(fresh)
	for _, f := range want {
		if !had[f] && !into[f] {
			continue
		}
		src, to := filepath.Join(main, filepath.FromSlash(f)), filepath.Join(dst,
			filepath.FromSlash(f))
		same, err := sameFile(src, to)
		if err != nil {
			return r, err
		}
		if same {
			continue
		}
		if err := copyFile(ctx, copier, src, to); err != nil {
			return r, err
		}
		r.Copied = append(r.Copied, f)
	}
	for _, f := range have {
		if wanted[f] {
			continue
		}
		// Only a file main no longer has goes: one main has, but tracks or does not ignore, stays.
		// ENOTDIR says a directory above it is a file now, so main has no such file either.
		if _, err := os.Lstat(filepath.Join(main, filepath.FromSlash(f))); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return r, fmt.Errorf("checking %s: %w", filepath.Join(main, f), err)
		}
		file := filepath.Join(dst, filepath.FromSlash(f))
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return r, fmt.Errorf("removing %s: %w", file, err)
		}
		r.Deleted = append(r.Deleted, f)
	}
	return r, nil
}

// set returns the strings of list as a set.
func set(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// copyable reports whether rel, relative to dir, is a path a file can be copied to: every
// directory above it that exists is a directory, not a link or a file, and rel itself is not a
// directory.
func copyable(dir, rel string) (bool, error) {
	names := strings.Split(rel, "/")
	cur := dir
	for i, n := range names {
		cur = filepath.Join(cur, n)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("checking %s: %w", cur, err)
		}
		last := i == len(names)-1
		if last && info.IsDir() || !last && !info.IsDir() {
			return false, nil
		}
	}
	return true, nil
}

// sameFile reports whether dst is already what src is: a link to the same target, or a regular
// file with the same permissions and bytes.
func sameFile(src, dst string) (bool, error) {
	si, err := os.Lstat(src)
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", src, err)
	}
	di, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", dst, err)
	}
	if si.Mode().Type() != di.Mode().Type() {
		return false, nil
	}
	if si.Mode()&fs.ModeSymlink != 0 {
		st, err := os.Readlink(src)
		if err != nil {
			return false, fmt.Errorf("reading the link %s: %w", src, err)
		}
		dt, err := os.Readlink(dst)
		if err != nil {
			return false, fmt.Errorf("reading the link %s: %w", dst, err)
		}
		return st == dt, nil
	}
	if !si.Mode().IsRegular() || si.Mode().Perm() != di.Mode().Perm() || si.Size() != di.Size() {
		return false, nil
	}
	return sameBytes(src, dst)
}

// sameBytes reports whether the files a and b hold the same bytes; they are the same size.
func sameBytes(a, b string) (_ bool, err error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, fa.Close()) }()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, fb.Close()) }()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		doneA := errors.Is(ea, io.EOF) || errors.Is(ea, io.ErrUnexpectedEOF)
		doneB := errors.Is(eb, io.EOF) || errors.Is(eb, io.ErrUnexpectedEOF)
		if ea != nil && !doneA {
			return false, ea
		}
		if eb != nil && !doneB {
			return false, eb
		}
		if doneA || doneB {
			return doneA && doneB, nil
		}
	}
}

// copyFile replaces dst with an APFS clone of src through copier, making the directories above it.
func copyFile(ctx context.Context, copier clone.Copier, src, dst string) error {
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", dst, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	return copier.Copy(ctx, src, dst)
}
