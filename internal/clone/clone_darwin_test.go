//go:build darwin

package clone

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
)

// These tests run the recipe for real: real git and /bin/cp -cR in a testenv repository, the
// harness project the issue names (spike §2 TA and §9 E.1 are the reference).

// key is the pool key the tests give testenv's repository.
const key = "project-00000000"

// newPool writes e's pool, with base testenv.Base and no slots, and returns a Recipe that clones
// into it as this process, copying with copier.
func newPool(t *testing.T, e *testenv.Env, copier Copier) Recipe {
	t.Helper()
	err := pool.Update(t.Context(), e.Home, key, func(p *pool.Pool) error {
		p.Repo, p.Base = e.Repo, testenv.Base
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return Recipe{
		Git:      git.Client{Runner: git.Exec{}, Env: e.Environ()},
		Copier:   copier,
		Proc:     proc.System{},
		OwnerPID: os.Getpid(),
	}
}

// excludeWorktrees makes git ignore .claude/worktrees through .git/info/exclude, as init's
// .gitignore line would, without a commit or a change to git status.
func excludeWorktrees(e *testenv.Env) {
	e.WriteFile(".git/info/exclude", ".claude/worktrees/\n")
}

// mustClone clones a slot from e's main checkout and fails the test on an error.
func mustClone(t *testing.T, e *testenv.Env, r Recipe) pool.Slot {
	t.Helper()
	slot, err := r.Clone(t.Context(), e.Home, key, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	return slot
}

// slotGit runs git in the slot at dir with e's environment and returns its trimmed stdout.
func slotGit(t *testing.T, e *testenv.Env, dir string, args ...string) string {
	t.Helper()
	res, err := git.Exec{}.Run(t.Context(), dir, e.Environ(), args...)
	if err == nil && res.ExitCode != 0 {
		err = errors.New(strings.TrimSpace(string(res.Stderr)))
	}
	if err != nil {
		t.Fatalf("git -C %s %s: %v", dir, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// checkReady fails the test unless slot, and its entry in state.json, is ready at path with no
// owner and the given seed_head and seed_dirty, and the slot's HEAD is base, detached.
func checkReady(t *testing.T, e *testenv.Env, slot pool.Slot, name, seedHead string,
	seedDirty bool) {
	t.Helper()
	path := filepath.Join(e.Repo, ".claude", "worktrees", name)
	want := pool.Slot{Name: name, Path: path, State: pool.StateReady, SeedHead: seedHead,
		SeedDirty: seedDirty}
	if !equalSlot(slot, want) {
		t.Errorf("Clone returned %+v, want %+v", slot, want)
	}
	p, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range p.Slots {
		if s.Name == name {
			found = true
			if !equalSlot(s, want) {
				t.Errorf("state.json has %+v, want %+v", s, want)
			}
		}
	}
	if !found {
		t.Errorf("state.json slots = %+v, want %s among them", p.Slots, name)
	}
	if got := slotGit(t, e, path, "rev-parse", "HEAD"); got != e.BaseCommit {
		t.Errorf("HEAD = %s, want base %s", got, e.BaseCommit)
	}
	// symbolic-ref -q exits 1, printing nothing, when HEAD is detached.
	res, err := git.Exec{}.Run(t.Context(), path, e.Environ(), "symbolic-ref", "-q", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 {
		t.Errorf("symbolic-ref HEAD: exit = %d, stdout %q, want 1: HEAD detached", res.ExitCode,
			res.Stdout)
	}
}

// equalSlot compares the fields a freshly cloned slot sets.
func equalSlot(a, b pool.Slot) bool {
	return a.Name == b.Name && a.Path == b.Path && a.State == b.State && a.OwnerPID == nil &&
		a.OwnerPIDStart == nil && a.SeedHead == b.SeedHead && a.SeedDirty == b.SeedDirty &&
		a.Lease == nil && !a.ReimportPending
}

// exists reports whether name, relative to dir, exists, without following a final link.
func exists(t *testing.T, dir, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

// The issue's acceptance test: a clean seed at base, holding everything the recipe must leave out
// or strip, gives a clean slot at base with the seed's mtimes and agent files.
func TestClone(t *testing.T) {
	e := testenv.New(t, testenv.OpenEditor())
	excludeWorktrees(e)
	for _, name := range []string{"Temp/UnityLockfile", "Logs/Editor.log", "obj/x.csproj",
		"Builds/x.app", ".claude/worktrees/agent-0123456789abcdef0/x",
		"Library/ProtocolInstance.json"} {
		e.WriteFile(name, "upool test\n")
	}
	if status := e.Git("status", "--porcelain"); status != "" {
		t.Fatalf("seed status = %q, want it clean", status)
	}

	slot := mustClone(t, e, newPool(t, e, CP{}))
	checkReady(t, e, slot, "slot-1", e.BaseCommit, false)
	if status := slotGit(t, e, slot.Path, "status", "--porcelain"); status != "" {
		t.Errorf("slot status = %q, want nothing", status)
	}

	// mtimes under Assets/ equal the seed's. cp -cR keeps a file's mtime; a directory's is when
	// the copy made it, which Unity does not read.
	assets := 0
	err := filepath.WalkDir(e.Path("Assets"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(e.Repo, path)
		if err != nil {
			return err
		}
		seedInfo, err := d.Info()
		if err != nil {
			return err
		}
		slotInfo, err := os.Stat(filepath.Join(slot.Path, rel))
		if err != nil {
			return err
		}
		if !slotInfo.ModTime().Equal(seedInfo.ModTime()) {
			t.Errorf("%s: mtime %v, want the seed's %v", rel, slotInfo.ModTime(),
				seedInfo.ModTime())
		}
		assets++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if assets == 0 {
		t.Fatal("found no files under Assets/ to compare")
	}

	for _, name := range []string{"CLAUDE.md", "AGENTS.md", ".claude", ".agents",
		".claude/settings.local.json", "Library/ArtifactDB",
		"Library/ScriptAssemblies/Assembly-CSharp.dll"} {
		if !exists(t, slot.Path, name) {
			t.Errorf("%s is missing from the slot", name)
		}
	}
	link := filepath.Join(slot.Path, filepath.FromSlash(testenv.SkillLink))
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("resolving %s: %v", testenv.SkillLink, err)
	}
	if !strings.HasPrefix(target, slot.Path+string(filepath.Separator)) {
		t.Errorf("%s resolves to %s, want it inside the slot %s", testenv.SkillLink, target,
			slot.Path)
	}
	if !exists(t, target, "SKILL.md") {
		t.Errorf("%s resolves to %s, which has no SKILL.md", testenv.SkillLink, target)
	}

	for _, name := range []string{"Temp", "Logs", "obj", "Builds", ".claude/worktrees",
		testenv.EditorInstanceFile, "Library/ProtocolInstance.json", "Library/Pipeline"} {
		if exists(t, slot.Path, name) {
			t.Errorf("%s is in the slot, want it left out or stripped", name)
		}
	}
	// The seed is the main checkout, and the recipe only reads it.
	for _, name := range []string{testenv.EditorInstanceFile, "Library/Pipeline", "Temp"} {
		if !exists(t, e.Repo, name) {
			t.Errorf("%s is gone from the seed, want it left alone", name)
		}
	}
}

// A seed one commit past base gives a slot at base, recording the seed's HEAD as seed_head: the
// commit its Library matches (A10).
func TestCloneMovesASeedPastBaseToBase(t *testing.T) {
	e := testenv.New(t, testenv.SecondCommit())
	slot := mustClone(t, e, newPool(t, e, CP{}))
	checkReady(t, e, slot, "slot-1", e.Head, false)
	if status := slotGit(t, e, slot.Path, "status", "--porcelain"); status != "" {
		t.Errorf("slot status = %q, want nothing", status)
	}
	want := e.Git("show", testenv.Base+":"+testenv.PlayerScript) + "\n"
	got, err := os.ReadFile(filepath.Join(slot.Path, filepath.FromSlash(testenv.PlayerScript)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want base's %q", testenv.PlayerScript, got, want)
	}
}

// What a dirty seed left in the slot is reverted, and its gitignored files are kept (step 7).
func TestCloneRevertsADirtySeed(t *testing.T) {
	e := testenv.New(t, testenv.UncommittedChanges())
	slot := mustClone(t, e, newPool(t, e, CP{}))
	checkReady(t, e, slot, "slot-1", e.BaseCommit, true)
	if status := slotGit(t, e, slot.Path, "status", "--porcelain"); status != "" {
		t.Errorf("slot status = %q, want nothing", status)
	}
	want := e.Git("show", "HEAD:"+testenv.GameScript) + "\n"
	got, err := os.ReadFile(filepath.Join(slot.Path, filepath.FromSlash(testenv.GameScript)))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want the committed %q", testenv.GameScript, got, want)
	}
	if exists(t, slot.Path, testenv.ScratchScript) {
		t.Errorf("%s is in the slot, want it cleaned", testenv.ScratchScript)
	}
	for _, name := range []string{"Library/ArtifactDB", "CLAUDE.md", testenv.SkillLink} {
		if !exists(t, slot.Path, name) {
			t.Errorf("%s is missing from the slot, want gitignored files kept", name)
		}
	}
	if status := e.Git("status", "--porcelain"); status == "" {
		t.Error("seed status is clean, want its changes left alone")
	}
}

// A second clone from the main checkout takes slot-2, and copies none of slot-1 into it.
func TestCloneTakesTheNextFreeName(t *testing.T) {
	e := testenv.New(t)
	excludeWorktrees(e)
	r := newPool(t, e, CP{})
	mustClone(t, e, r)
	slot := mustClone(t, e, r)
	checkReady(t, e, slot, "slot-2", e.BaseCommit, false)
	if exists(t, slot.Path, ".claude/worktrees") {
		t.Error("slot-2 has .claude/worktrees, want it left out")
	}
}

// With no Refresher, a stale seed is refused before anything is written.
func TestCloneRefusesAStaleSeed(t *testing.T) {
	e := testenv.New(t, testenv.StaleLibrary())
	r := newPool(t, e, CP{})
	_, err := r.Clone(t.Context(), e.Home, key, e.Repo)
	if err == nil || !strings.Contains(err.Error(), "the Library in "+e.Repo+" is stale") {
		t.Fatalf("err = %v, want one saying the Library is stale", err)
	}
	p, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Slots) != 0 {
		t.Errorf("slots = %+v, want none", p.Slots)
	}
	if exists(t, e.Repo, ".claude/worktrees") {
		t.Error(".claude/worktrees exists, want nothing cloned")
	}
}

// The acceptance test of #98: a stale harness seed with no Editor gets open, `--until-ready` and
// close in the fake's log before the copy starts, and once its Editor has compiled, the check runs
// again and the clone goes on from it. A seed the Editor leaves stale fails the clone, before
// anything is written.
func TestStaleSeedIsRefreshed(t *testing.T) {
	for _, compiles := range []bool{true, false} {
		name := map[bool]string{true: "brought current", false: "still stale"}[compiles]
		t.Run(name, func(t *testing.T) {
			e := testenv.New(t, testenv.StaleLibrary())
			excludeWorktrees(e)
			u := fake.Install(t, openHeadless(e.Repo), statusReady, closeHeadless)
			var before [][]string // the fake's log when the copy starts
			spy := &spyCopier{fn: func() {
				for _, c := range u.Calls() {
					before = append(before, c.Args)
				}
			}}
			r := newPool(t, e, spy)
			var during func()
			if compiles {
				during = func() { compile(t, e.Repo) }
			}
			r.Refresher = newRefresher(e, u, newRefreshProcs(1, during))
			slot, err := r.Clone(t.Context(), e.Home, key, e.Repo)
			if !compiles {
				want := "the Library in " + e.Repo + " is still stale after its refresh"
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want one saying %q", err, want)
				}
				if spy.calls != 0 || exists(t, e.Repo, ".claude/worktrees") {
					t.Error("the clone copied, want nothing cloned")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.EqualFunc(before, refreshCalls(e.Repo, true), slices.Equal) {
				t.Errorf("unity calls when the copy started = %q, want open, status and close",
					before)
			}
			checkReady(t, e, slot, "slot-1", e.Head, false)
			if got := editors(t, e.Home); len(got) != 0 {
				t.Errorf("ledger = %+v, want empty", got)
			}
		})
	}
}

// spyCopier runs fn before its first copy, then copies with CP, or fails every copy with err.
type spyCopier struct {
	fn    func()
	err   error
	calls int
}

func (c *spyCopier) Copy(ctx context.Context, src, dst string) error {
	c.calls++
	if c.calls == 1 && c.fn != nil {
		c.fn()
	}
	if c.err != nil {
		return c.err
	}
	return CP{}.Copy(ctx, src, dst)
}

// While the copy runs, the slot is already in state.json as creating, with this process and its
// start time as owner, and the lock is free (step 2; A4).
func TestCloneWritesTheCreatingEntryAndDropsTheLockBeforeCopying(t *testing.T) {
	e := testenv.New(t)
	start, err := proc.Start(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	copier := &spyCopier{}
	copier.fn = func() {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		err := pool.Update(ctx, e.Home, key, func(p *pool.Pool) error {
			if len(p.Slots) != 1 {
				t.Errorf("slots while copying = %+v, want slot-1 alone", p.Slots)
				return nil
			}
			s := p.Slots[0]
			if s.Name != "slot-1" || s.State != pool.StateCreating || s.OwnerPID == nil ||
				*s.OwnerPID != os.Getpid() || s.OwnerPIDStart == nil ||
				!s.OwnerPIDStart.Equal(start) {
				t.Errorf("slot while copying = %+v, want slot-1 creating, owned by process %d "+
					"started at %v", s, os.Getpid(), start)
			}
			return nil
		})
		if err != nil {
			t.Errorf("taking the lock while copying: %v, want it free", err)
		}
	}
	slot := mustClone(t, e, newPool(t, e, copier))
	if copier.calls == 0 {
		t.Fatal("the copier was never called")
	}
	checkReady(t, e, slot, "slot-1", e.BaseCommit, false)
}

// A clone that fails part-way leaves its slot creating with this process as owner, for recovery
// by state once the process is gone ((f) transition 21).
func TestCloneThatFailsLeavesTheSlotCreating(t *testing.T) {
	e := testenv.New(t)
	failure := errors.New("disk full")
	_, err := newPool(t, e, &spyCopier{err: failure}).Clone(t.Context(), e.Home, key, e.Repo)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "cloning slot-1 into ") {
		t.Fatalf("err = %v, want it to name slot-1 and wrap %v", err, failure)
	}
	p, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Slots) != 1 || p.Slots[0].State != pool.StateCreating ||
		p.Slots[0].OwnerPID == nil || *p.Slots[0].OwnerPID != os.Getpid() {
		t.Errorf("slots = %+v, want slot-1 creating, owned by process %d", p.Slots, os.Getpid())
	}
}
