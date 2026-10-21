package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of init's refusals, which change nothing and so run everywhere. The tests of an init
// that succeeds clone a slot with `cp -cR`, and run only on macOS (init_darwin_test.go).

// newInitEnv is a testenv environment for init. init loads every setting, and off darwin config
// cannot default editor_ram_budget_gb from the machine's RAM (config.PhysicalMemory), so the
// environment sets it; init does not use it. init registers the hooks to run upool by the path it
// finds this binary at on PATH, so PATH ends in a directory holding a link to it, as a Homebrew
// install's does.
func newInitEnv(t *testing.T, opts ...testenv.Option) *testenv.Env {
	t.Helper()
	e := testenv.New(t, opts...)
	e.Setenv(config.EnvName("editor_ram_budget_gb"), "16")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := tempDir(t)
	if err := os.Symlink(exe, filepath.Join(bin, upoolName)); err != nil {
		t.Fatal(err)
	}
	e.Setenv("PATH", e.Getenv("PATH")+string(os.PathListSeparator)+bin)
	return e
}

// runIn runs upool with args in e, with dir as the working directory: the real command table,
// e's environment, the test clock, and this process's ID, which a clone records as its slot's
// owner and reads the start time of, and its parent's, which take --confirm records as a lease's
// holder.
func runIn(t *testing.T, e *testenv.Env, dir string, args ...string) result {
	t.Helper()
	return runAt(t, e, dir, func() time.Time { return clock }, args...)
}

// runAt is runIn with now as upool's clock, such as time.Now inside a testing/synctest bubble.
func runAt(t *testing.T, e *testenv.Env, dir string, now func() time.Time,
	args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{
		commands:   commandTable,
		hooks:      hookTable,
		stdin:      strings.NewReader(""),
		stdout:     &out,
		stderr:     &errOut,
		getenv:     e.Getenv,
		environ:    e.Environ,
		getwd:      func() (string, error) { return dir, nil },
		now:        now,
		pid:        os.Getpid(),
		ppid:       os.Getppid(),
		executable: os.Executable,
	}
	code := d.run(args)
	return result{code, out.String(), errOut.String()}
}

// readFile returns name, relative to e's repository, or "" if it does not exist.
func readFile(t *testing.T, e *testenv.Env, name string) string {
	t.Helper()
	b, err := os.ReadFile(e.Path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkRefused fails the test unless r is a refusal whose message contains each of want, and init
// wrote nothing: no ~/.upool, no .upool.toml, .gitignore as it was, and no block in CLAUDE.md.
func checkRefused(t *testing.T, e *testenv.Env, r result, gitignore string, want ...string) {
	t.Helper()
	if r.code != output.ExitFailure {
		t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q, want empty", r.stdout)
	}
	for _, w := range want {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr = %q, want it to contain %q", r.stderr, w)
		}
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool: stat error = %v, want it not to exist", err)
	}
	if got := readFile(t, e, ".upool.toml"); got != "" {
		t.Errorf(".upool.toml = %q, want none", got)
	}
	if got := readFile(t, e, ".gitignore"); got != gitignore {
		t.Errorf(".gitignore = %q, want it unchanged: %q", got, gitignore)
	}
	if got := readFile(t, e, "CLAUDE.md"); strings.Contains(got, "<!-- upool:") {
		t.Errorf("CLAUDE.md = %q, want no upool block", got)
	}
}

func TestInitRefusesTrackedLibrary(t *testing.T) {
	e := newInitEnv(t)
	e.Git("add", "-f", "Library/ArtifactDB")
	e.Git("commit", "-q", "-m", "track the Library")
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		"git tracks 1 file under Library/", "Library/ArtifactDB")
}

func TestInitRefusesOpenEditor(t *testing.T) {
	for _, file := range []string{"Library/EditorInstance.json", "Library/ProtocolInstance.json"} {
		t.Run(filepath.Base(file), func(t *testing.T) {
			var e *testenv.Env
			if file == testenv.EditorInstanceFile {
				e = newInitEnv(t, testenv.OpenEditor())
			} else {
				e = newInitEnv(t)
				e.WriteFile(file, "{}\n")
			}
			r := runIn(t, e, e.Repo, "init")
			checkRefused(t, e, r, readFile(t, e, ".gitignore"),
				"the Unity Editor has "+e.Repo+" open", file)
		})
	}
}

