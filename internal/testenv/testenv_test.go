package testenv

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
)

// gitExit runs `git -C <Repo> args…` as Env.Git does and returns its exit code, for a command
// whose failure is the answer.
func gitExit(t *testing.T, e *Env, args ...string) int {
	t.Helper()
	res, err := git.Exec{}.Run(context.Background(), e.Repo, e.Environ(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return res.ExitCode
}

// newestScript returns the newest mtime under Assets/ of a .cs file, the staleness check's first
// input (docs/contracts.md (g) step 1).
func newestScript(t *testing.T, e *Env) time.Time {
	t.Helper()
	var newest time.Time
	err := filepath.WalkDir(e.Path("Assets"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".cs" {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if newest.IsZero() {
		t.Fatal("no .cs file under Assets/")
	}
	return newest
}

// oldestAssembly returns the oldest mtime of Library/ScriptAssemblies and everything in it, so a
// check passes whichever of them the staleness check ends up reading.
func oldestAssembly(t *testing.T, e *Env) time.Time {
	t.Helper()
	var oldest time.Time
	err := filepath.WalkDir(e.Path("Library/ScriptAssemblies"),
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if oldest.IsZero() || info.ModTime().Before(oldest) {
				oldest = info.ModTime()
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return oldest
}

// libraryCurrent reports whether every script is older than the Library's ScriptAssemblies.
func libraryCurrent(t *testing.T, e *Env) bool {
	t.Helper()
	return newestScript(t, e).Before(oldestAssembly(t, e))
}

// exists reports whether name, relative to the repository, exists, without following a final
// symlink.
func exists(t *testing.T, e *Env, name string) bool {
	t.Helper()
	_, err := os.Lstat(e.Path(name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestNewRepoIsCleanAndLibraryIgnored(t *testing.T) {
	e := New(t)
	if out := e.Git("status", "--porcelain"); out != "" {
		t.Errorf("git status --porcelain = %q, want nothing", out)
	}
	var tracked int
	for _, f := range strings.Split(e.Git("ls-files"), "\n") {
		if strings.HasPrefix(f, "Library/") {
			tracked++
		}
	}
	if tracked != 0 {
		t.Errorf("git ls-files lists %d files under Library/, want 0", tracked)
	}
	// Not vacuous: the Library is on disk, and git ignores it.
	for _, name := range []string{"Library/ArtifactDB", "Library/SourceAssetDB",
		"Library/Pipeline/eval-usage.jsonl", "Library/ScriptAssemblies/Assembly-CSharp.dll"} {
		if !exists(t, e, name) {
			t.Errorf("%s is missing, want a marker file", name)
		}
	}
	if got := e.Git("check-ignore", "Library/ArtifactDB"); got != "Library/ArtifactDB" {
		t.Errorf("git check-ignore Library/ArtifactDB = %q, want it ignored", got)
	}
	if exists(t, e, EditorInstanceFile) {
		t.Errorf("%s exists, want it absent with no Editor open", EditorInstanceFile)
	}
}

func TestHomeIsIsolated(t *testing.T) {
	t.Setenv("UPOOL_READY", "9")
	e := New(t)
	if real, err := os.UserHomeDir(); err == nil && e.Home == real {
		t.Fatalf("Home = %s, this machine's home directory", e.Home)
	}
	if got := e.Getenv("HOME"); got != e.Home {
		t.Errorf("Getenv(HOME) = %q, want %q", got, e.Home)
	}
	if got := e.Getenv("UPOOL_READY"); got != "" {
		t.Errorf("Getenv(UPOOL_READY) = %q, want \"\": nothing but PATH comes from this process", got)
	}
	entries, err := os.ReadDir(e.Home)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range entries {
		names = append(names, d.Name())
	}
	if !slices.Equal(names, []string{".config"}) {
		t.Errorf("Home holds %q, want only .config (no .upool)", names)
	}
	config, err := os.ReadDir(filepath.Join(e.Home, ".config"))
	if err != nil {
		t.Fatal(err)
	}
	if len(config) != 0 {
		t.Errorf("Home/.config holds %d entries, want none", len(config))
	}
	// git reads no configuration but the repository's own: not this machine's system file, and
	// not a global one, which would be under Home.
	for _, line := range strings.Split(e.Git("config", "--list", "--show-origin"), "\n") {
		if !strings.HasPrefix(line, "file:.git/config\t") {
			t.Errorf("git config entry %q, want every origin file:.git/config", line)
		}
	}
}

func TestPathsAreCanonical(t *testing.T) {
	e := New(t)
	for _, p := range []string{e.Home, e.Repo} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		if resolved != p {
			t.Errorf("path %s resolves to %s, want it canonical", p, resolved)
		}
	}
	if top := e.Git("rev-parse", "--show-toplevel"); top != e.Repo {
		t.Errorf("git rev-parse --show-toplevel = %s, want Repo %s", top, e.Repo)
	}
}

func TestNewHasOneCommitOnMain(t *testing.T) {
	e := New(t)
	if got := e.Git("rev-parse", "--abbrev-ref", "HEAD"); got != Base {
		t.Errorf("branch = %s, want %s", got, Base)
	}
	if got := e.Git("rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commits = %s, want 1", got)
	}
	if e.Head != e.BaseCommit || e.Head != e.Git("rev-parse", "HEAD") {
		t.Errorf("Head = %s, BaseCommit = %s, want both HEAD", e.Head, e.BaseCommit)
	}
}

func TestProjectFiles(t *testing.T) {
	e := New(t)
	version, err := os.ReadFile(e.Path("ProjectSettings/ProjectVersion.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "m_EditorVersion: 6000.4.11f1\n"; string(version) != want {
		t.Errorf("ProjectVersion.txt = %q, want %q", version, want)
	}
	settings, err := os.ReadFile(e.Path("ProjectSettings/ProjectSettings.asset"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "\n  scriptingDefineSymbols:\n    Standalone: ") {
		t.Errorf("ProjectSettings.asset has no scriptingDefineSymbols line:\n%s", settings)
	}
	tracked := strings.Split(e.Git("ls-files", "Assets/Scripts"), "\n")
	if want := []string{GameScript, PlayerScript}; !slices.Equal(tracked, want) {
		t.Errorf("tracked scripts = %q, want %q", tracked, want)
	}
}

func TestLibraryIsCurrentAtHead(t *testing.T) {
	e := New(t)
	if !libraryCurrent(t, e) {
		t.Errorf("newest script %v, oldest ScriptAssemblies entry %v, want the scripts older",
			newestScript(t, e), oldestAssembly(t, e))
	}
}

func TestAgentFilesAreIgnoredAndSkillLinkResolves(t *testing.T) {
	e := New(t)
	ignored := []string{"CLAUDE.md", "AGENTS.md", ".claude/settings.local.json",
		".agents/skills/x/SKILL.md", SkillLink}
	for _, name := range ignored {
		if !exists(t, e, name) {
			t.Errorf("%s is missing", name)
		}
	}
	got := strings.Split(e.Git(append([]string{"check-ignore"}, ignored...)...), "\n")
	if !slices.Equal(got, ignored) {
		t.Errorf("git check-ignore = %q, want all of %q", got, ignored)
	}
	target, err := os.Readlink(e.Path(SkillLink))
	if err != nil {
		t.Fatal(err)
	}
	if target != "../../.agents/skills/x" {
		t.Errorf("%s -> %s, want ../../.agents/skills/x", SkillLink, target)
	}
	resolved, err := filepath.EvalSymlinks(e.Path(SkillLink))
	if err != nil {
		t.Fatal(err)
	}
	if want := e.Path(".agents/skills/x"); resolved != want {
		t.Errorf("%s resolves to %s, want %s", SkillLink, resolved, want)
	}
	// init's job, not the harness's (docs/contracts.md (g): doctor checks it is ignored).
	if code := gitExit(t, e, "check-ignore", "-q", ".claude/worktrees/slot-1"); code != 1 {
		t.Errorf("git check-ignore .claude/worktrees/slot-1 exit = %d, want 1 (not ignored)", code)
	}
}

func TestStaleLibraryHasScriptNewerThanAssemblies(t *testing.T) {
	e := New(t, StaleLibrary())
	if libraryCurrent(t, e) {
		t.Errorf("newest script %v, oldest ScriptAssemblies entry %v, want the script newer",
			newestScript(t, e), oldestAssembly(t, e))
	}
	info, err := os.Stat(e.Path(PlayerScript))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(StaleTime) {
		t.Errorf("%s mtime = %v, want %v", PlayerScript, info.ModTime(), StaleTime)
	}
	if out := e.Git("status", "--porcelain"); out != "" {
		t.Errorf("git status --porcelain = %q, want nothing: staleness is not a change", out)
	}
}

func TestOpenEditorWritesInstanceFile(t *testing.T) {
	e := New(t, OpenEditor())
	if !exists(t, e, EditorInstanceFile) {
		t.Errorf("%s is missing, want it with an Editor open", EditorInstanceFile)
	}
	if out := e.Git("status", "--porcelain"); out != "" {
		t.Errorf("git status --porcelain = %q, want nothing", out)
	}
}

func TestUncommittedChangesLeaveLibraryCurrent(t *testing.T) {
	e := New(t, UncommittedChanges())
	// Through internal/git, as upool reads it: Env.Git trims the leading space of " M".
	c := git.Client{Runner: git.Exec{}, Env: e.Environ()}
	got, err := c.Status(context.Background(), e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []git.StatusEntry{{XY: " M", Path: GameScript}, {XY: "??", Path: ScratchScript}}
	if !slices.Equal(got, want) {
		t.Errorf("git status = %+v, want %+v", got, want)
	}
	if !libraryCurrent(t, e) {
		t.Errorf("newest script %v, oldest ScriptAssemblies entry %v, want the Library current",
			newestScript(t, e), oldestAssembly(t, e))
	}
}

func TestSecondCommitPutsHeadOnePastBase(t *testing.T) {
	e := New(t, SecondCommit())
	if e.Head == e.BaseCommit {
		t.Fatalf("Head = BaseCommit = %s, want them different", e.Head)
	}
	if got := e.Git("rev-parse", Base); got != e.BaseCommit {
		t.Errorf("%s = %s, want BaseCommit %s", Base, got, e.BaseCommit)
	}
	if got := e.Git("rev-parse", "HEAD"); got != e.Head {
		t.Errorf("HEAD = %s, want Head %s", got, e.Head)
	}
	if got := e.Git("rev-parse", "--abbrev-ref", "HEAD"); got != Branch {
		t.Errorf("branch = %s, want %s", got, Branch)
	}
	if got := e.Git("rev-list", "--count", Base+"..HEAD"); got != "1" {
		t.Errorf("commits past %s = %s, want 1", Base, got)
	}
	if got := e.Git("rev-list", "--count", Base); got != "1" {
		t.Errorf("commits on %s = %s, want 1", Base, got)
	}
	if got := e.Git("diff", "--name-only", Base, "HEAD"); got != PlayerScript {
		t.Errorf("files changed past %s = %q, want %s", Base, got, PlayerScript)
	}
	if out := e.Git("status", "--porcelain"); out != "" {
		t.Errorf("git status --porcelain = %q, want nothing", out)
	}
	if !libraryCurrent(t, e) {
		t.Errorf("newest script %v, oldest ScriptAssemblies entry %v, want the Library current",
			newestScript(t, e), oldestAssembly(t, e))
	}
}

func TestOptionsCombine(t *testing.T) {
	e := New(t, OpenEditor(), StaleLibrary(), UncommittedChanges(), SecondCommit())
	if libraryCurrent(t, e) {
		t.Error("Library is current, want it stale")
	}
	if !exists(t, e, EditorInstanceFile) {
		t.Errorf("%s is missing", EditorInstanceFile)
	}
	if e.Git("status", "--porcelain") == "" {
		t.Error("git status --porcelain prints nothing, want the uncommitted changes")
	}
	if e.Head == e.BaseCommit {
		t.Error("Head = BaseCommit, want HEAD one commit past base")
	}
}

func TestSetMtime(t *testing.T) {
	e := New(t)
	want := time.Date(2026, time.October, 13, 9, 30, 0, 0, time.UTC)
	e.SetMtime(GameScript, want)
	info, err := os.Stat(e.Path(GameScript))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(want) {
		t.Errorf("mtime = %v, want %v", info.ModTime(), want)
	}
}

func TestSetenvReachesGetenvAndEnviron(t *testing.T) {
	e := New(t)
	e.Setenv("PATH", "/fake/bin")
	if got := e.Getenv("PATH"); got != "/fake/bin" {
		t.Errorf("Getenv(PATH) = %q, want /fake/bin", got)
	}
	env := e.Environ()
	if !slices.Contains(env, "PATH=/fake/bin") || !slices.Contains(env, "HOME="+e.Home) {
		t.Errorf("Environ() = %q, want PATH=/fake/bin and HOME=%s", env, e.Home)
	}
	if !slices.IsSorted(env) {
		t.Errorf("Environ() = %q, want it sorted", env)
	}
}
