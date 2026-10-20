package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// newInitEnv is a testenv environment for init. init loads every setting, and off darwin config
// cannot default editor_ram_budget_gb from the machine's RAM (config.PhysicalMemory), so the
// environment sets it; init does not use it.
func newInitEnv(t *testing.T, opts ...testenv.Option) *testenv.Env {
	t.Helper()
	e := testenv.New(t, opts...)
	e.Setenv(config.EnvName("editor_ram_budget_gb"), "16")
	return e
}

// runIn runs upool with args in e, with dir as the working directory: the real command table,
// e's environment, and the test clock.
func runIn(t *testing.T, e *testenv.Env, dir string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	d := dispatcher{
		commands: commandTable,
		hooks:    hookTable,
		stdin:    strings.NewReader(""),
		stdout:   &out,
		stderr:   &errOut,
		getenv:   e.Getenv,
		environ:  e.Environ,
		getwd:    func() (string, error) { return dir, nil },
		now:      func() time.Time { return clock },
	}
	code := d.run(args)
	return result{code, out.String(), errOut.String()}
}

// mustInit runs `upool init` in e's repository and fails the test unless it exits 0.
func mustInit(t *testing.T, e *testenv.Env, args ...string) result {
	t.Helper()
	r := runIn(t, e, e.Repo, append([]string{"init"}, args...)...)
	if r.code != output.ExitOK {
		t.Fatalf("init: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	return r
}

// repoKey is the <repo-key> init gives e's repository.
func repoKey(t *testing.T, e *testenv.Env) string {
	t.Helper()
	canonical, err := paths.Key(e.Repo)
	if err != nil {
		t.Fatal(err)
	}
	return registry.Key(canonical)
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

// checkPool fails the test unless e's pool has state.json with repo, base and no slots.
func checkPool(t *testing.T, e *testenv.Env, base string) {
	t.Helper()
	p, err := pool.Load(e.Home, repoKey(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != pool.SchemaVersion || p.Repo != e.Repo || p.Base != base ||
		len(p.Slots) != 0 || len(p.AgentWorktrees) != 0 || p.RefreshJob != nil {
		t.Errorf("state.json = %+v, want schema_version %d, repo %s, base %s and nothing else",
			p, pool.SchemaVersion, e.Repo, base)
	}
}

// checkRefused fails the test unless r is a refusal whose message contains each of want, and init
// wrote nothing: no ~/.upool, no .upool.toml, and .gitignore as it was.
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
}

func TestInitRegistersRepoAndIgnoresWorktrees(t *testing.T) {
	e := newInitEnv(t)
	gitignore := readFile(t, e, ".gitignore")
	r := mustInit(t, e)

	key := repoKey(t, e)
	want := "added .claude/worktrees/ to .gitignore\n" +
		"base is main, the current branch: origin has no default branch\n" +
		"wrote .upool.toml: ready = 1, max_slots = 3, base = \"main\"\n" +
		"registered " + e.Repo + " as pool " + key + "\n"
	if r.stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", r.stdout, want)
	}
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
	if got, want := readFile(t, e, ".gitignore"), gitignore+".claude/worktrees/\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
	// Exits 0 only if git ignores the path.
	e.Git("check-ignore", "-q", ".claude/worktrees/slot-1")
	c, err := config.Load(config.Inputs{Getenv: e.Getenv, Repo: e.Repo, Home: e.Home})
	if err != nil {
		t.Fatal(err)
	}
	if c.Ready != (config.Setting[int]{Value: 1, Source: config.Repo}) ||
		c.MaxSlots != (config.Setting[int]{Value: 3, Source: config.Repo}) ||
		c.Base != (config.Setting[string]{Value: "main", Source: config.Repo}) {
		t.Errorf("settings from .upool.toml: ready %+v, max_slots %+v, base %+v; want 1, 3 and "+
			"main, each from the repo file", c.Ready, c.MaxSlots, c.Base)
	}
	reg, ok, err := registry.Lookup(e.Home, e.Repo)
	if err != nil || !ok || reg.Key != key {
		t.Errorf("registry.Lookup = %+v, %v, %v, want key %s", reg, ok, err, key)
	}
	checkPool(t, e, "main")
	if _, err := os.Stat(e.Path(".claude/worktrees")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(".claude/worktrees: stat error = %v, want it not to exist: init clones no slot",
			err)
	}
}

func TestInitFromASubdirectoryRegistersTheRoot(t *testing.T) {
	e := newInitEnv(t)
	r := runIn(t, e, e.Path("Assets/Scripts"), "init")
	if r.code != output.ExitOK {
		t.Fatalf("exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	if _, ok, err := registry.Lookup(e.Home, e.Repo); err != nil || !ok {
		t.Errorf("registry.Lookup(%s) = %v, %v, want it registered", e.Repo, ok, err)
	}
	checkPool(t, e, "main")
}

func TestInitTwiceReportsAlreadyRegistered(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	gitignore, repoFile := readFile(t, e, ".gitignore"), readFile(t, e, ".upool.toml")
	r := mustInit(t, e)
	if want := e.Repo + " is already registered as pool " + repoKey(t, e) + "\n"; r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if got := readFile(t, e, ".gitignore"); got != gitignore {
		t.Errorf(".gitignore = %q, want it unchanged: %q", got, gitignore)
	}
	if got := readFile(t, e, ".upool.toml"); got != repoFile {
		t.Errorf(".upool.toml = %q, want it unchanged: %q", got, repoFile)
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

// Uncommitted changes are not staleness: the clone reverts what the seed left ((g) step 7).
func TestInitAcceptsADirtyCheckoutWithACurrentLibrary(t *testing.T) {
	e := newInitEnv(t, testenv.UncommittedChanges())
	mustInit(t, e)
	checkPool(t, e, "main")
}

// addOrigin gives e's repository an origin, a bare repository beside it whose default branch is
// trunk, holding main's commit, fetched so that refs/remotes/origin/HEAD is origin/trunk.
func addOrigin(t *testing.T, e *testenv.Env) {
	t.Helper()
	origin := filepath.Join(filepath.Dir(e.Repo), "origin.git")
	e.Git("init", "-q", "--bare", "-b", "trunk", origin)
	e.Git("remote", "add", "origin", origin)
	e.Git("push", "-q", "origin", testenv.Base+":trunk")
	e.Git("fetch", "-q", "origin")
	e.Git("remote", "set-head", "origin", "--auto")
	if got := e.Git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); got != "origin/trunk" {
		t.Fatalf("origin/HEAD = %q, want origin/trunk", got)
	}
}

func TestInitDefaultsBaseToOriginHead(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) *testenv.Env
		base, line string
	}{
		{"origin's default branch", func(t *testing.T) *testenv.Env {
			e := newInitEnv(t)
			addOrigin(t, e)
			return e
		}, "trunk", "base is trunk, origin's default branch"},
		{"no origin: the current branch", func(t *testing.T) *testenv.Env {
			return newInitEnv(t, testenv.SecondCommit())
		}, testenv.Branch, "base is feature, the current branch: origin has no default branch"},
		{"origin without a default branch: the current branch", func(t *testing.T) *testenv.Env {
			e := newInitEnv(t, testenv.SecondCommit())
			addOrigin(t, e)
			e.Git("remote", "set-head", "origin", "--delete")
			return e
		}, testenv.Branch, "base is feature, the current branch: origin has no default branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.setup(t)
			r := mustInit(t, e)
			if !strings.Contains(r.stdout, tt.line+"\n") {
				t.Errorf("stdout = %q, want the line %q", r.stdout, tt.line)
			}
			if !strings.Contains(readFile(t, e, ".upool.toml"), "base = \""+tt.base+"\"\n") {
				t.Errorf(".upool.toml = %q, want base = %q", readFile(t, e, ".upool.toml"), tt.base)
			}
			checkPool(t, e, tt.base)
		})
	}
}

// A base that a layer sets wins over origin's default branch, and init says where it came from.
func TestInitTakesAConfiguredBase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, e *testenv.Env)
		line  string
	}{
		{"environment", func(_ *testing.T, e *testenv.Env) {
			e.Setenv(config.EnvName("base"), "release")
		}, "base is release, from UPOOL_BASE"},
		{"user file", func(t *testing.T, e *testenv.Env) {
			writeUserConfig(t, e, "base = \"release\"\n")
		}, "base is release, from ~/.config/upool/config.toml"},
		{"repo file", func(_ *testing.T, e *testenv.Env) {
			e.WriteFile(".upool.toml", "base = \"release\"\n")
		}, "base is release, from .upool.toml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newInitEnv(t)
			addOrigin(t, e)
			tt.setup(t, e)
			r := mustInit(t, e)
			if !strings.Contains(r.stdout, tt.line+"\n") {
				t.Errorf("stdout = %q, want the line %q", r.stdout, tt.line)
			}
			checkPool(t, e, "release")
		})
	}
}

