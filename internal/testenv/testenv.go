// Package testenv is the harness later tests run upool in: a temporary git repository holding a
// fake Unity project, under a temporary HOME, with nothing taken from this machine but git, the
// one command on its PATH, so that no test reaches a real Unity CLI (CONTRIBUTING.md, "Testing":
// isolated from this machine, real git, temporary paths resolved with filepath.EvalSymlinks).
//
// The project has what the clone recipe and the staleness check read (docs/contracts.md (g) Clone
// recipe; docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on
// handout, reset without `-x`"): tracked scripts under Assets/Scripts, ProjectSettings with a
// scriptingDefineSymbols line, a gitignored Library whose ScriptAssemblies are newer than every
// script, and the gitignored agent files the clone must carry, including the relative
// .claude/skills link the spike found (spike §10). Options turn that into the states later tests
// need: a stale Library, an open Editor, uncommitted changes, and HEAD one commit past base.
//
// Scripts and ScriptAssemblies get fixed mtimes, ScriptTime and LibraryTime, so staleness is
// decided by the options alone and never by when a test wrote a file.
package testenv

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weslo/upool/internal/git"
)

// EditorVersion is the project's m_EditorVersion, the version the captures under
// testdata/captures/unity/ report.
const EditorVersion = "6000.4.11f1"

// Base is the branch the repository's first commit is on.
const Base = "main"

// Branch is the branch SecondCommit checks out, one commit past Base.
const Branch = "feature"

// ScriptTime is the mtime of every script the environment writes, and LibraryTime the mtime of
// Library/ScriptAssemblies and what is in it: an hour later, so the Library is current at HEAD.
// StaleLibrary gives one script StaleTime, an hour after LibraryTime.
var (
	ScriptTime  = time.Date(2026, time.October, 12, 21, 0, 0, 0, time.UTC)
	LibraryTime = ScriptTime.Add(time.Hour)
	StaleTime   = LibraryTime.Add(time.Hour)
)

// The project's scripts, relative to the repository.
const (
	// PlayerScript is the script SecondCommit changes and StaleLibrary makes newer than the
	// Library.
	PlayerScript = "Assets/Scripts/Player.cs"
	// GameScript is the tracked script UncommittedChanges edits.
	GameScript = "Assets/Scripts/GameManager.cs"
	// ScratchScript is the untracked script UncommittedChanges adds.
	ScratchScript = "Assets/Scripts/Scratch.cs"
)

// EditorInstanceFile is the file an open Editor holds in its Library, absent after a clean exit
// (spike §2 T1.5). It names the Editor's process as process_id, which clone.EditorOpen reads
// (A45). OpenEditor's names EditorPID, which a test's process table must report as a running Unity
// Editor for the checkout to count as open.
const EditorInstanceFile = "Library/EditorInstance.json"

// EditorPID is the process OpenEditor's EditorInstanceFile names.
const EditorPID = 4242

// SkillLink is the relative link the spike found under .claude/skills, and SkillTarget what it
// points at (spike §10).
const (
	SkillLink   = ".claude/skills/x"
	SkillTarget = "../../.agents/skills/x"
)

// gitignore covers what Unity regenerates (docs/contracts.md (g), the exclusion list, and
// Library) and the agent files the spike's repository ignored (docs/design.md, "Why clone the
// whole checkout rather than check out and clone the Library"). It names those files rather than
// all of .claude, so .claude/worktrees stays unignored until `upool init` ignores it.
const gitignore = `/Library/
/Temp/
/Logs/
/obj/
/Builds/
/CLAUDE.md
/AGENTS.md
/.claude/settings.local.json
/.claude/skills/
/.agents/
`

// projectSettings is a ProjectSettings.asset cut down to the lines around scriptingDefineSymbols
// in testdata/captures/git/projectsettings-define-reorder.diff, with its symbols in the order
// that diff starts from.
const projectSettings = `%YAML 1.1
%TAG !u! tag:unity3d.com,2011:
--- !u!129 &1
PlayerSettings:
  webWasm2023: 0
  webEnableSubmoduleStrippingCompatibility: 0
  scriptingDefineSymbols:
    Standalone: SENTIS_ANALYTICS_ENABLED;APP_UI_EDITOR_ONLY;STEAMWORKS_NET
  additionalCompilerArguments: {}
  platformArchitecture: {}
`

