//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/output"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/registry"
	"github.com/weslo/upool/internal/testenv"
	"github.com/weslo/upool/internal/unity/fake"
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

// init refreshes a stale Library in the main checkout before it writes anything, rather than
// refusing it (#98): it opens the checkout's Editor headless, waits until it is ready, closes it,
// and, the Library current, goes on to clone slot-1 from it. The stand-in Editor brings the Library
// current as it starts.
func TestInitRefreshesAStaleLibrary(t *testing.T) {
	e := newInitEnv(t, testenv.StaleLibrary())
	// Then the clone compares slot-1's cache key across its checkout ((g) step 7).
	u := fake.Install(t, openReply(t, e, "open-headless.json"), statusReady, closeHeadlessReply,
		fake.Reply{Capture: "cache-key.json"}, fake.Reply{Capture: "cache-key.json"})
	e.Setenv("PATH", u.Dir()+string(os.PathListSeparator)+e.Getenv("PATH"))
	built := testenv.StaleTime.Add(time.Hour)
	procs := newRewarmProcs(1, func() {
		e.SetMtime("Library/ScriptAssemblies/Assembly-CSharp.dll", built)
		e.SetMtime("Library/ScriptAssemblies", built)
	})
	r := runProcs(t, e, e.Repo, procs, func() time.Time { return clock }, "init")
	if r.code != output.ExitOK {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0", r.code, r.stdout, r.stderr)
	}
	want := "upool: the Library in " + e.Repo + " is stale; refreshing it with a headless " +
		"Editor first\n"
	if !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
	wantCalls := [][]string{{"open", e.Repo, "--args", "-batchmode", "--format", "json"},
		{"status", "--until-ready", "--timeout", "120", "--format", "json"},
		{"close", e.Repo, "--force", "--format", "json"}}
	if got := unityArgs(u); len(got) < 3 || !slices.EqualFunc(got[:3], wantCalls, slices.Equal) {
		t.Errorf("unity calls = %q, want open, status and close of the main checkout first", got)
	}
	if got := strings.Join(slotNamesOf(t, e), " "); got != "slot-1=ready" {
		t.Errorf("slots = %s, want slot-1 ready", got)
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

// projectFiles is what repoFiles reads in e's repository, without what init writes there itself:
// .gitignore, .upool.toml, the hooks' settings and the slots.
func projectFiles(t *testing.T, e *testenv.Env) map[string]string {
	t.Helper()
	files := repoFiles(t, e.Repo)
	for rel := range files {
		if rel == ".gitignore" || rel == ".upool.toml" || rel == projectSettings ||
			strings.HasPrefix(rel, ".claude/worktrees/") {
			delete(files, rel)
		}
	}
	return files
}

// checkProjectFiles fails the test unless projectFiles reads before in e's repository.
func checkProjectFiles(t *testing.T, e *testenv.Env, before map[string]string) {
	t.Helper()
	after := projectFiles(t, e)
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if after[k] != before[k] {
			t.Errorf("%s = %q, want it as it was: %q", k, after[k], before[k])
		}
	}
	for _, k := range slices.Sorted(maps.Keys(after)) {
		if _, ok := before[k]; !ok {
			t.Errorf("%s appeared: %q", k, after[k])
		}
	}
}

// earlierUpool writes into e's repository what an earlier upool's init wrote: its block, upserted
// into each of CLAUDE.md and AGENTS.md, and the skill at each of skillFiles.
func earlierUpool(t *testing.T, e *testenv.Env) {
	t.Helper()
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	for _, name := range []string{claudeMD, agentsMD} {
		if _, err := block.Upsert(e.Path(name), b); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range skillFiles {
		e.WriteFile(name, assets.Skill)
	}
}

// The acceptance test of #230: init writes nothing into the project's agent files. Run twice in
// the harness project, it leaves CLAUDE.md, AGENTS.md, .claude/skills/ and .agents/skills/, and
// every other file but its own, byte for byte as they were, and writes nothing under ~/.claude,
// ~/.agents or ~/.codex (A43).
func TestInitLeavesTheAgentFilesAlone(t *testing.T) {
	e := newInitEnv(t)
	before := projectFiles(t, e)
	for _, name := range []string{claudeMD, agentsMD, testenv.SkillLink,
		".agents/skills/x/SKILL.md"} {
		if _, ok := before[name]; !ok {
			t.Fatalf("the harness project has no %s, so the test shows nothing", name)
		}
	}
	mustInit(t, e)
	mustInit(t, e)
	checkProjectFiles(t, e, before)
	for _, dir := range []string{".claude", ".agents", ".codex"} {
		if _, err := os.Lstat(filepath.Join(e.Home, dir)); err == nil {
			t.Errorf("~/%s exists, want init to write nothing under the home directory's", dir)
		}
	}
}

// The acceptance test of #230: in a project carrying an earlier upool's block and skill in files
// git does not track, init removes exactly those. CLAUDE.md is left with its own text, an
// AGENTS.md the block alone filled is deleted, and each skill goes with its directory (A43).
func TestInitRemovesAnEarlierUpoolsAgentFiles(t *testing.T) {
	e := newInitEnv(t)
	if err := os.Remove(e.Path(agentsMD)); err != nil {
		t.Fatal(err)
	}
	before, claude := projectFiles(t, e), readFile(t, e, claudeMD)
	earlierUpool(t, e)
	r := mustInit(t, e)
	checkProjectFiles(t, e, before)
	for _, line := range []string{"removed upool's block from " + e.Path(claudeMD),
		"removed upool's block from " + e.Path(agentsMD),
		"removed the upool skill, " + e.Path(skillFiles[0]),
		"removed the upool skill, " + e.Path(skillFiles[1])} {
		if want := line + ", which upool no longer writes\n"; !strings.Contains(r.stdout, want) {
			t.Errorf("stdout = %q, want the line %q", r.stdout, want)
		}
	}
	for _, name := range skillFiles {
		dir := filepath.Dir(e.Path(name))
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %v, want the skill's directory gone", dir, err)
		}
	}
	slot := filepath.Join(slotPath(e), claudeMD)
	if b, err := os.ReadFile(slot); err != nil || string(b) != claude {
		t.Errorf("%s = %q, %v; want the main checkout's without upool's block: slot-1 is cloned "+
			"after init removes it", slot, b, err)
	}
}

// init removes a SKILL.md only when an upool wrote it, and a skill's directory only when nothing
// else is in it (#230).
func TestInitRemovesOnlyUpoolsSkill(t *testing.T) {
	e := newInitEnv(t)
	const theirs = "---\nname: pool-notes\ndescription: The project's own notes.\n---\n"
	e.WriteFile(skillFiles[0], assets.Skill)
	e.WriteFile(".claude/skills/upool/notes.md", "notes\n")
	e.WriteFile(skillFiles[1], theirs)
	mustInit(t, e)
	if _, err := os.Lstat(e.Path(skillFiles[0])); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %v, want upool's skill removed", skillFiles[0], err)
	}
	if got := readFile(t, e, ".claude/skills/upool/notes.md"); got != "notes\n" {
		t.Errorf("notes.md = %q, want it and its directory kept", got)
	}
	if got := readFile(t, e, skillFiles[1]); got != theirs {
		t.Errorf("%s = %q, want the project's own skill kept: %q", skillFiles[1], got, theirs)
	}
}

