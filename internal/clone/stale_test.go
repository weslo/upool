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

// editorDAGDir is the Editor's DAG in the build cache, named as the M1 gate's project has it
// (#144).
const editorDAGDir = "Library/Bee/artifacts/200b0aEDbg.dag"

// compiled gives the build cache under e's Library an assembly at mtime in the DAG directory dag.
func compiled(e *testenv.Env, dag string, mtime time.Time) {
	name := dag + "/Assembly-CSharp.dll"
	e.WriteFile(name, "assembly\n")
	e.SetMtime(name, mtime)
}

// The cases of the acceptance tests of #49 and #144, and the edits that must or must not count.
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
		{
			// The Editor compiled the newer script, and the build cache's assembly came out as
			// ScriptAssemblies already had it, so the Editor left ScriptAssemblies alone (#144).
			name: "a build-cache assembly compiled after the script",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				compiled(e, editorDAGDir, testenv.StaleTime.Add(time.Minute))
			},
			script: testenv.PlayerScript,
		},
		{
			name: "a script newer than ScriptAssemblies and the build cache",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				compiled(e, editorDAGDir, testenv.StaleTime.Add(-time.Minute))
			},
			stale:  true,
			script: testenv.PlayerScript,
			reason: testenv.PlayerScript + " (modified 2026-10-12T23:00:00Z) is newer than " +
				editorDAGDir + " (modified 2026-10-12T22:59:00Z)",
		},
		{
			name: "ScriptAssemblies missing, with a newer build cache",
			change: func(t *testing.T, e *testenv.Env) {
				if err := os.RemoveAll(e.Path("Library/ScriptAssemblies")); err != nil {
					t.Fatal(err)
				}
				compiled(e, editorDAGDir, testenv.StaleTime)
			},
			stale:  true,
			script: testenv.GameScript,
			reason: "it has no Library/ScriptAssemblies",
		},
		{
			// A player build compiles the same scripts, into assemblies the Editor does not load.
			name: "a player build's assemblies compiled after the script",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				for _, dag := range []string{"200b0aP.dag", "200b0aPDevDbg.dag"} {
					compiled(e, "Library/Bee/artifacts/"+dag, testenv.StaleTime.Add(time.Minute))
				}
			},
			stale:  true,
			script: testenv.PlayerScript,
			reason: "is newer than Library/ScriptAssemblies (modified 2026-10-12T22:00:00Z)",
		},
		{
			// Only an assembly counts: the build writes other files to the DAG directory too.
			name: "a newer file in the build cache that is not an assembly",
			opts: []testenv.Option{testenv.StaleLibrary()},
			change: func(_ *testing.T, e *testenv.Env) {
				e.WriteFile(editorDAGDir+"/Assembly-CSharp.rsp", "-target:library\n")
				e.SetMtime(editorDAGDir+"/Assembly-CSharp.rsp", testenv.StaleTime.Add(time.Minute))
			},
			stale:  true,
			script: testenv.PlayerScript,
			reason: "is newer than Library/ScriptAssemblies",
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
	if !s.CompiledTime.IsZero() || s.CompiledIn != "" || !s.Compiled().Equal(testenv.LibraryTime) {
		t.Errorf("with no build cache, CompiledTime = %v in %q, Compiled() = %v; want zero, "+
			"\"\", %v", s.CompiledTime, s.CompiledIn, s.Compiled(), testenv.LibraryTime)
	}
	compiled(e, editorDAGDir, testenv.StaleTime)
	if s := check(t, e); !s.CompiledTime.Equal(testenv.StaleTime) || s.CompiledIn != editorDAGDir ||
		!s.Compiled().Equal(testenv.StaleTime) {
		t.Errorf("CompiledTime = %v in %q, Compiled() = %v; want %v in %q", s.CompiledTime,
			s.CompiledIn, s.Compiled(), testenv.StaleTime, editorDAGDir)
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