// Env is one harness environment.
type Env struct {
	// Home is the temporary HOME: it holds an empty .config and no .upool.
	Home string
	// Repo is the repository's working tree, the main checkout.
	Repo string
	// BaseCommit is Base's commit, and Head the commit HEAD is at: the same commit unless
	// SecondCommit was given.
	BaseCommit string
	Head       string

	t   testing.TB
	env map[string]string
}

// Option changes the environment New makes.
type Option func(*options)

type options struct {
	stale, openEditor, uncommitted, secondCommit bool
}

// StaleLibrary gives PlayerScript an mtime newer than Library/ScriptAssemblies, StaleTime, as the
// spike's stale seed had (docs/design.md, "Slot lifecycle: seed only from a Library current at
// HEAD, refresh on handout, reset without `-x`"). Its content, and so git status, is
// unchanged.
func StaleLibrary() Option { return func(o *options) { o.stale = true } }

// OpenEditor writes EditorInstanceFile, as a running Editor holds it.
func OpenEditor() Option { return func(o *options) { o.openEditor = true } }

// UncommittedChanges edits the tracked GameScript and adds the untracked ScratchScript, both at
// ScriptTime, so the repository is dirty but its Library still current.
func UncommittedChanges() Option { return func(o *options) { o.uncommitted = true } }

// SecondCommit checks out Branch from Base and commits a change to PlayerScript there, at
// ScriptTime, so HEAD is one commit past base and the Library is current at HEAD.
func SecondCommit() Option { return func(o *options) { o.secondCommit = true } }