// writeUserConfig writes doc as the user file under e's HOME.
func writeUserConfig(t *testing.T, e *testenv.Env, doc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(config.UserPath(e.Home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserPath(e.Home), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
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

// The values in effect go into a new .upool.toml, wherever they were set; an existing one is kept.
func TestInitWritesTheValuesInEffectOnlyToANewRepoFile(t *testing.T) {
	e := newInitEnv(t)
	e.Setenv(config.EnvName("max_slots"), "5")
	writeUserConfig(t, e, "ready = 2\n")
	mustInit(t, e)
	want := "ready = 2\nmax_slots = 5\nbase = \"main\"\n"
	if got := readFile(t, e, ".upool.toml"); !strings.HasSuffix(got, "\n"+want) {
		t.Errorf(".upool.toml = %q, want it to end with %q", got, want)
	}

	e = newInitEnv(t)
	const existing = "ready = 2\n"
	e.WriteFile(".upool.toml", existing)
	r := mustInit(t, e)
	if !strings.Contains(r.stdout, "kept the existing .upool.toml\n") {
		t.Errorf("stdout = %q, want it to say the existing .upool.toml was kept", r.stdout)
	}
	if got := readFile(t, e, ".upool.toml"); got != existing {
		t.Errorf(".upool.toml = %q, want it unchanged: %q", got, existing)
	}
	checkPool(t, e, "main")
}

// A .gitignore line that already covers .claude/worktrees, as the spike's `.claude/` did, is
// enough; and an added line starts on a line of its own.
func TestInitAddsTheIgnoreLineOnlyWhenNeeded(t *testing.T) {
	e := newInitEnv(t)
	gitignore := readFile(t, e, ".gitignore") + ".claude/\n"
	e.WriteFile(".gitignore", gitignore)
	r := mustInit(t, e)
	if !strings.HasPrefix(r.stdout, ".claude/worktrees/ is already ignored\n") {
		t.Errorf("stdout = %q, want it to start by saying .claude/worktrees/ is already ignored",
			r.stdout)
	}
	if got := readFile(t, e, ".gitignore"); got != gitignore {
		t.Errorf(".gitignore = %q, want it unchanged: %q", got, gitignore)
	}

	e = newInitEnv(t)
	e.WriteFile(".gitignore", "/Library/")
	mustInit(t, e)
	if got, want := readFile(t, e, ".gitignore"), "/Library/\n.claude/worktrees/\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
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

func TestInitJSONGivesTheEnvelope(t *testing.T) {
	e := newInitEnv(t)
	r := mustInit(t, e, "--json")
	if r.stderr != "" {
		t.Errorf("stderr = %q, want empty", r.stderr)
	}
	env := decode(t, r.stdout)
	if !env.Success || env.Command != "init" || len(env.Errors) != 0 || len(env.Warnings) != 0 {
		t.Errorf("envelope = %+v, want success for init with no errors or warnings", env)
	}
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"repo":               e.Repo,
		"key":                repoKey(t, e),
		"already_registered": false,
		"base":               "main",
		"base_source":        "current_branch",
		"gitignore_updated":  true,
		"repo_file_written":  true,
	}
	if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
		t.Errorf("data = %s, want %s", mustJSON(t, got), mustJSON(t, want))
	}

	r = mustInit(t, e, "--json")
	env = decode(t, r.stdout)
	data, _ = json.Marshal(env.Data) // as above
	want = map[string]any{"repo": e.Repo, "key": repoKey(t, e), "already_registered": true,
		"gitignore_updated": false, "repo_file_written": false}
	if string(data) != string(mustJSON(t, want)) {
		t.Errorf("data when already registered = %s, want %s", data, mustJSON(t, want))
	}
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

// mustJSON is v as JSON, with map keys sorted, for comparing decoded documents.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// After registry.json is lost, init finds the pool again by its key and keeps its state.
func TestInitKeepsTheStateOfAPoolWhoseRegistryWasLost(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	key := repoKey(t, e)
	slot := pool.Slot{Name: "slot-1", Path: e.Path(".claude/worktrees/slot-1"),
		State: pool.StateReady, LastUsed: clock}
	err := pool.Update(t.Context(), e.Home, key, func(p *pool.Pool) error {
		p.Slots = append(p.Slots, slot)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.Home, ".upool", "registry.json")); err != nil {
		t.Fatal(err)
	}
	r := mustInit(t, e)
	if !strings.HasSuffix(r.stdout, "registered "+e.Repo+" as pool "+key+"\n") {
		t.Errorf("stdout = %q, want it to end by registering the repository again", r.stdout)
	}
	p, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Slots) != 1 || p.Slots[0].Name != "slot-1" || p.Repo != e.Repo {
		t.Errorf("state.json = %+v, want the existing pool with slot-1 kept", p)
	}
}