// A stale Library would hand the first slot, and every clone after it, a recompile
// (docs/contracts.md (g) step 1), so init refuses it and says how to bring it current.
func TestInitRefusesStaleLibrary(t *testing.T) {
	const fix = "open the project in Unity, let it finish importing and compiling, quit it, " +
		"then run upool init again"
	tests := []struct {
		name   string
		setup  func(t *testing.T) *testenv.Env
		reason string
	}{
		{"a script newer than ScriptAssemblies", func(t *testing.T) *testenv.Env {
			return newInitEnv(t, testenv.StaleLibrary())
		}, testenv.PlayerScript + " (modified 2026-10-12T23:00:00Z) is newer than " +
			"Library/ScriptAssemblies (modified 2026-10-12T22:00:00Z)"},
		{"no ScriptAssemblies", func(t *testing.T) *testenv.Env {
			e := newInitEnv(t)
			if err := os.RemoveAll(e.Path("Library/ScriptAssemblies")); err != nil {
				t.Fatal(err)
			}
			return e
		}, "it has no Library/ScriptAssemblies"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.setup(t)
			r := runIn(t, e, e.Repo, "init")
			checkRefused(t, e, r, readFile(t, e, ".gitignore"),
				"the Library in "+e.Repo+" is stale: "+tt.reason+": "+fix)
		})
	}
}

// With no origin and a detached HEAD there is no branch to default to, so init asks for one.
func TestInitRefusesToGuessBaseOnDetachedHEAD(t *testing.T) {
	e := newInitEnv(t)
	e.Git("checkout", "-q", "--detach")
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		"origin has no default branch and HEAD is detached", "set base in")
}

// The first slot is moved to base ((g) step 7), so a base that names no commit is refused before
// init writes anything, rather than after the clone has copied the checkout.
func TestInitRefusesABaseThatNamesNoCommit(t *testing.T) {
	e := newInitEnv(t)
	e.Setenv(config.EnvName("base"), "release")
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		"base is release, from UPOOL_BASE, which names no commit in "+e.Repo,
		"create the branch, or set base to one that exists")
}

// A CLAUDE.md whose upool markers do not pair is refused before init writes anything, rather than
// guessed at (internal/block).
func TestInitRefusesACLAUDEMDWhoseMarkersDoNotPair(t *testing.T) {
	e := newInitEnv(t)
	doc := "# Fake project\n\n" + block.Begin(1) + "\nA block with no end.\n"
	e.WriteFile("CLAUDE.md", doc)
	gitignore := readFile(t, e, ".gitignore")
	r := runIn(t, e, e.Repo, "init")
	if r.code != output.ExitFailure {
		t.Errorf("exit = %d, want %d", r.code, output.ExitFailure)
	}
	want := "upserting upool's block into " + e.Path("CLAUDE.md") + ": line 3 begins a upool " +
		"block that no line after it ends"
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
	if got := readFile(t, e, "CLAUDE.md"); got != doc {
		t.Errorf("CLAUDE.md = %q, want it unchanged: %q", got, doc)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".upool")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.upool: stat error = %v, want it not to exist", err)
	}
	if got := readFile(t, e, ".upool.toml"); got != "" {
		t.Errorf(".upool.toml = %q, want none", got)
	}
	if got := readFile(t, e, ".gitignore"); got != gitignore {
		t.Errorf(".gitignore = %q, want it unchanged: %q", got, gitignore)
	}
}

func TestInitRefusesAProjectBelowTheRoot(t *testing.T) {
	e := newInitEnv(t)
	if err := os.MkdirAll(e.Path("Game"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.Git("mv", "ProjectSettings", "Game")
	e.Git("commit", "-q", "-m", "move the project")
	gitignore := readFile(t, e, ".gitignore")
	for _, dir := range []string{e.Repo, e.Path("Game")} {
		r := runIn(t, e, dir, "init")
		checkRefused(t, e, r, gitignore,
			"the Unity project "+e.Path("Game")+" is not at the root of its git repository")
	}
}

func TestInitRefusesARepositoryThatIsNotAUnityProject(t *testing.T) {
	e := newInitEnv(t)
	e.Git("rm", "-q", "-r", "ProjectSettings")
	e.Git("commit", "-q", "-m", "not Unity")
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		e.Repo+" is not a Unity project: it has no ProjectSettings/ProjectVersion.txt")
}

func TestInitOutsideAGitRepositoryFails(t *testing.T) {
	e := newInitEnv(t)
	dir := filepath.Join(filepath.Dir(e.Repo), "elsewhere")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	r := runIn(t, e, dir, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		"upool init runs inside a Unity project's git repository", "not a git repository")
}

func TestInitJSONReportsARefusalAsAFailureEnvelope(t *testing.T) {
	e := newInitEnv(t, testenv.OpenEditor())
	r := runIn(t, e, e.Repo, "init", "--json")
	if r.code != output.ExitFailure || r.stderr != "" {
		t.Errorf("exit = %d, stderr = %q, want %d and empty", r.code, r.stderr, output.ExitFailure)
	}
	env := decode(t, r.stdout)
	if env.Success || len(env.Errors) != 1 ||
		env.Errors[0].Code != string(output.CodeCommandFailed) ||
		!strings.Contains(env.Errors[0].Message, "the Unity Editor has") {
		t.Errorf("envelope = %+v, want one COMMAND_FAILED error saying the Editor is open", env)
	}
}

// --slots outside 1 to max_slots is refused before init writes anything (A21).
func TestInitSlotsOutsideOneToMaxSlotsFails(t *testing.T) {
	for _, tt := range []struct {
		name     string
		maxSlots string
		slots    string
		want     string
	}{
		{"zero", "", "0", "--slots 0 is not from 1 to max_slots, 3"},
		{"above the default max_slots", "", "4", "--slots 4 is not from 1 to max_slots, 3"},
		{"above a configured max_slots", "1", "2", "--slots 2 is not from 1 to max_slots, 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newInitEnv(t)
			if tt.maxSlots != "" {
				e.Setenv(config.EnvName("max_slots"), tt.maxSlots)
			}
			r := runIn(t, e, e.Repo, "init", "--slots", tt.slots)
			checkRefused(t, e, r, readFile(t, e, ".gitignore"), tt.want)
		})
	}
}