// The acceptance test of #230: init writes into no file git tracks. A block in a tracked CLAUDE.md
// and a skill in a tracked SKILL.md stay byte for byte, the latter also through a link git does
// not track, and init warns, naming each file (A43).
func TestInitWarnsOfAnEarlierUpoolsTrackedFiles(t *testing.T) {
	e := newInitEnv(t)
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	if _, err := block.Upsert(e.Path(claudeMD), b); err != nil {
		t.Fatal(err)
	}
	e.WriteFile(skillFiles[1], assets.Skill)
	e.Git("add", "-f", claudeMD, skillFiles[1])
	e.Git("commit", "-q", "-m", "an earlier upool's files")
	if err := os.Symlink("../../.agents/skills/upool", e.Path(".claude/skills/upool")); err != nil {
		t.Fatal(err)
	}
	before := projectFiles(t, e)
	r := mustInit(t, e)
	checkProjectFiles(t, e, before)
	for _, w := range []string{trackedWarning(e.Path(claudeMD), "the block"),
		trackedWarning(e.Path(skillFiles[0]), "the upool skill"),
		trackedWarning(e.Path(skillFiles[1]), "the upool skill")} {
		if !strings.Contains(r.stderr, "upool: warning: "+w+"\n") {
			t.Errorf("stderr = %q, want the warning %q", r.stderr, w)
		}
	}
	if strings.Contains(r.stdout, "removed") {
		t.Errorf("stdout = %q, want nothing removed", r.stdout)
	}
}

