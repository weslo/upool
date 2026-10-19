package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These tests run real git in temporary repositories (CONTRIBUTING.md, "Real git, always").

// gitEnv is an environment for real git cut off from this machine's: a temporary HOME, no system
// config, and a fixed identity for the commits the fixtures make.
func gitEnv(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tempDir(t),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=upool test",
		"GIT_AUTHOR_EMAIL=test@upool.invalid",
		"GIT_COMMITTER_NAME=upool test",
		"GIT_COMMITTER_EMAIL=test@upool.invalid",
	}
}

// tempDir is t.TempDir() with symlinks resolved, as git prints it: on macOS it is under /var, which
// is a symlink to /private/var.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// realGit returns a Client that runs real git with gitEnv.
func realGit(t *testing.T) Client {
	t.Helper()
	return Client{Runner: Exec{}, Env: gitEnv(t)}
}

// must runs `git -C dir args…` through c's runner to build a fixture, fails the test unless it
// exits 0, and returns its stdout with surrounding space trimmed.
func must(t *testing.T, c Client, dir string, args ...string) string {
	t.Helper()
	out, err := c.run(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// writeFile writes content to name under dir, creating its parent directories.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repository on main with one commit and returns its path and HEAD.
func newRepo(t *testing.T, c Client) (repo, head string) {
	t.Helper()
	base := tempDir(t)
	repo = filepath.Join(base, "repo")
	must(t, c, base, "init", "-q", "-b", "main", repo)
	writeFile(t, repo, "README.md", "fixture\n")
	must(t, c, repo, "add", "README.md")
	must(t, c, repo, "commit", "-q", "-m", "first")
	return repo, must(t, c, repo, "rev-parse", "HEAD")
}

func TestWorktreeListParsesDetachedLockedAndPrunable(t *testing.T) {
	c := realGit(t)
	repo, head := newRepo(t, c)
	base := filepath.Dir(repo)
	detached := filepath.Join(base, "detached")
	locked := filepath.Join(base, "locked slot")
	gone := filepath.Join(base, "gone")
	reason := "leased by upool\nsession 1"
	must(t, c, repo, "worktree", "add", "-q", "--detach", detached)
	must(t, c, repo, "worktree", "add", "-q", "-b", "feature", locked)
	must(t, c, repo, "worktree", "lock", "--reason", reason, locked)
	must(t, c, repo, "worktree", "add", "-q", "--detach", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	got, err := c.WorktreeList(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Path != repo {
		t.Fatalf("WorktreeList = %+v, want 4 worktrees, %s first", got, repo)
	}
	// git sorts the linked worktrees by path; the test does not rely on the order.
	byPath := map[string]Worktree{}
	for _, wt := range got {
		byPath[wt.Path] = wt
	}
	if byPath[gone].PrunableReason == "" {
		t.Errorf("prunable reason is empty, want git's reason")
	}
	want := map[string]Worktree{
		repo:     {Path: repo, HEAD: head, Branch: "refs/heads/main"},
		detached: {Path: detached, HEAD: head, Detached: true},
		locked: {Path: locked, HEAD: head, Branch: "refs/heads/feature", Locked: true,
			LockReason: reason},
		gone: {Path: gone, HEAD: head, Detached: true, Prunable: true,
			PrunableReason: byPath[gone].PrunableReason},
	}
	if !reflect.DeepEqual(byPath, want) {
		t.Errorf("WorktreeList =\n%+v\nwant\n%+v", byPath, want)
	}

	// Asked from a linked worktree, git lists the same worktrees.
	fromLinked, err := c.WorktreeList(context.Background(), detached)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromLinked, got) {
		t.Errorf("WorktreeList from %s =\n%+v\nwant\n%+v", detached, fromLinked, got)
	}
}

// A commit made on a detached HEAD passes git status but is on no ref, so a reset to base would
// orphan it (docs/contracts.md (f), A5).
func TestHeadReachableFalseForDetachedCommit(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	slot := filepath.Join(filepath.Dir(repo), "slot-1")
	must(t, c, repo, "worktree", "add", "-q", "--detach", slot)
	ctx := context.Background()

	if ok, err := c.HeadReachable(ctx, slot); err != nil || !ok {
		t.Fatalf("HeadReachable at main's commit = %v, %v, want true, nil", ok, err)
	}
	writeFile(t, slot, "work.txt", "a session's work\n")
	must(t, c, slot, "add", "work.txt")
	must(t, c, slot, "commit", "-q", "-m", "work on a detached HEAD")

	if entries, err := c.Status(ctx, slot); err != nil || len(entries) != 0 {
		t.Fatalf("Status = %+v, %v, want no entries", entries, err)
	}
	if ok, err := c.HeadReachable(ctx, slot); err != nil || ok {
		t.Errorf("HeadReachable after a detached commit = %v, %v, want false, nil", ok, err)
	}

	// Once a branch holds the commit, it is reachable again (A18: release leaves the branch).
	must(t, c, slot, "switch", "-q", "-c", "keep")
	if ok, err := c.HeadReachable(ctx, slot); err != nil || !ok {
		t.Errorf("HeadReachable on branch keep = %v, %v, want true, nil", ok, err)
	}
}

// The spike's remove hook found `?? spike-reuse.txt` in a slot it had to park
// (testdata/captures/git/remove-status-untracked.txt). The test builds that state with real git.
func TestStatusReportsCapturedUntrackedEntry(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	writeFile(t, repo, "spike-reuse.txt", "left behind\n")

	capture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "captures", "git",
		"remove-status-untracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.run(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, capture) {
		t.Fatalf("git status --porcelain = %q, want the capture's %q", plain, capture)
	}

	got, err := c.Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{{XY: "??", Path: "spike-reuse.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

// A user's status.showUntrackedFiles=no must not make a dirty slot look clean.
func TestStatusReportsUntrackedFilesDespiteConfig(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	must(t, c, repo, "config", "status.showUntrackedFiles", "no")
	writeFile(t, repo, "untracked.txt", "x\n")

	got, err := c.Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{{XY: "??", Path: "untracked.txt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

// Library/ is gitignored in every pooled project, and a slot's Library does not make it dirty.
func TestStatusSkipsIgnoredFiles(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	writeFile(t, repo, ".gitignore", "Library/\n")
	must(t, c, repo, "add", ".gitignore")
	must(t, c, repo, "commit", "-q", "-m", "ignore Library")
	writeFile(t, repo, "Library/ScriptAssemblies/Assembly-CSharp.dll", "dll\n")

	if got, err := c.Status(context.Background(), repo); err != nil || len(got) != 0 {
		t.Errorf("Status = %+v, %v, want no entries", got, err)
	}
}

// -z leaves paths unquoted, so names with spaces and newlines come back as they are, and a rename
// carries its original path.
func TestStatusReportsRenamesAndUnusualNames(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	must(t, c, repo, "mv", "README.md", "READ ME.md")
	writeFile(t, repo, "new\nline.txt", "x\n")

	got, err := c.Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{
		{XY: "R ", Path: "READ ME.md", OrigPath: "README.md"},
		{XY: "??", Path: "new\nline.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Status = %+v, want %+v", got, want)
	}
}

// The test's own working directory is inside upool's repository, so a call that fell back to it
// would print that repository instead.
func TestExecRunsInDirNotTheWorkingDirectory(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	if got := must(t, c, repo, "rev-parse", "--show-toplevel"); got != repo {
		t.Errorf("toplevel = %q, want %q", got, repo)
	}
}

func TestExecRefusesRelativeDirectory(t *testing.T) {
	for _, dir := range []string{"", ".", "internal/git"} {
		if _, err := (Exec{}).Run(context.Background(), dir, gitEnv(t), "status"); err == nil {
			t.Errorf("Run in %q: err = nil, want an error", dir)
		}
	}
}

// Unity's managed git hooks must see UNITY_SKIP_HOOKS=1, even when the caller's environment says
// otherwise (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on
// handout, reset without `-x`").
func TestExecSetsUnitySkipHooksForHooks(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	seen := filepath.Join(tempDir(t), "seen")
	writeFile(t, repo, ".git/hooks/post-checkout", "#!/bin/sh\nprintf '%s' \"$UNITY_SKIP_HOOKS\" > '"+
		seen+"'\n")
	if err := os.Chmod(filepath.Join(repo, ".git", "hooks", "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Env = append(c.Env, "UNITY_SKIP_HOOKS=0")
	must(t, c, repo, "checkout", "-q", "--detach")

	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("the post-checkout hook did not run: %v", err)
	}
	if string(got) != "1" {
		t.Errorf("hook saw UNITY_SKIP_HOOKS=%q, want \"1\"", got)
	}
}

// A git command that runs and fails is a Result with its exit code, not a Run error.
func TestExecReturnsNonZeroExitWithoutError(t *testing.T) {
	dir := tempDir(t)
	env := append(gitEnv(t), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	res, err := (Exec{}).Run(context.Background(), dir, env, "status")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 128 || !bytes.Contains(res.Stderr, []byte("not a git repository")) {
		t.Errorf("exit = %d, stderr = %q, want 128 and \"not a git repository\"", res.ExitCode,
			res.Stderr)
	}
}

func TestExecReportsEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Exec{}).Run(ctx, tempDir(t), gitEnv(t), "status")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestTopLevelFindsTheRootFromASubdirectory(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	sub := filepath.Join(repo, "Assets", "Scripts")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repo, sub} {
		got, err := c.TopLevel(context.Background(), dir)
		if err != nil || got != repo {
			t.Errorf("TopLevel(%s) = %q, %v, want %q, nil", dir, got, err, repo)
		}
	}
}

func TestTopLevelOutsideARepositoryIsAnError(t *testing.T) {
	dir := tempDir(t)
	env := append(gitEnv(t), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	c := Client{Runner: Exec{}, Env: env}
	got, err := c.TopLevel(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("TopLevel = %q, %v, want an error saying not a git repository", got, err)
	}
}

// Only tracked paths under the pathspec are listed: not an ignored file, not a sibling whose name
// starts the same way, and not a Library/ below the top level.
func TestLsFilesListsTrackedPathsUnderThePathspec(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	writeFile(t, repo, ".gitignore", "/Library/\n")
	writeFile(t, repo, "Library/ignored.asset", "x\n")
	writeFile(t, repo, "Library/forced.asset", "x\n")
	writeFile(t, repo, "Library/sub/forced two.asset", "x\n")
	writeFile(t, repo, "LibraryNotes.txt", "x\n")
	writeFile(t, repo, "Packages/Library/x.txt", "x\n")
	must(t, c, repo, "add", ".gitignore", "LibraryNotes.txt", "Packages")
	must(t, c, repo, "add", "-f", "Library/forced.asset", "Library/sub/forced two.asset")
	got, err := c.LsFiles(context.Background(), repo, "Library/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Library/forced.asset", "Library/sub/forced two.asset"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LsFiles(Library/) = %q, want %q", got, want)
	}
	if got, err := c.LsFiles(context.Background(), repo, "Assets/"); err != nil || len(got) != 0 {
		t.Errorf("LsFiles(Assets/) = %q, %v, want nothing", got, err)
	}
}

// A path inside an ignored directory is ignored whether the directory or a parent is named, and
// whether or not the path exists.
func TestCheckIgnoreReportsPathsUnderIgnoredDirectories(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	for gitignore, want := range map[string]bool{
		"":                      false,
		"/Library/\n":           false,
		".claude/\n":            true,
		"/.claude/worktrees/\n": true,
		".claude/worktrees/\n":  true,
	} {
		writeFile(t, repo, ".gitignore", gitignore)
		got, err := c.CheckIgnore(context.Background(), repo, ".claude/worktrees/slot-1")
		if err != nil || got != want {
			t.Errorf(".gitignore %q: CheckIgnore = %v, %v, want %v, nil", gitignore, got, err, want)
		}
	}
}

func TestSymbolicRefFollowsHEADAndOriginHEAD(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	ctx := context.Background()
	if got, ok, err := c.SymbolicRef(ctx, repo, "HEAD"); err != nil || !ok || got != "main" {
		t.Errorf("SymbolicRef(HEAD) = %q, %v, %v, want main, true, nil", got, ok, err)
	}
	const originHEAD = "refs/remotes/origin/HEAD"
	if got, ok, err := c.SymbolicRef(ctx, repo, originHEAD); err != nil || ok {
		t.Errorf("SymbolicRef(%s) with no origin = %q, %v, %v, want false, nil", originHEAD, got,
			ok, err)
	}
	must(t, c, repo, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	must(t, c, repo, "symbolic-ref", originHEAD, "refs/remotes/origin/trunk")
	if got, ok, err := c.SymbolicRef(ctx, repo, originHEAD); err != nil || !ok ||
		got != "origin/trunk" {
		t.Errorf("SymbolicRef(%s) = %q, %v, %v, want origin/trunk, true, nil", originHEAD, got, ok,
			err)
	}
	must(t, c, repo, "checkout", "-q", "--detach")
	if got, ok, err := c.SymbolicRef(ctx, repo, "HEAD"); err != nil || ok {
		t.Errorf("SymbolicRef(HEAD) when detached = %q, %v, %v, want false, nil", got, ok, err)
	}
}