// init --help says what --slots does, and that slots beyond ready become prune candidates after
// shrink_after idle (A21).
func TestInitSlotsHelpSaysWhatBecomesOfThem(t *testing.T) {
	e := newInitEnv(t)
	r := runIn(t, e, e.Repo, "init", "--help")
	if r.code != output.ExitOK || !strings.HasPrefix(r.stdout, "usage: upool init [--slots N] "+
		"[--json]\n") || !strings.Contains(r.stdout, "-slots int") ||
		!strings.Contains(r.stdout, "slots beyond ready") || !strings.Contains(r.stdout,
		"become prune candidates once idle for shrink_after") || r.stderr != "" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and the help naming slots beyond ready",
			r.code, r.stdout, r.stderr)
	}
}

// A project settings file init cannot register the hooks in is refused before anything is
// written, and left as it was.
func TestInitRefusesProjectSettingsItCannotRegisterIn(t *testing.T) {
	e := newInitEnv(t)
	const doc = `{"hooks": ["WorktreeCreate"]}`
	e.WriteFile(projectSettings, doc)
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"), e.Path(projectSettings))
	if got := readFile(t, e, projectSettings); got != doc {
		t.Errorf("%s = %q, want it unchanged: %q", projectSettings, got, doc)
	}
}

// Without this binary on PATH there is no path for the hooks to run that survives an upgrade, so
// init refuses before writing anything.
func TestInitRefusesWhenThisBinaryIsNotOnPath(t *testing.T) {
	e := newInitEnv(t)
	dirs := filepath.SplitList(e.Getenv("PATH"))
	e.Setenv("PATH", strings.Join(dirs[:len(dirs)-1], string(os.PathListSeparator)))
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"), "no upool on PATH is this binary")
}

// An AGENTS.md whose upool markers do not pair is refused before anything is written, as a
// CLAUDE.md's is.
func TestInitRefusesAnAGENTSMDWhoseMarkersDoNotPair(t *testing.T) {
	e := newInitEnv(t)
	const doc = "Use tabs.\n<!-- upool:begin v1 -->\nno end marker\n"
	e.WriteFile("AGENTS.md", doc)
	r := runIn(t, e, e.Repo, "init")
	checkRefused(t, e, r, readFile(t, e, ".gitignore"),
		"line 2 begins a upool block that no line after it ends")
	if got := readFile(t, e, "AGENTS.md"); got != doc {
		t.Errorf("AGENTS.md = %q, want it unchanged", got)
	}
}

// A home directory in a folder the desktop app's helper cannot read from, by name in any case or
// through a link, is refused before anything is written: the hooks read upool's state under
// ~/.upool (#66).
func TestInitRefusesAHomeInAProtectedFolder(t *testing.T) {
	for _, home := range []string{"Documents/home", "Desktop/home", "Downloads/home",
		"documents/home", "Documents", "link"} {
		t.Run(home, func(t *testing.T) {
			e := newInitEnv(t)
			root := tempDir(t)
			dir := filepath.Join(root, filepath.FromSlash(home))
			if home == "link" {
				target := filepath.Join(root, "Documents", "home")
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			} else if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			e.Home = dir
			e.Setenv("HOME", dir)
			r := runIn(t, e, e.Repo, "init")
			checkRefused(t, e, r, readFile(t, e, ".gitignore"),
				"which the desktop app's hook helper cannot read from")
		})
	}
}

// upool setup is gone: init registers the hooks and installs the rest per project (A26).
func TestSetupIsGone(t *testing.T) {
	e := newInitEnv(t)
	r := runIn(t, e, e.Repo, "setup")
	if r.code != output.ExitFailure || !strings.Contains(r.stderr, "usage: upool") ||
		strings.Contains(r.stderr, "upool setup") {
		t.Errorf("exit %d, stderr %q; want usage, without setup, and a failure", r.code, r.stderr)
	}
}