// The upgrade path of #230: run again in a project it registered, init removes what an earlier
// upool wrote there, as a first run does, and warns of what it leaves in a tracked file.
func TestInitAgainRemovesAnEarlierUpoolsAgentFiles(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	if _, err := block.Upsert(e.Path(claudeMD), b); err != nil {
		t.Fatal(err)
	}
	e.Git("add", "-f", claudeMD)
	e.Git("commit", "-q", "-m", "an earlier upool's CLAUDE.md")
	before := projectFiles(t, e)
	if _, err := block.Upsert(e.Path(agentsMD), b); err != nil {
		t.Fatal(err)
	}
	for _, name := range skillFiles {
		e.WriteFile(name, assets.Skill)
	}
	r := mustInit(t, e)
	checkProjectFiles(t, e, before)
	if !strings.HasPrefix(r.stdout, e.Repo+" is already registered as pool ") {
		t.Errorf("stdout = %q, want the repository already registered", r.stdout)
	}
	for _, line := range []string{"removed upool's block from " + e.Path(agentsMD),
		"removed the upool skill, " + e.Path(skillFiles[0]),
		"removed the upool skill, " + e.Path(skillFiles[1])} {
		if want := line + ", which upool no longer writes\n"; !strings.Contains(r.stdout, want) {
			t.Errorf("stdout = %q, want the line %q", r.stdout, want)
		}
	}
	if w := trackedWarning(e.Path(claudeMD), "the block"); !strings.Contains(r.stderr,
		"upool: warning: "+w+"\n") {
		t.Errorf("stderr = %q, want the warning %q", r.stderr, w)
	}
}

// git matches a pathspec by its case and APFS opens a file in any: a block an earlier upool wrote
// into AGENTS.md, which opened the project's tracked agents.md, stays, with a warning (#230).
func TestInitWarnsOfATrackedFileInAnotherCase(t *testing.T) {
	e := newInitEnv(t)
	if err := os.Remove(e.Path(agentsMD)); err != nil {
		t.Fatal(err)
	}
	e.WriteFile("agents.md", "# Fake project\n\nIts own guidance.\n")
	if _, err := os.Stat(e.Path(agentsMD)); err != nil {
		t.Skipf("the volume is case-sensitive, so AGENTS.md is not agents.md: %v", err)
	}
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	if _, err := block.Upsert(e.Path(agentsMD), b); err != nil {
		t.Fatal(err)
	}
	e.Git("add", "-f", "agents.md")
	e.Git("commit", "-q", "-m", "an earlier upool's block")
	before := projectFiles(t, e)
	r := mustInit(t, e)
	checkProjectFiles(t, e, before)
	if w := trackedWarning(e.Path(agentsMD), "the block"); !strings.Contains(r.stderr,
		"upool: warning: "+w+"\n") {
		t.Errorf("stderr = %q, want the warning %q", r.stderr, w)
	}
}

// A link at a skill's directory is the project's own: init removes upool's SKILL.md through it and
// keeps the link, tracked or not, removing the directory it resolves to only when nothing else is
// in it (#230).
func TestInitKeepsALinkedSkillDirectory(t *testing.T) {
	const target = "../../.agents/skills/upool"
	for _, tt := range []struct {
		name           string
		tracked, notes bool
	}{
		{"a tracked link", true, false},
		{"a link to a directory with another file", false, true},
		{"a link to a directory with nothing else", false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newInitEnv(t)
			e.WriteFile(skillFiles[1], assets.Skill)
			if tt.notes {
				e.WriteFile(".agents/skills/upool/notes.md", "notes\n")
			}
			link := e.Path(filepath.Dir(skillFiles[0]))
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if tt.tracked {
				e.Git("add", "-f", filepath.Dir(skillFiles[0]))
				e.Git("commit", "-q", "-m", "the project's link")
			}
			mustInit(t, e)
			if got, err := os.Readlink(link); err != nil || got != target {
				t.Errorf("%s -> %q, %v; want the link kept", link, got, err)
			}
			if _, err := os.Lstat(e.Path(skillFiles[1])); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s: %v, want upool's skill removed", skillFiles[1], err)
			}
			dir := e.Path(filepath.Dir(skillFiles[1]))
			if _, err := os.Lstat(dir); tt.notes != (err == nil) {
				t.Errorf("%s: %v, want it kept only when another file is in it", dir, err)
			}
			if tt.notes && readFile(t, e, ".agents/skills/upool/notes.md") != "notes\n" {
				t.Error("notes.md changed, want it kept")
			}
			if status := gitIn(t, e, e.Repo, "status", "--porcelain", "--",
				filepath.Dir(skillFiles[0])); status != "" {
				t.Errorf("git status of the link = %q, want nothing", status)
			}
		})
	}
}