// New makes an environment: a temporary HOME and, beside it, a repository holding the fake
// project with one commit on Base, changed by opts. The options apply in a fixed order whatever
// order they are given in: SecondCommit, then UncommittedChanges, StaleLibrary and OpenEditor.
func New(t testing.TB, opts ...Option) *Env {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{
		Home: filepath.Join(root, "home"),
		Repo: filepath.Join(root, "project"),
		t:    t,
	}
	// PATH holds git alone, linked from this machine's, so that nothing a test runs reaches this
	// machine's unity or claude; a test that wants the fake unity puts it first on PATH.
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	e.env = map[string]string{
		"HOME": e.Home,
		"PATH": bin,
		// Isolated from this machine's git configuration: no system file, and the global one is
		// under Home, where there is none.
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "upool test",
		"GIT_AUTHOR_EMAIL":    "test@upool.invalid",
		"GIT_COMMITTER_NAME":  "upool test",
		"GIT_COMMITTER_EMAIL": "test@upool.invalid",
	}
	for _, dir := range []string{filepath.Join(e.Home, ".config"), e.Repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	e.Git("init", "-q", "-b", Base)
	e.WriteFile(".gitignore", gitignore)
	e.WriteFile("ProjectSettings/ProjectVersion.txt", "m_EditorVersion: "+EditorVersion+"\n")
	e.WriteFile("ProjectSettings/ProjectSettings.asset", projectSettings)
	// Each later change to a script changes its size too. The script keeps its mtime, and git
	// skips rehashing a file whose size and stat times match its index entry.
	e.writeScript(PlayerScript, script("Player", "Speed = 5f"))
	e.writeScript(GameScript, script("GameManager", "Level = 1f"))
	e.writeAgentFiles()
	e.writeLibrary()
	e.Git("add", "-A")
	e.Git("commit", "-q", "-m", "fake Unity project")
	e.BaseCommit = e.Git("rev-parse", "HEAD")
	e.Head = e.BaseCommit

	if o.secondCommit {
		e.Git("checkout", "-q", "-b", Branch)
		e.writeScript(PlayerScript, script("Player", "Speed = 12f"))
		e.Git("commit", "-q", "-am", "faster player")
		e.Head = e.Git("rev-parse", "HEAD")
	}
	if o.uncommitted {
		e.writeScript(GameScript, script("GameManager", "Level = 10f"))
		e.writeScript(ScratchScript, script("Scratch", "Value = 0f"))
	}
	if o.stale {
		e.SetMtime(PlayerScript, StaleTime)
	}
	if o.openEditor {
		e.WriteFile(EditorInstanceFile, fmt.Sprintf(`{"process_id": %d}`+"\n", EditorPID))
	}
	return e
}

// script is a MonoBehaviour called class with one field, field.
func script(class, field string) string {
	return "using UnityEngine;\n\npublic class " + class + " : MonoBehaviour\n{\n    public float " +
		field + ";\n}\n"
}

// writeAgentFiles writes the gitignored agent files the clone must carry (docs/design.md, "Why
// clone the whole checkout rather than check out and clone the Library") and the relative skill
// link (spike §10).
func (e *Env) writeAgentFiles() {
	e.t.Helper()
	e.WriteFile("CLAUDE.md", "# Fake project\n\nRead AGENTS.md.\n")
	e.WriteFile("AGENTS.md", "# Fake project\n\nAgent guidance for the fake project.\n")
	e.WriteFile(".claude/settings.local.json", "{}\n")
	e.WriteFile(".agents/skills/x/SKILL.md", "---\nname: x\ndescription: A fake skill.\n---\n")
	link := e.Path(SkillLink)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(SkillTarget, link); err != nil {
		e.t.Fatal(err)
	}
}

// writeLibrary writes the Library a closed Editor leaves: marker files, Pipeline with the one file
// that survives a clean exit (spike §2 T1.5), and ScriptAssemblies at LibraryTime.
func (e *Env) writeLibrary() {
	e.t.Helper()
	for _, name := range []string{"Library/ArtifactDB", "Library/SourceAssetDB",
		"Library/Pipeline/eval-usage.jsonl"} {
		e.WriteFile(name, "upool testenv marker\n")
	}
	const dll = "Library/ScriptAssemblies/Assembly-CSharp.dll"
	e.WriteFile(dll, "upool testenv marker\n")
	e.SetMtime(dll, LibraryTime)
	e.SetMtime("Library/ScriptAssemblies", LibraryTime)
}

// writeScript writes a script and gives it ScriptTime, so writing it does not make the Library
// stale.
func (e *Env) writeScript(name, content string) {
	e.t.Helper()
	e.WriteFile(name, content)
	e.SetMtime(name, ScriptTime)
}

// Path returns name, a slash-separated path relative to the repository, as an absolute path.
func (e *Env) Path(name string) string {
	return filepath.Join(e.Repo, filepath.FromSlash(name))
}

// WriteFile writes content to name, relative to the repository, creating its parent directories.
func (e *Env) WriteFile(name, content string) {
	e.t.Helper()
	path := e.Path(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// SetMtime sets the access and modification times of name, relative to the repository, to mtime.
// A directory's mtime changes whenever an entry is added to it, so set it after writing its
// files.
func (e *Env) SetMtime(name string, mtime time.Time) {
	e.t.Helper()
	if err := os.Chtimes(e.Path(name), mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

// Git runs `git -C <Repo> args…` through internal/git with the environment's variables, fails the
// test unless it exits 0, and returns its stdout with surrounding space trimmed.
func (e *Env) Git(args ...string) string {
	e.t.Helper()
	res, err := git.Exec{}.Run(context.Background(), e.Repo, e.Environ(), args...)
	if err == nil && res.ExitCode != 0 {
		err = errors.New(strings.TrimSpace(string(res.Stderr)))
	}
	if err != nil {
		e.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// Getenv looks up a variable in the environment, as run's getenv does: "" for one that is not
// set. Nothing is read from this process's environment except PATH, when New copies it.
func (e *Env) Getenv(key string) string {
	return e.env[key]
}

// Setenv sets a variable for Getenv and Environ, such as PATH to put a fake unity first.
func (e *Env) Setenv(key, value string) {
	e.env[key] = value
}

// Environ returns the environment as "KEY=value" entries, sorted by key, for a command.
func (e *Env) Environ() []string {
	keys := slices.Sorted(maps.Keys(e.env))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+e.env[k])
	}
	return out
}
