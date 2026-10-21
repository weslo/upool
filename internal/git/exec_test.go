package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestHeadNamesTheCheckedOutCommit(t *testing.T) {
	c := realGit(t)
	repo, first := newRepo(t, c)
	if got, err := c.Head(context.Background(), repo); err != nil || got != first {
		t.Errorf("Head = %q, %v, want %q, nil", got, err, first)
	}
	writeFile(t, repo, "README.md", "changed\n")
	must(t, c, repo, "commit", "-q", "-am", "second")
	must(t, c, repo, "checkout", "-q", "--detach", first)
	if got, err := c.Head(context.Background(), repo); err != nil || got != first {
		t.Errorf("Head on a detached HEAD = %q, %v, want %q, nil", got, err, first)
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

// DiffSides reads both versions through git, the whole file whatever the user's diff settings, and
// CheckoutPath reverts the one file it names.
func TestDiffSidesAndCheckoutPathOnAWorktree(t *testing.T) {
	c := realGit(t)
	repo, _ := newRepo(t, c)
	var long strings.Builder
	for i := range 40 {
		fmt.Fprintf(&long, "line %d\r\n", i)
	}
	committed := long.String() + "\nlast"
	writeFile(t, repo, "settings.asset", committed)
	writeFile(t, repo, "bin.dat", "a\x00b")
	must(t, c, repo, "add", "-A")
	must(t, c, repo, "commit", "-q", "-m", "files")
	for _, setting := range [][2]string{{"diff.suppressBlankEmpty", "true"},
		{"diff.context", "0"}, {"diff.noprefix", "true"}, {"color.diff", "always"},
		{"diff.external", "false"}} {
		must(t, c, repo, "config", setting[0], setting[1])
	}

	index, worktree, ok, err := c.DiffSides(context.Background(), repo, "settings.asset")
	if err != nil || ok || index != nil || worktree != nil {
		t.Errorf("DiffSides of an unchanged file = %q, %q, %v, %v, want no patch", index, worktree,
			ok, err)
	}

	changed := strings.Replace(committed, "line 30\r\n", "line thirty\r\n", 1) + "\n"
	writeFile(t, repo, "settings.asset", changed)
	writeFile(t, repo, "README.md", "edited\n")
	writeFile(t, repo, "bin.dat", "a\x00c")
	index, worktree, ok, err = c.DiffSides(context.Background(), repo, "settings.asset")
	if err != nil || !ok {
		t.Fatalf("DiffSides = %v, %v, want a patch", ok, err)
	}
	if string(index) != committed || string(worktree) != changed {
		t.Errorf("DiffSides = %q, %q, want %q, %q", index, worktree, committed, changed)
	}
	if _, _, ok, err := c.DiffSides(context.Background(), repo, "bin.dat"); err != nil || ok {
		t.Errorf("DiffSides of a binary file = %v, %v, want no patch", ok, err)
	}

	if err := c.CheckoutPath(context.Background(), repo, "settings.asset"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(repo, "settings.asset")); err != nil ||
		string(b) != committed {
		t.Errorf("settings.asset = %q, %v, want it as committed", b, err)
	}
	if status := must(t, c, repo, "status", "--porcelain"); status != "M README.md\n M bin.dat" {
		t.Errorf("status = %q, want README.md and bin.dat still modified", status)
	}
}

// WorktreeRemove takes a worktree with untracked files away, registration and directory, and
// WorktreePrune drops the registration of one whose directory is already gone.
func TestWorktreeRemoveAndPrune(t *testing.T) {
	c := realGit(t)
	repo, head := newRepo(t, c)
	removed, deleted := filepath.Join(repo, "wt-removed"), filepath.Join(repo, "wt-deleted")
	for _, wt := range []string{removed, deleted} {
		if err := c.WorktreeAdd(context.Background(), repo, wt, head); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, removed, "half-copied.txt", "partial\n")
	if err := c.WorktreeRemove(context.Background(), repo, removed); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}
	if err := c.WorktreePrune(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s: stat error = %v, want it removed", removed, err)
	}
	wts, err := c.WorktreeList(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 || wts[0].Path != repo {
		t.Errorf("worktrees = %+v, want the main one alone", wts)
	}
}

// The lease calls on a real worktree: a valid and an invalid branch name, a branch that exists
// and one that does not, checking each out, and the lock with its reason, then unlocking it.
func TestLeaseCallsOnAWorktree(t *testing.T) {
	c := realGit(t)
	repo, head := newRepo(t, c)
	ctx := context.Background()
	wt := filepath.Join(repo, "wt")
	if err := c.WorktreeAdd(ctx, repo, wt, head); err != nil {
		t.Fatal(err)
	}
	must(t, c, wt, "checkout", "-q", "--detach", head) // fill the --no-checkout worktree
	for name, want := range map[string]bool{"feature/x": true, "bad..name": false, "-x": false} {
		if ok, err := c.CheckBranchName(ctx, repo, name); err != nil || ok != want {
			t.Errorf("CheckBranchName(%q) = %t, %v; want %t", name, ok, err, want)
		}
	}
	if ok, err := c.BranchExists(ctx, repo, "main"); err != nil || !ok {
		t.Errorf("BranchExists(main) = %t, %v; want true", ok, err)
	}
	if ok, err := c.BranchExists(ctx, repo, "feature/x"); err != nil || ok {
		t.Errorf("BranchExists(feature/x) = %t, %v; want false", ok, err)
	}
	if err := c.CheckoutNewBranch(ctx, wt, "feature/x", "main"); err != nil {
		t.Fatal(err)
	}
	if got := must(t, c, wt, "symbolic-ref", "--short", "HEAD"); got != "feature/x" {
		t.Errorf("HEAD = %s, want feature/x", got)
	}
	if err := c.CheckoutBranch(ctx, wt, "main"); err == nil ||
		!strings.Contains(err.Error(), repo) {
		t.Errorf("CheckoutBranch(main), checked out in the main worktree: %v, want git's "+
			"refusal naming %s", err, repo)
	}
	must(t, c, repo, "branch", "other")
	if err := c.CheckoutBranch(ctx, wt, "other"); err != nil {
		t.Fatal(err)
	}

	must(t, c, repo, "worktree", "lock", "--reason", "upool lease 0123", wt)
	wts, err := c.WorktreeList(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 || !wts[1].Locked || wts[1].LockReason != "upool lease 0123" {
		t.Errorf("worktrees = %+v, want wt locked with its reason", wts)
	}
	if err := c.WorktreeUnlock(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	if wts, err = c.WorktreeList(ctx, repo); err != nil || wts[1].Locked {
		t.Errorf("worktrees after unlock = %+v, %v; want wt unlocked", wts, err)
	}
}
