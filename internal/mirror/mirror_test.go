package mirror

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/testenv"
)

// copyFiles is a Copier that copies a link as a link and a file with its permissions, without
// cp -c, so that the tests run everywhere.
type copyFiles struct{}

func (copyFiles) Copy(_ context.Context, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode().Perm())
}

// newEnv is a testenv repository whose committed .gitignore also ignores ignore, a line each.
func newEnv(t *testing.T, ignore string) (*testenv.Env, git.Client) {
	t.Helper()
	e := testenv.New(t)
	if ignore != "" {
		b, err := os.ReadFile(e.Path(".gitignore"))
		if err != nil {
			t.Fatal(err)
		}
		e.WriteFile(".gitignore", string(b)+ignore)
		e.Git("add", ".gitignore")
		e.Git("commit", "-q", "-m", "Ignore the test's files")
	}
	return e, git.Client{Runner: git.Exec{}, Env: e.Environ()}
}

// slotOf adds a worktree of e's repository at its HEAD under .claude/worktrees, as a slot is
// there, and returns its path.
func slotOf(t *testing.T, e *testenv.Env) string {
	t.Helper()
	path := e.Path(".claude/worktrees/slot-1")
	e.Git("worktree", "add", "-q", "--detach", path, "HEAD")
	return path
}

// Files names what Claude Code 2.1.305 copied into a worktree it made, observed on 2026-10-24 with
// one wholly ignored directory per pattern (A44): out of such a directory only the files of one a
// pattern reaches, the directory matching it, an anchored pattern naming it, or the first name
// after **/ being in its path, and once one is reached every file in it a pattern names. A
// pattern with no slash went by the **/ rule. Outside such directories every ignored file a
// pattern names counts; a tracked one or an untracked one git does not ignore never does.
func TestFilesFollowsClaudeCodesRule(t *testing.T) {
	e, g := newEnv(t, "/v1/\n/v2/\n/v3/\n/aux/\n/tools/\n/cache/\n/gen/\n/out/\nobj/\n.env\n"+
		"notes.local.md\n")
	for _, f := range []string{"v1/config.json", "v2/.env", "v3/a/notes.local.md",
		"aux/skills/a/SKILL.md", "tools/s/x.cfg", "tools/y.cfg", "tools/config.json",
		"cache/a.txt", "gen/z.txt", "out/build/k.txt", ".env", "deep/notes.local.md",
		"Library/config.json", "loose/config.json", "config.json", "deep2/obj/a.txt"} {
		e.WriteFile(f, f+"\n")
	}
	e.Git("add", "config.json")
	e.Git("commit", "-q", "-m", "A tracked file a pattern names")
	e.WriteFile(File, "**/config.json\n.env\n**/skills/**\ntools/**/x.cfg\ncache/\n**/gen\n"+
		"**/out/build/k.txt\nnotes.local.md\nobj\n")
	got, err := Files(context.Background(), g, e.Path(File), e.Repo)
	// .claude/skills/x is testenv's skill link: .claude/skills/ is ignored as a whole, and skills,
	// the first name after **/ in **/skills/**, is in its path. deep2/obj/, ignored as a whole
	// below a directory that is not, matches the slashless obj, as it would **/obj.
	want := []string{".claude/skills/x", ".env", "cache/a.txt", "deep/notes.local.md",
		"deep2/obj/a.txt", "gen/z.txt", "out/build/k.txt", "tools/config.json", "tools/s/x.cfg"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %q, %v; want %q", got, err, want)
	}
}

// A project with no .worktreeinclude names no file.
func TestFilesWithoutAWorktreeInclude(t *testing.T) {
	e, g := newEnv(t, "")
	if got, err := Files(context.Background(), g, e.Path(File), e.Repo); err != nil || got != nil {
		t.Errorf("Files = %q, %v; want none", got, err)
	}
}

// mirrorInto runs Mirror from e's main checkout into dst and fails the test unless it copied and
// deleted what want says.
func mirrorInto(t *testing.T, e *testenv.Env, g git.Client, dst string, want Result) {
	t.Helper()
	got, err := Mirror(context.Background(), g, copyFiles{}, e.Repo, dst)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Mirror = %+v, %v; want %+v", got, err, want)
	}
}

// checkFile fails the test unless file holds want, or, with want empty, does not exist.
func checkFile(t *testing.T, file, want string) {
	t.Helper()
	b, err := os.ReadFile(file)
	switch {
	case want == "" && !errors.Is(err, fs.ErrNotExist):
		t.Errorf("%s = %q, %v; want no such file", file, b, err)
	case want != "" && (err != nil || string(b) != want):
		t.Errorf("%s = %q, %v; want %q", file, b, err, want)
	}
}

