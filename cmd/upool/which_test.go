package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
)

// The tests of `upool which`. They write registry.json and state.json directly, as init and take
// would leave them, so they need no git and run everywhere. The /private/var spelling of a slot's
// path is macOS's, and its test is in which_darwin_test.go.

// agentWorktree is the name of the agent worktree whichPool puts beside its slots, which is not a
// slot (docs/contracts.md (e), "Agent worktree").
const agentWorktree = "agent-0123456789abcdef0"

// whichPool is a registered pool for which to search: its HOME and its main checkout as it was
// registered.
type whichPool struct {
	home, repo string
}

// newWhichPool registers repo, spelled as given, under a new HOME, with three slots under
// repo/.claude/worktrees, each a directory with an Assets/Scripts subdirectory: slot-1, leased and
// confirmed; slot-2, ready; and slot-10, leased by the create hook and not yet confirmed. An agent
// worktree sits beside them. The slots' paths are recorded with repo's spelling.
func newWhichPool(t *testing.T, repo string) whichPool {
	t.Helper()
	w := whichPool{home: tempDir(t), repo: repo}
	for _, name := range []string{"slot-1", "slot-2", "slot-10", agentWorktree} {
		if err := os.MkdirAll(filepath.Join(w.path(name), "Assets", "Scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Add(t.Context(), w.home, repo)
	if err != nil {
		t.Fatal(err)
	}
	acquired := clock.Add(-time.Hour)
	err = pool.Update(t.Context(), w.home, reg.Key, func(p *pool.Pool) error {
		p.Repo, p.Base = repo, "main"
		p.Slots = []pool.Slot{
			{Name: "slot-1", Path: w.path("slot-1"), State: pool.StateLeased, LastUsed: acquired,
				Lease: &pool.Lease{LeaseID: "a1b2c3d4", Holder: "issue-51",
					WorktreePath: w.path("slot-1"), AcquiredAt: acquired, Confirmed: true,
					Lane: "script"}},
			{Name: "slot-2", Path: w.path("slot-2"), State: pool.StateReady, LastUsed: acquired},
			{Name: "slot-10", Path: w.path("slot-10"), State: pool.StateLeasedUnconfirmed,
				LastUsed: acquired,
				Lease: &pool.Lease{LeaseID: "e5f6a7b8", Holder: "session-10",
					WorktreePath: w.path("slot-10"), AcquiredAt: acquired, Lane: "script",
					NameHint: "fix-the-build"}},
		}
		p.AgentWorktrees = []pool.AgentWorktree{{Path: w.path(agentWorktree),
			ParentSessionID: "session-1", ParentPID: 1, CreatedAt: acquired}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// path is the worktree name under the main checkout's .claude/worktrees.
func (w whichPool) path(name string) string {
	return filepath.Join(w.repo, ".claude", "worktrees", name)
}

// which runs `upool which args…` in w's HOME with wd as the working directory.
func (w whichPool) which(t *testing.T, wd string, args ...string) result {
	t.Helper()
	env := map[string]string{"HOME": w.home, "PWD": wd}
	return dispatch(t, commandTable, hookTable, env, "", append([]string{"which"}, args...)...)
}

// checkFound fails the test unless r is which printing path alone.
func checkFound(t *testing.T, r result, path string) {
	t.Helper()
	if r.code != output.ExitOK {
		t.Errorf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if want := path + "\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
}

// checkNotInSlot fails the test unless r is which exiting 1 because wd is in no slot.
func checkNotInSlot(t *testing.T, r result, wd string) {
	t.Helper()
	if r.code != output.ExitFailure {
		t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	if want := "upool: " + wd + " is not inside a slot of any registered pool\n"; r.stderr != want {
		t.Errorf("stderr = %q, want %q", r.stderr, want)
	}
}

func TestWhichPrintsTheSlotContainingTheWorkingDirectory(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	tests := []struct {
		name, wd, want string
	}{
		{"the slot itself", w.path("slot-1"), w.path("slot-1")},
		{"a subdirectory of a slot", filepath.Join(w.path("slot-1"), "Assets", "Scripts"),
			w.path("slot-1")},
		{"a path with a trailing separator", w.path("slot-2") + "/", w.path("slot-2")},
		{"a path through ..", filepath.Join(w.path("slot-2"), "Assets") + "/..", w.path("slot-2")},
		// slot-1 is a prefix of slot-10 as a string, not as a path.
		{"slot-10, not slot-1", filepath.Join(w.path("slot-10"), "Assets"), w.path("slot-10")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkFound(t, w.which(t, tt.wd), tt.want)
		})
	}
}

// The design's `which` exits 1 outside a slot (docs/contracts.md (d), "upool's own exit codes"):
// the main checkout, everywhere else under it that is not a slot, and everywhere outside it.
func TestWhichOutsideEverySlotExitsOneWithNotInSlot(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	empty := whichPool{home: tempDir(t), repo: w.repo}
	tests := []struct {
		name string
		w    whichPool
		wd   string
	}{
		{"the main checkout", w, w.repo},
		{"a subdirectory of the main checkout", w, filepath.Join(w.repo, "Assets")},
		{"the worktrees directory", w, filepath.Join(w.repo, ".claude", "worktrees")},
		{"an agent worktree", w, w.path(agentWorktree)},
		{"a directory outside the pool", w, tempDir(t)},
		{"a slot directory when nothing is registered", empty, w.path("slot-1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkNotInSlot(t, tt.w.which(t, tt.wd), tt.wd)

			r := tt.w.which(t, tt.wd, "--json")
			if r.code != output.ExitFailure || r.stderr != "" {
				t.Errorf("--json: exit = %d with stderr %q, want %d and empty", r.code, r.stderr,
					output.ExitFailure)
			}
			env := decode(t, r.stdout)
			if env.Success || env.Command != "which" || env.Data != nil || len(env.Errors) != 1 ||
				env.Errors[0].Code != string(output.CodeNotInSlot) {
				t.Errorf("--json: envelope = %+v, want a failure for which with no data and "+
					"one error, %s", env, output.CodeNotInSlot)
			}
		})
	}
}

func TestWhichJSONGivesTheSlotItsLeaseAndThePoolsRepo(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	tests := []struct {
		slot string
		want map[string]any
	}{
		{"slot-1", map[string]any{
			"name":  "slot-1",
			"path":  w.path("slot-1"),
			"state": "leased",
			"lease": map[string]any{
				"holder":      "issue-51",
				"lease_id":    "a1b2c3d4",
				"confirmed":   true,
				"acquired_at": "2026-10-19T11:00:00Z",
			},
			"repo": w.repo,
		}},
		{"slot-10", map[string]any{
			"name":  "slot-10",
			"path":  w.path("slot-10"),
			"state": "leased-unconfirmed",
			"lease": map[string]any{
				"holder":      "session-10",
				"lease_id":    "e5f6a7b8",
				"confirmed":   false,
				"acquired_at": "2026-10-19T11:00:00Z",
			},
			"repo": w.repo,
		}},
		{"slot-2", map[string]any{
			"name":  "slot-2",
			"path":  w.path("slot-2"),
			"state": "ready",
			"lease": nil,
			"repo":  w.repo,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.slot, func(t *testing.T) {
			r := w.which(t, filepath.Join(w.path(tt.slot), "Assets"), "--json")
			if r.code != output.ExitOK || r.stderr != "" {
				t.Errorf("exit = %d with stderr %q, want %d and empty", r.code, r.stderr,
					output.ExitOK)
			}
			env := decode(t, r.stdout)
			if !env.Success || env.Command != "which" || len(env.Errors) != 0 ||
				len(env.Warnings) != 0 {
				t.Errorf("envelope = %+v, want success for which with no errors or warnings", env)
			}
			if got, want := mustJSON(t, env.Data), mustJSON(t, tt.want); string(got) != string(want) {
				t.Errorf("data = %s, want %s", got, want)
			}
		})
	}
}

// snapshot is every file under dir, by path relative to it, with its contents and modification
// time, so that two snapshots are equal only if nothing was written in between.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = info.ModTime().String() + "\n" + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// which has no side effects (docs/design.md, "One binary, a dozen commands, one JSON file per
// pool"): it reads the state under the lock and writes none of it, whether it finds a slot or not.
func TestWhichWritesNothing(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	state := filepath.Join(w.home, ".upool")
	before := snapshot(t, state)
	for _, wd := range []string{w.path("slot-1"), w.path("slot-2"), w.repo} {
		for _, args := range [][]string{nil, {"--json"}} {
			w.which(t, wd, args...)
		}
	}
	after := snapshot(t, state)
	if string(mustJSON(t, after)) != string(mustJSON(t, before)) {
		t.Errorf("~/.upool after which = %v, want it unchanged: %v", after, before)
	}

	// Nor does it make a ~/.upool where there is none, as taking the lock would.
	empty := whichPool{home: tempDir(t), repo: w.repo}
	for _, wd := range []string{w.path("slot-1"), w.repo} {
		checkNotInSlot(t, empty.which(t, wd), wd)
	}
	if _, err := os.Stat(filepath.Join(empty.home, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool after which with nothing registered: stat error = %v, want it not "+
			"to exist", err)
	}
}

// A state.json which cannot read is an error, not a working directory outside every slot.
func TestWhichReportsAPoolItCannotRead(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	canonical, err := paths.Key(w.repo)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(w.home, ".upool", "pools", registry.Key(canonical), "state.json")
	if err := os.WriteFile(file, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := w.which(t, w.path("slot-1"), "--json")
	if r.code != output.ExitFailure {
		t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
	}
	env := decode(t, r.stdout)
	if len(env.Errors) != 1 || env.Errors[0].Code != string(output.CodeCommandFailed) ||
		!strings.Contains(env.Errors[0].Message, "upgrade upool") {
		t.Errorf("errors = %+v, want one %s saying to upgrade upool", env.Errors,
			output.CodeCommandFailed)
	}
}

// which takes no arguments; it does not quietly answer for the working directory when given a
// path.
func TestWhichRefusesArguments(t *testing.T) {
	w := newWhichPool(t, filepath.Join(tempDir(t), "project"))
	r := w.which(t, w.path("slot-1"), w.path("slot-2"))
	if r.code != output.ExitFailure || r.stdout != "" {
		t.Errorf("exit = %d with stdout %q, want %d and empty", r.code, r.stdout,
			output.ExitFailure)
	}
	if want := "unexpected arguments"; !strings.Contains(r.stderr, want) ||
		!strings.Contains(r.stderr, "usage: upool "+whichUsage) {
		t.Errorf("stderr = %q, want %q and the usage", r.stderr, want)
	}
}