// A CLAUDE.md or an AGENTS.md whose upool markers do not pair is left as it is, with a warning
// naming it, rather than guessed at; init writes neither, so it goes on (internal/block; #230).
func TestInitLeavesAFileWhoseMarkersDoNotPair(t *testing.T) {
	e := newInitEnv(t)
	claude := "# Fake project\n\n" + block.Begin(1) + "\nA block with no end.\n"
	const agents = "Use tabs.\n<!-- upool:begin v1 -->\nno end marker\n"
	e.WriteFile(claudeMD, claude)
	e.WriteFile(agentsMD, agents)
	r := mustInit(t, e)
	for name, doc := range map[string]string{claudeMD: claude, agentsMD: agents} {
		if got := readFile(t, e, name); got != doc {
			t.Errorf("%s = %q, want it unchanged: %q", name, got, doc)
		}
	}
	for _, w := range []string{"left " + e.Path(claudeMD) + " as it is, since its upool " +
		"markers do not pair: line 3 begins a upool block that no line after it ends",
		"left " + e.Path(agentsMD) + " as it is, since its upool markers do not pair: line 2 " +
			"begins a upool block that no line after it ends"} {
		if !strings.Contains(r.stderr, w) {
			t.Errorf("stderr = %q, want the warning %q", r.stderr, w)
		}
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
		"upool":              pathUpool(e),
		"hooks":              map[string]any{"path": e.Path(projectSettings), "change": "updated"},
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
		"hooks": map[string]any{"path": e.Path(projectSettings), "change": "unchanged"}}
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
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-tool guard"}]},
      "@pre-tool-use@"
    ],
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "other-tool start", "timeout": 30}]},
      "@session-start@"
    ],
    "WorktreeCreate": [
      {"hooks": [{"type": "command", "command": "/opt/other/worktree.sh"}]},
      "@worktree-create@"
    ],
    "WorktreeRemove": ["@worktree-remove@"],
    "SubagentStart": ["@subagent-start@"]
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

// The acceptance test of #88: init runs none of Unity's VCS set-up (A35). With a fake unity on
// PATH, init's calls, the cache key comparison around its slot's checkout, hold no vcs call.
func TestInitRunsNoVCSSetUp(t *testing.T) {
	e := newInitEnv(t)
	u := fake.Install(t, slices.Repeat([]fake.Reply{{Capture: "cache-key.json"}}, 4)...)
	e.Setenv("PATH", u.Dir()+string(os.PathListSeparator)+e.Getenv("PATH"))
	mustInit(t, e)
	calls := u.Calls()
	if len(calls) == 0 {
		t.Fatal("init made no unity call, so the fake shows nothing")
	}
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "vcs" {
			t.Errorf("init ran unity %v", c.Args)
		}
	}
}

// init registers upool's SubagentStart hook in the project's settings, by the path PATH finds
// upool at, as it registers the other hooks (A43; #229).
func TestInitRegistersSubagentStart(t *testing.T) {
	e := newInitEnv(t)
	mustInit(t, e)
	var settings struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, e, projectSettings)), &settings); err != nil {
		t.Fatal(err)
	}
	want := []hookGroup{{Hooks: []commandHook{{Type: "command", Command: pathUpool(e),
		Args: []string{"hook", "subagent-start"}}}}}
	if got := settings.Hooks["SubagentStart"]; !reflect.DeepEqual(got, want) {
		t.Errorf("SubagentStart = %+v, want %+v", got, want)
	}
}