// Mirror copies what the slot lacks or has otherwise, its bytes or its permissions, then nothing
// while nothing changes, and deletes what the main checkout no longer has.
func TestMirrorCopiesWhatDiffersAndDeletesWhatIsGone(t *testing.T) {
	e, g := newEnv(t, "notes.local.md\n")
	slot := slotOf(t, e)
	e.WriteFile(File, "CLAUDE.md\nnotes.local.md\n")
	e.WriteFile("notes.local.md", "notes\n")
	none := []string{}
	mirrorInto(t, e, g, slot, Result{Copied: []string{"CLAUDE.md", "notes.local.md"},
		Deleted: none})
	checkFile(t, filepath.Join(slot, "notes.local.md"), "notes\n")
	mirrorInto(t, e, g, slot, Result{Copied: none, Deleted: none})

	e.WriteFile("notes.local.md", "newer\n")
	mirrorInto(t, e, g, slot, Result{Copied: []string{"notes.local.md"}, Deleted: none})
	checkFile(t, filepath.Join(slot, "notes.local.md"), "newer\n")
	if err := os.Chmod(e.Path("notes.local.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	mirrorInto(t, e, g, slot, Result{Copied: []string{"notes.local.md"}, Deleted: none})

	if err := os.Remove(e.Path("notes.local.md")); err != nil {
		t.Fatal(err)
	}
	mirrorInto(t, e, g, slot, Result{Copied: none, Deleted: []string{"notes.local.md"}})
	checkFile(t, filepath.Join(slot, "notes.local.md"), "")
	checkFile(t, filepath.Join(slot, "CLAUDE.md"), "# Fake project\n\nRead AGENTS.md.\n")
}

// Mirror never copies through a link in the slot, so it writes nothing outside the slot, and
// never over a directory there.
func TestMirrorNeverWritesThroughALinkOrOverADirectory(t *testing.T) {
	e, g := newEnv(t, "/notes/\nnotes.local.md\n")
	slot := slotOf(t, e)
	e.WriteFile(File, "notes/\nnotes.local.md\n")
	e.WriteFile("notes/today.md", "today\n")
	e.WriteFile("notes.local.md", "notes\n")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(slot, "notes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(slot, "notes.local.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	mirrorInto(t, e, g, slot, Result{Copied: []string{}, Deleted: []string{}})
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Errorf("the link's target holds %v, %v; want nothing written through the link",
			entries, err)
	}
}

// A nested repository, such as a slot under .claude/worktrees, which init has git ignore, is never
// copied, though a pattern names the directory it is in: git lists it as a directory, and only
// files are mirrored.
func TestMirrorSkipsANestedRepository(t *testing.T) {
	e, g := newEnv(t, ".claude/worktrees/\n")
	slot := slotOf(t, e)
	e.WriteFile(File, ".claude/\n")
	got, err := Mirror(context.Background(), g, copyFiles{}, e.Repo, slot)
	want := []string{".claude/settings.local.json", ".claude/skills/x"}
	if err != nil || !reflect.DeepEqual(got.Copied, want) {
		t.Errorf("Mirror = %+v, %v; want %q copied, and no slot", got, err, want)
	}
	if _, err := os.Lstat(filepath.Join(slot, ".claude", "worktrees")); err == nil {
		t.Error("the slot has a .claude/worktrees, want no slot copied into it")
	}
}

// A file the slot has where the main checkout now has a file in place of a directory above it is
// one the main checkout no longer has, and goes, as do the deletions after it.
func TestMirrorDeletesWhatIsBelowAFileInTheMainCheckout(t *testing.T) {
	e, g := newEnv(t, "/notes\n/zz.local\n")
	slot := slotOf(t, e)
	e.WriteFile(File, "notes\nzz.local\n")
	for _, f := range []string{"notes/today.md", "zz.local"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(slot, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(slot, f), []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.WriteFile("notes", "a file now\n")
	mirrorInto(t, e, g, slot, Result{Copied: []string{},
		Deleted: []string{"notes/today.md", "zz.local"}})
}

// A directory git ignores as a whole that no pattern reaches, as a slot's Library is, is never
// touched: nothing is copied into it or deleted from it.
func TestMirrorLeavesWhatNoPatternReaches(t *testing.T) {
	e, g := newEnv(t, "")
	slot := slotOf(t, e)
	e.WriteFile("Library/config.json", "main's\n")
	if err := os.MkdirAll(filepath.Join(slot, "Library"), 0o755); err != nil {
		t.Fatal(err)
	}
	slotLibrary := filepath.Join(slot, "Library", "other.json")
	if err := os.WriteFile(slotLibrary, []byte("the slot's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.WriteFile(File, "**/config.json\n**/other.json\n")
	mirrorInto(t, e, g, slot, Result{Copied: []string{}, Deleted: []string{}})
	checkFile(t, slotLibrary, "the slot's\n")
	checkFile(t, filepath.Join(slot, "Library", "config.json"), "")
}
