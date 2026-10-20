package clone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/testenv"
)

// check runs CheckStaleness on e's repository with real git and fails the test on an error.
func check(t *testing.T, e *testenv.Env) Staleness {
	t.Helper()
	g := git.Client{Runner: git.Exec{}, Env: e.Environ()}
	s, err := CheckStaleness(t.Context(), g, e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The three cases of the issue's acceptance test, and the edits that must or must not count.
func TestStaleness(t *testing.T) {
	tests := []struct {
		name   string
		opts   []testenv.Option
		change func(t *testing.T, e *testenv.Env)
		stale  bool
		// script is the newest script, and reason a part of Reason.
		script, reason string
	}{
		{name: "fresh", script: testenv.GameScript},
		{
			name:   "stale by one .cs edit",
			opts:   []testenv.Option{testenv.StaleLibrary()},
			stale:  true,
			script: testenv.PlayerScript,
			reason: testenv.PlayerScript + " (modified 2026-10-12T23:00:00Z) is newer than " +
				"Library/ScriptAssemblies (modified 2026-10-12T22:00:00Z)",
		},
		{
			name: "ScriptAssemblies missing",
			change: func(t *testing.T, e *testenv.Env) {
				if err := os.RemoveAll(e.Path("Library/ScriptAssemblies")); err != nil {
					t.Fatal(err)
				}
			},
			stale:  true,
			script: testenv.GameScript,
			reason: "it has no Library/ScriptAssemblies",
		},
		{
			name: "a new script in a nested directory",
			change: func(_ *testing.T, e *testenv.Env) {
				e.WriteFile("Assets/Game/Enemies/Enemy.cs", "public class Enemy {}\n")
				e.SetMtime("Assets/Game/Enemies/Enemy.cs", testenv.StaleTime)
			},
			stale:  true,
			script: "Assets/Game/Enemies/Enemy.cs",
			reason: "Assets/Game/Enemies/Enemy.cs (modified 2026-10-12T23:00:00Z)",
		},
		{
			name: "newer files that are not scripts",
			change: func(_ *testing.T, e *testenv.Env) {
				for _, name := range []string{"Assets/Scripts/Player.cs.meta",
					"Assets/Prefabs/Player.prefab", "Assets/Scripts/Notes.csv"} {
					e.WriteFile(name, "not a script\n")
					e.SetMtime(name, testenv.StaleTime)
				}
			},
			script: testenv.GameScript,
		},
		{
			// An Editor that rewrites an assembly in place moves only the file's mtime.
			name: "an assembly rewritten after the script, in place",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				e.SetMtime("Library/ScriptAssemblies/Assembly-CSharp.dll",
					testenv.StaleTime.Add(time.Minute))
			},
			script: testenv.PlayerScript,
		},
		{
			// One that writes a new file and renames it over the old moves the directory's too,
			// and the file keeps whatever mtime it was written with.
			name: "an assembly replaced after the script",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				e.SetMtime("Library/ScriptAssemblies", testenv.StaleTime.Add(time.Minute))
			},
			script: testenv.PlayerScript,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, tt.opts...)
			if tt.change != nil {
				tt.change(t, e)
			}
			s := check(t, e)
			if s.Stale() != tt.stale {
				t.Errorf("Stale() = %v, want %v; report %+v", s.Stale(), tt.stale, s)
			}
			// Both scripts share ScriptTime, so a fresh project's newest is the first walked.
			if s.NewestScript != tt.script {
				t.Errorf("NewestScript = %q, want %q", s.NewestScript, tt.script)
			}
			reason := s.Reason()
			if tt.stale && !strings.Contains(reason, tt.reason) {
				t.Errorf("Reason() = %q, want it to contain %q", reason, tt.reason)
			}
			if !tt.stale && reason != "" {
				t.Errorf("Reason() = %q, want empty", reason)
			}
		})
	}
}

func TestStalenessReadsTheAssemblyTimes(t *testing.T) {
	e := testenv.New(t)
	s := check(t, e)
	if s.AssembliesMissing || !s.AssembliesTime.Equal(testenv.LibraryTime) {
		t.Errorf("AssembliesTime = %v, missing %v, want %v, false", s.AssembliesTime,
			s.AssembliesMissing, testenv.LibraryTime)
	}
	if !s.ScriptTime.Equal(testenv.ScriptTime) {
		t.Errorf("ScriptTime = %v, want %v", s.ScriptTime, testenv.ScriptTime)
	}
}

// HEAD and the dirty check are recorded whether or not the Library is stale, and a dirty seed is
// not a stale one: the clone reverts what it left ((g) step 7).
func TestStalenessRecordsHeadAndDirty(t *testing.T) {
	tests := []struct {
		name         string
		opts         []testenv.Option
		dirty, stale bool
	}{
		{"clean at base", nil, false, false},
		{"one commit past base", []testenv.Option{testenv.SecondCommit()}, false, false},
		{"uncommitted changes", []testenv.Option{testenv.UncommittedChanges()}, true, false},
		{"stale and dirty", []testenv.Option{testenv.UncommittedChanges(), testenv.StaleLibrary()},
			true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := testenv.New(t, tt.opts...)
			s := check(t, e)
			if s.Head != e.Head || s.Dirty != tt.dirty || s.Stale() != tt.stale {
				t.Errorf("Head %q, Dirty %v, Stale() %v; want %q, %v, %v", s.Head, s.Dirty,
					s.Stale(), e.Head, tt.dirty, tt.stale)
			}
		})
	}
}

// A newer script reached through a symbolic link does not count: only files under Assets do.
func TestStalenessSkipsSymbolicLinks(t *testing.T) {
	e := testenv.New(t)
	e.WriteFile("Outside/Linked.cs", "public class Linked {}\n")
	e.SetMtime("Outside/Linked.cs", testenv.StaleTime)
	if err := os.Symlink("../../Outside/Linked.cs", e.Path("Assets/Scripts/Linked.cs")); err != nil {
		t.Fatal(err)
	}
	if s := check(t, e); s.Stale() {
		t.Errorf("Stale() = true, want false; report %+v", s)
	}
}

func TestStalenessOutsideARepositoryIsAnError(t *testing.T) {
	e := testenv.New(t)
	if err := os.RemoveAll(e.Path(".git")); err != nil {
		t.Fatal(err)
	}
	e.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(e.Repo))
	g := git.Client{Runner: git.Exec{}, Env: e.Environ()}
	if _, err := CheckStaleness(t.Context(), g, e.Repo); err == nil ||
		!strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want one saying not a git repository", err)
	}
}
