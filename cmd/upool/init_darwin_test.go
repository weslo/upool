//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
)

// The tests of an init that succeeds, and so clones the first slot with `cp -cR`: macOS only, as
// the clone recipe is (CONTRIBUTING.md, "Build and test"). The refusals are in init_test.go.

// mustInit runs `upool init` in e's repository and fails the test unless it exits 0.
func mustInit(t *testing.T, e *testenv.Env, args ...string) result {
	t.Helper()
	r := runIn(t, e, e.Repo, append([]string{"init"}, args...)...)
	if r.code != output.ExitOK {
		t.Fatalf("init: exit = %d, want %d; stderr %q", r.code, output.ExitOK, r.stderr)
	}
	return r
}

// pathUpool is the upool newInitEnv puts at the end of e's PATH, the path init registers the hooks
// to run upool by.
func pathUpool(e *testenv.Env) string {
	dirs := filepath.SplitList(e.Getenv("PATH"))
	return filepath.Join(dirs[len(dirs)-1], upoolName)
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

// slotPath is where init clones the first slot of e's repository.
func slotPath(e *testenv.Env) string {
	return e.Path(".claude/worktrees/slot-1")
}

// gitIn runs `git -C dir args…` with e's environment, fails the test unless it exits 0, and
// returns its stdout with surrounding space trimmed.
func gitIn(t *testing.T, e *testenv.Env, dir string, args ...string) string {
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

// checkPool fails the test unless e's pool has state.json with repo and base, and one slot,
// slot-1, which init cloned from the main checkout at e.Head: ready, with no owner or lease, and
// in git at base with nothing to commit (docs/contracts.md (f) transitions 1 and 2).
func checkPool(t *testing.T, e *testenv.Env, base string) {
	t.Helper()
	p, err := pool.Load(e.Home, repoKey(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if p.SchemaVersion != pool.SchemaVersion || p.Repo != e.Repo || p.Base != base ||
		len(p.Slots) != 1 || len(p.AgentWorktrees) != 0 || p.RefreshJob != nil {
		t.Fatalf("state.json = %+v, want schema_version %d, repo %s, base %s, slot-1 and "+
			"nothing else", p, pool.SchemaVersion, e.Repo, base)
	}
	s := p.Slots[0]
	if s.Name != "slot-1" || s.Path != slotPath(e) || s.State != pool.StateReady ||
		s.OwnerPID != nil || s.OwnerPIDStart != nil || s.SeedHead != e.Head || s.Lease != nil {
		t.Errorf("slot = %+v, want slot-1 at %s, ready, with no owner or lease, and seed_head %s",
			s, slotPath(e), e.Head)
	}
	if got, want := gitIn(t, e, s.Path, "rev-parse", "HEAD"),
		e.Git("rev-parse", base+"^{commit}"); got != want {
		t.Errorf("slot-1 HEAD = %s, want base %s at %s", got, base, want)
	}
	if status := gitIn(t, e, s.Path, "status", "--porcelain"); status != "" {
		t.Errorf("slot-1 status = %q, want nothing", status)
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
		"added upool's block to CLAUDE.md\n" +
		"added upool's block to AGENTS.md\n" +
		"created " + e.Path(skillFiles[0]) + "\n" +
		"created " + e.Path(skillFiles[1]) + "\n" +
		"cloned slot-1 into " + slotPath(e) + "\n" +
		"registered " + e.Repo + " as pool " + key + "\n" +
		"the hooks run " + pathUpool(e) + "\n" +
		"updated " + e.Path(projectSettings) + "\n"
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
	// init's own .gitignore line and .upool.toml leave the main checkout dirty, so the slot
	// records that and does not keep them ((g) step 7).
	p, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Slots[0].SeedDirty {
		t.Error("seed_dirty = false, want true: init's own edits leave the main checkout dirty")
	}
	if _, err := os.Stat(filepath.Join(slotPath(e), ".upool.toml")); !errors.Is(err,
		fs.ErrNotExist) {
		t.Errorf("slot-1/.upool.toml: stat error = %v, want it reverted with the rest", err)
	}
	if _, err := os.Stat(filepath.Join(slotPath(e), "CLAUDE.md")); err != nil {
		t.Errorf("slot-1/CLAUDE.md: %v, want the gitignored agent files carried", err)
	}
}

// init upserts upool's block into the repository's CLAUDE.md, leaving the text around it as it
// was, and the first slot carries the file, which the environment's .gitignore ignores.
func TestInitWritesBlock(t *testing.T) {
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	current := string(b.Render())
	stale := block.Begin(b.Version) + "\nA text upool no longer writes.\n" +
		block.End(b.Version) + "\n"
	newer := string(block.Block{Version: b.Version + 1, Body: "A newer text.\n"}.Render())
	const before, after = "# Fake project\n\n", "\n## Testing\n\nRun the tests.\n"
	// write returns a setup that writes doc as CLAUDE.md.
	write := func(doc string) func(*testing.T, *testenv.Env) {
		return func(_ *testing.T, e *testenv.Env) { e.WriteFile("CLAUDE.md", doc) }
	}
	tests := []struct {
		name  string
		setup func(*testing.T, *testenv.Env)
		want  func(original string) string // CLAUDE.md after init, given it before
		line  string
	}{
		{"after the file's text", func(*testing.T, *testenv.Env) {},
			func(o string) string { return o + "\n" + current },
			"added upool's block to CLAUDE.md"},
		{"a missing file", func(t *testing.T, e *testenv.Env) {
			if err := os.Remove(e.Path("CLAUDE.md")); err != nil {
				t.Fatal(err)
			}
		}, func(string) string { return current }, "created CLAUDE.md with upool's block"},
		{"a stale copy, in place", write(before + stale + after),
			func(string) string { return before + current + after },
			"updated upool's block in CLAUDE.md to v1"},
		{"the current copy, kept", write(before + current + after),
			func(o string) string { return o }, "CLAUDE.md already has upool's block"},
		{"a newer copy, kept", write(before + newer + after),
			func(o string) string { return o },
			"kept the newer upool block in CLAUDE.md: this upool writes v1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newInitEnv(t)
			tt.setup(t, e)
			want := tt.want(readFile(t, e, "CLAUDE.md"))
			r := mustInit(t, e)
			if !strings.Contains(r.stdout, "\n"+tt.line+"\n") {
				t.Errorf("stdout = %q, want the line %q", r.stdout, tt.line)
			}
			if got := readFile(t, e, "CLAUDE.md"); got != want {
				t.Errorf("CLAUDE.md =\n%s\nwant\n%s", got, want)
			}
			slotFile, err := os.ReadFile(filepath.Join(slotPath(e), "CLAUDE.md"))
			if err != nil || string(slotFile) != want {
				t.Errorf("slot-1/CLAUDE.md = %q, %v, want the main checkout's: %q", slotFile, err,
					want)
			}
		})
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
	claude := readFile(t, e, "CLAUDE.md")
	r := mustInit(t, e)
	want := e.Repo + " is already registered as pool " + repoKey(t, e) + "\n" +
		"CLAUDE.md already has upool's block\n" + "AGENTS.md already has upool's block\n" +
		e.Path(skillFiles[0]) + " is up to date\n" + e.Path(skillFiles[1]) + " is up to date\n" +
		"the hooks run " + pathUpool(e) + "\n" + e.Path(projectSettings) + " is up to date\n"
	if r.stdout != want {
		t.Errorf("stdout = %q, want %q", r.stdout, want)
	}
	if got := readFile(t, e, ".gitignore"); got != gitignore {
		t.Errorf(".gitignore = %q, want it unchanged: %q", got, gitignore)
	}
	if got := readFile(t, e, ".upool.toml"); got != repoFile {
		t.Errorf(".upool.toml = %q, want it unchanged: %q", got, repoFile)
	}
	if got := readFile(t, e, "CLAUDE.md"); got != claude {
		t.Errorf("CLAUDE.md = %q, want it unchanged: %q", got, claude)
	}
}

// Uncommitted changes are not staleness: the clone reverts what the seed left ((g) step 7), which
// checkPool sees as a clean slot.
func TestInitAcceptsADirtyCheckoutWithACurrentLibrary(t *testing.T) {
	e := newInitEnv(t, testenv.UncommittedChanges())
	mustInit(t, e)
	checkPool(t, e, "main")
}

// addOrigin gives e's repository an origin, a bare repository beside it whose default branch is
// trunk, holding main's commit, fetched so that refs/remotes/origin/HEAD is origin/trunk, and a
// local trunk, as cloning the origin would have made: the first slot is cloned at base.
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
	e.Git("branch", "-q", "trunk", "origin/trunk")
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
			e.Git("branch", "release")
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
		"claude_md_block":    "inserted",
		"agents_md_block":    "inserted",
		"skills": []any{map[string]any{"path": e.Path(skillFiles[0]), "change": "created"},
			map[string]any{"path": e.Path(skillFiles[1]), "change": "created"}},
		"upool": pathUpool(e),
		"hooks": map[string]any{"path": e.Path(projectSettings), "change": "updated"},
		"slots": []any{map[string]any{
			"name":       "slot-1",
			"path":       slotPath(e),
			"seed":       e.Repo,
			"seed_head":  e.Head,
			"seed_dirty": true,
		}},
	}
	if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
		t.Errorf("data = %s, want %s", mustJSON(t, got), mustJSON(t, want))
	}

	r = mustInit(t, e, "--json")
	env = decode(t, r.stdout)
	data, _ = json.Marshal(env.Data) // as above
	want = map[string]any{"repo": e.Repo, "key": repoKey(t, e), "already_registered": true,
		"gitignore_updated": false, "repo_file_written": false, "upool": pathUpool(e),
		"hooks":           map[string]any{"path": e.Path(projectSettings), "change": "unchanged"},
		"claude_md_block": "unchanged", "agents_md_block": "unchanged",
		"skills": []any{map[string]any{"path": e.Path(skillFiles[0]), "change": "unchanged"},
			map[string]any{"path": e.Path(skillFiles[1]), "change": "unchanged"}}}
	if string(data) != string(mustJSON(t, want)) {
		t.Errorf("data when already registered = %s, want %s", data, mustJSON(t, want))
	}
}

// After registry.json is lost, init finds the pool again by its key, keeps its state, and clones
// no second slot: only a pool with no slots gets a first one.
func TestInitKeepsTheStateOfAPoolWhoseRegistryWasLost(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	key := repoKey(t, e)
	before, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.Home, ".upool", "registry.json")); err != nil {
		t.Fatal(err)
	}
	r := mustInit(t, e)
	want := "kept the pool's existing slots: slot-1 (ready)\n" +
		"registered " + e.Repo + " as pool " + key + "\n" +
		"the hooks run " + pathUpool(e) + "\n" + e.Path(projectSettings) + " is up to date\n"
	if !strings.HasSuffix(r.stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", r.stdout, want)
	}
	after, err := pool.Load(e.Home, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, after)) != string(mustJSON(t, before)) {
		t.Errorf("state.json = %+v, want it unchanged: %+v", after, before)
	}
	if _, err := os.Stat(e.Path(".claude/worktrees/slot-2")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("slot-2: stat error = %v, want it not to exist", err)
	}
}

