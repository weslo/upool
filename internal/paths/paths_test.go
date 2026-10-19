package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sensitive and insensitive stand in a volume of each kind, whatever this machine's is.
func sensitive(string) (bool, error)   { return true, nil }
func insensitive(string) (bool, error) { return false, nil }

// tempDir is t.TempDir with its symlinks resolved (on macOS it is under /var, which is
// /private/var).
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func key(t *testing.T, p string, caseSensitive func(string) (bool, error)) string {
	t.Helper()
	k, err := KeyWith(p, caseSensitive)
	if err != nil {
		t.Fatalf("KeyWith(%q): %v", p, err)
	}
	return k
}

func TestKeyFoldsCaseOnCaseInsensitiveVolume(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "Down.Game"))
	want := strings.ToLower(filepath.Join(dir, "down.game", ".claude", "worktrees", "slot-1"))
	for _, p := range []string{
		filepath.Join(dir, "Down.Game", ".claude", "worktrees", "slot-1"),
		filepath.Join(dir, "Down.Game", ".Claude", "Worktrees", "Slot-1"),
		strings.ToLower(filepath.Join(dir, "Down.Game", ".claude", "worktrees", "slot-1")),
	} {
		if got := key(t, p, insensitive); got != want {
			t.Errorf("Key(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestKeyKeepsCaseOnCaseSensitiveVolume(t *testing.T) {
	dir := tempDir(t)
	p := filepath.Join(dir, "Down.Game", "Assets")
	if got := key(t, p, sensitive); got != p {
		t.Errorf("Key(%q) = %q, want it unchanged", p, got)
	}
}

func TestKeyResolvesSymlinksInTheExistingPrefix(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "real"))
	symlink(t, filepath.Join(dir, "real"), filepath.Join(dir, "link"))
	want := filepath.Join(dir, "real", "slot-1", "Assets")
	for _, p := range []string{
		filepath.Join(dir, "link", "slot-1", "Assets"),
		filepath.Join(dir, "real", "slot-1", "Assets"),
	} {
		if got := key(t, p, sensitive); got != want {
			t.Errorf("Key(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestKeyResolvesRelativeSymlinks(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, ".agents", "skills", "unity-use"))
	mkdir(t, filepath.Join(dir, ".claude", "skills"))
	symlink(t, "../../.agents/skills/unity-use", filepath.Join(dir, ".claude", "skills", "unity-use"))
	p := filepath.Join(dir, ".claude", "skills", "unity-use", "SKILL.md")
	want := filepath.Join(dir, ".agents", "skills", "unity-use", "SKILL.md")
	if got := key(t, p, sensitive); got != want {
		t.Errorf("Key(%q) = %q, want %q", p, got, want)
	}
}

func TestKeyCleans(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "a"))
	want := filepath.Join(dir, "a", "c")
	for _, p := range []string{
		dir + "/a/./b/../c",
		dir + "//a/c/",
		dir + "/a/missing/../c",
		dir + "/missing/../a/c",
	} {
		if got := key(t, p, sensitive); got != want {
			t.Errorf("Key(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestKeyFollowsDotDotFromASymlinkTarget(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "a", "b"))
	symlink(t, filepath.Join(dir, "a", "b"), filepath.Join(dir, "link"))
	// link/.. is the parent of link's target, a, not the directory holding link.
	p := filepath.Join(dir, "link") + "/../x"
	if got, want := key(t, p, sensitive), filepath.Join(dir, "a", "x"); got != want {
		t.Errorf("Key(%q) = %q, want %q", p, got, want)
	}
}

func TestKeyPassesThroughAFile(t *testing.T) {
	dir := tempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "f", "x")
	if got := key(t, p, sensitive); got != p {
		t.Errorf("Key(%q) = %q, want it unchanged", p, got)
	}
}

func TestKeyMakesRelativePathsAbsolute(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "slot-1"))
	t.Chdir(filepath.Join(dir, "slot-1"))
	for p, want := range map[string]string{
		".":         filepath.Join(dir, "slot-1"),
		"Assets/x":  filepath.Join(dir, "slot-1", "Assets", "x"),
		"../slot-2": filepath.Join(dir, "slot-2"),
	} {
		if got := key(t, p, sensitive); got != want {
			t.Errorf("Key(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestKeyAsksAboutTheDeepestExistingPath(t *testing.T) {
	dir := tempDir(t)
	mkdir(t, filepath.Join(dir, "real"))
	symlink(t, filepath.Join(dir, "real"), filepath.Join(dir, "link"))
	var asked []string
	record := func(p string) (bool, error) {
		asked = append(asked, p)
		return true, nil
	}
	key(t, filepath.Join(dir, "link", "missing", "x"), record)
	if want := filepath.Join(dir, "real"); len(asked) != 1 || asked[0] != want {
		t.Errorf("asked about %q, want only %q", asked, want)
	}
}

func TestKeyReturnsTheCaseSensitivityError(t *testing.T) {
	failed := errors.New("no answer")
	_, err := KeyWith(tempDir(t), func(string) (bool, error) { return false, failed })
	if !errors.Is(err, failed) {
		t.Errorf("err = %v, want %v", err, failed)
	}
}

func TestKeyRejectsAnEmptyPath(t *testing.T) {
	if k, err := KeyWith("", sensitive); err == nil {
		t.Errorf("Key(\"\") = %q, want an error", k)
	}
}

func TestKeyAsksCaseSensitive(t *testing.T) {
	dir := tempDir(t)
	got, err := Key(filepath.Join(dir, "x"))
	if err != nil {
		t.Fatal(err)
	}
	if got != key(t, filepath.Join(dir, "x"), CaseSensitive) {
		t.Errorf("Key = %q, want KeyWith(…, CaseSensitive)", got)
	}
}

func TestWithin(t *testing.T) {
	for _, c := range []struct {
		child, parent string
		want          bool
	}{
		{"/r/.claude/worktrees/slot-1", "/r/.claude/worktrees/slot-1", true},
		{"/r/.claude/worktrees/slot-1/Assets/a.cs", "/r/.claude/worktrees/slot-1", true},
		{"/r/.claude/worktrees/slot-10", "/r/.claude/worktrees/slot-1", false},
		{"/r/.claude/worktrees/slot-1-old/a.cs", "/r/.claude/worktrees/slot-1", false},
		{"/r/.claude/worktrees", "/r/.claude/worktrees/slot-1", false},
		{"/s/.claude/worktrees/slot-1", "/r/.claude/worktrees/slot-1", false},
		{"/r", "/", true},
		{"/", "/", true},
	} {
		if got := Within(c.child, c.parent); got != c.want {
			t.Errorf("Within(%q, %q) = %v, want %v", c.child, c.parent, got, c.want)
		}
	}
}
