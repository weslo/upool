package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// The harness pool that doctor's and take's tests run against, made by hand rather than by init so
// that the tests run everywhere: a testenv repository whose .gitignore ignores .claude/worktrees,
// two slots made with `git worktree add --detach` at base, and the pool registered with both
// ready, as init and a later clone would leave it, on a machine where `upool setup` has run.

// harnessPool is a harness pool.
type harnessPool struct {
	*testenv.Env
	key string
	// tools is whether a fake unity and claude are on PATH; without them only git is, and the
	// version checks warn.
	tools bool
}

// slot returns the path of the harness pool's slot name.
func (p harnessPool) slot(name string) string {
	return p.Path(".claude/worktrees/" + name)
}

// newHarnessPool makes a harness pool with slot-1 and slot-2, both ready and unleased.
func newHarnessPool(t *testing.T) harnessPool {
	t.Helper()
	return harnessPoolIn(t, newInitEnv(t))
}

// harnessPoolIn makes e's repository a harness pool, as newHarnessPool does, with upool's hooks
// registered in its settings as init registers them. Only
// git and upool, this test binary, are on its PATH, so doctor runs neither this machine's unity
// and claude nor, at the cost of a process each, the fakes; installTools (doctor_test.go) puts the
// fakes there.
func harnessPoolIn(t *testing.T, e *testenv.Env) harnessPool {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := tempDir(t)
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	e.Setenv("PATH", bin)
	registerProjectHooks(t, e)
	p := harnessPool{Env: e}
	p.WriteFile(".gitignore", readFile(t, p.Env, ".gitignore")+worktreesLine+"\n")
	var slots []pool.Slot
	for _, name := range []string{"slot-1", "slot-2"} {
		p.Git("worktree", "add", "-q", "--detach", p.slot(name), testenv.Base)
		slots = append(slots, pool.Slot{Name: name, Path: p.slot(name), State: pool.StateReady,
			SeedHead: p.Head, LastUsed: clock})
	}
	reg, err := registry.Add(t.Context(), p.Home, p.Repo)
	if err != nil {
		t.Fatal(err)
	}
	p.key = reg.Key
	p.update(t, func(pl *pool.Pool) {
		pl.Repo, pl.Base, pl.Slots = p.Repo, testenv.Base, slots
	})
	return p
}

// registerProjectHooks registers upool's hooks in the project's .claude/settings.local.json, as
// init's last step does (A26), to run a link to this test binary that it puts in a directory of its
// own at the end of e's PATH.
func registerProjectHooks(t *testing.T, e *testenv.Env) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := tempDir(t)
	upool := filepath.Join(bin, upoolName)
	if err := os.Symlink(exe, upool); err != nil {
		t.Fatal(err)
	}
	e.Setenv("PATH", e.Getenv("PATH")+string(os.PathListSeparator)+bin)
	reg, err := planRegistration(e.Path(projectSettings), upool, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.apply(); err != nil {
		t.Fatal(err)
	}
}

// update changes the harness pool's state.json with f.
func (p harnessPool) update(t *testing.T, f func(*pool.Pool)) {
	t.Helper()
	err := pool.Update(t.Context(), p.Home, p.key, func(pl *pool.Pool) error {
		f(pl)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// slotNames returns the names of the slots in the harness pool's state.json.
func (p harnessPool) slotNames(t *testing.T) []string {
	t.Helper()
	pl, err := pool.Load(p.Home, p.key)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range pl.Slots {
		names = append(names, s.Name)
	}
	return names
}

// listed reports whether `git worktree list` in the harness pool lists path.
func (p harnessPool) listed(path string) bool {
	return strings.Contains(p.Git("worktree", "list", "--porcelain"), "worktree "+path+"\n")
}