// init --slots 2 clones slot-1 from the main checkout and slot-2 seeded from slot-1, both ready
// and owned by nothing, and writes nothing about it to .upool.toml (A21).
func TestInitSlotsClonesTwoReadySlots(t *testing.T) {
	e := newInitEnv(t)
	r := mustInit(t, e, "--slots", "2", "--json")
	env := decode(t, r.stdout)
	data, _ := json.Marshal(env.Data) // it was decoded from JSON, so it encodes
	var got initReport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	slot2 := e.Path(".claude/worktrees/slot-2")
	if len(got.Slots) != 2 || got.Slots[0].Seed != e.Repo || got.Slots[1].Path != slot2 ||
		got.Slots[1].Seed != slotPath(e) {
		t.Errorf("slots = %+v, want slot-1 from the main checkout and slot-2 from slot-1",
			got.Slots)
	}
	p, err := pool.Load(e.Home, repoKey(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Slots) != 2 {
		t.Fatalf("state.json slots = %+v, want two", p.Slots)
	}
	for _, s := range p.Slots {
		if s.State != pool.StateReady || s.OwnerPID != nil || s.Lease != nil {
			t.Errorf("%s = %s, owner %v, lease %+v; want ready, unowned, unleased", s.Name,
				s.State, s.OwnerPID, s.Lease)
		}
		if status := gitIn(t, e, s.Path, "status", "--porcelain"); status != "" {
			t.Errorf("%s status = %q, want clean", s.Name, status)
		}
	}
	if _, err := os.Stat(filepath.Join(slot2, "Library", "ScriptAssemblies")); err != nil {
		t.Errorf("slot-2 has no warm Library: %v", err)
	}
	if got := readFile(t, e, ".upool.toml"); !strings.Contains(got, "ready = 1\n") {
		t.Errorf(".upool.toml = %q, want ready left at 1", got)
	}

	q := newInitEnv(t)
	r = mustInit(t, q, "--slots", "2")
	if want := "cloned slot-2 into " + q.Path(".claude/worktrees/slot-2") +
		", seeded from slot-1\n"; !strings.Contains(r.stdout, want) {
		t.Errorf("stdout = %q, want the line %q", r.stdout, want)
	}
}

// The acceptance test of #173: init registers upool's hooks in the project's
// .claude/settings.local.json, keeping its other keys and another tool's hooks, compared as JSON;
// upool's three handlers appear once after two runs; no copy of the file is kept beside it; and
// ~/.claude/settings.json is neither created nor changed (A26).
func TestInitRegistersHooks(t *testing.T) {
	e := newInitEnv(t)
	e.WriteFile(projectSettings, otherSettings)
	mustInit(t, e)
	mustInit(t, e)
	b, err := os.ReadFile(e.Path(projectSettings))
	if err != nil {
		t.Fatal(err)
	}
	want := upoolEntries(t, pathUpool(e), `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "model": "opus",
  "cleanupPeriodDays": 30,
  "permissions": {"allow": ["Bash(git status)"], "deny": []},
  "env": {"OTHER_TOOL": "<on> & off"},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-tool guard"}]}
    ],
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start", "timeout": 30}]},
      "@session-start@"
    ],
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "/opt/other/worktree.sh"}]},
      "@worktree-create@"
    ],
    "WorktreeRemove": ["@worktree-remove@"]
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}
}`)
	if !sameJSON(b, []byte(want)) {
		t.Errorf("%s =\n%s\nwant, as JSON,\n%s", projectSettings, b, want)
	}
	for _, path := range []string{e.Path(projectSettings + ".upool-backup"),
		filepath.Join(e.Home, ".claude", "settings.json")} {
		if _, err := os.Lstat(path); err == nil {
			t.Errorf("%s exists, want init to leave it unmade", path)
		}
	}
}

// The acceptance test of #174: init installs both copies of the skill in the project and one
// block in each of its CLAUDE.md and AGENTS.md, keeping their own text, and a later init rewrites
// a copy of the skill that differs, as an upgraded upool's would; nothing is written under
// ~/.claude, ~/.agents or ~/.codex (A26).
func TestInitInstallsSkillAndBlocks(t *testing.T) {
	e := newInitEnv(t)
	originals := map[string]string{"CLAUDE.md": readFile(t, e, "CLAUDE.md"),
		"AGENTS.md": readFile(t, e, "AGENTS.md")}
	mustInit(t, e)
	e.WriteFile(skillFiles[1], "an older skill\n")
	r := mustInit(t, e)
	if want := "updated " + e.Path(skillFiles[1]) + "\n"; !strings.Contains(r.stdout, want) {
		t.Errorf("second init: stdout = %q, want a line %q", r.stdout, want)
	}
	for _, name := range skillFiles {
		if got := readFile(t, e, name); got != assets.Skill {
			t.Errorf("%s = %q, want the skill", name, got)
		}
		checkMode(t, e.Path(name), skillMode)
	}
	rendered := string(block.Block{Version: assets.ClaudeMDBlockVersion,
		Body: assets.ClaudeMDBlock}.Render())
	for name, original := range originals {
		got := readFile(t, e, name)
		if n := strings.Count(got, block.Begin(assets.ClaudeMDBlockVersion)); n != 1 {
			t.Errorf("%s has %d upool blocks, want 1", name, n)
		}
		if !strings.HasPrefix(got, original) || !strings.Contains(got, rendered) {
			t.Errorf("%s = %q, want its own text and then the block", name, got)
		}
	}
	for _, dir := range []string{".claude", ".agents", ".codex"} {
		if _, err := os.Lstat(filepath.Join(e.Home, dir)); err == nil {
			t.Errorf("~/%s exists, want init to write nothing under the home directory's", dir)
		}
	}
}
