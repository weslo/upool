package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/assets"
	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/clone"
	"github.com/weslo/upool/internal/config"
	"github.com/weslo/upool/internal/git"
	"github.com/weslo/upool/internal/paths"
	"github.com/weslo/upool/internal/pool"
	"github.com/weslo/upool/internal/proc"
	"github.com/weslo/upool/internal/registry"
)

// initCommand is `upool init`, run from Terminal inside a Unity project's repository: it registers
// the project as a pool and clones its first slot, slot-1, from the main checkout
// (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `init` row;
// docs/contracts.md (f) transitions 1 and 2), and adds upool's instruction block to the
// repository's CLAUDE.md. It refuses an open Editor or a stale Library, which the first slot would
// seed from. --slots (#64) arrives with its own issue.
var initCommand = command{
	usage: "init [--json]",
	setup: func(*flag.FlagSet) handler { return initRepo },
}

const (
	// projectVersion is the file whose directory is a Unity project (docs/design.md, the
	// configuration paragraph: project detection). upool pools a project at its repository's root.
	projectVersion = "ProjectSettings/ProjectVersion.txt"
	// worktreesLine is the line init adds to .gitignore, so that slots under .claude/worktrees are
	// ignored (docs/contracts.md (g) Clone recipe, the preconditions).
	worktreesLine = ".claude/worktrees/"
	// worktreesProbe is the path under .claude/worktrees that init asks git check-ignore about. A
	// path under it, not the directory itself, so that a broader line such as the spike's
	// `.claude/` counts (docs/design.md, "One binary, a dozen commands, one JSON file per pool").
	worktreesProbe = ".claude/worktrees/slot-1"
	// originHEAD names origin's default branch, base's first default (A17).
	originHEAD = "refs/remotes/origin/HEAD"
	// claudeMD is the instruction file, at the repository's root, that init upserts upool's block
	// into (docs/design.md, "One binary, a dozen commands, one JSON file per pool", the `init` row).
	claudeMD = "CLAUDE.md"
)

// editorFiles are the files an open Editor holds in its Library. Both are absent after a clean
// exit (spike §2 T1.5), so either one means the Editor has the project open.
var editorFiles = []string{"Library/EditorInstance.json", "Library/ProtocolInstance.json"}

// The base_source values for a base that nothing set, which init chose (A17). A base that a
// layer set has that layer's config.Source instead.
const (
	baseFromOrigin = "origin_head"
	baseFromBranch = "current_branch"
)

// initReport is what init did: the data of its --json envelope and, in human form, one line per
// step.
type initReport struct {
	// Repo is the main checkout: the top level of the repository holding the working directory.
	Repo string `json:"repo"`
	// Key is the pool's <repo-key>, naming ~/.upool/pools/<key>/.
	Key string `json:"key"`
	// AlreadyRegistered is true when the repository was registered already, and init did nothing
	// else.
	AlreadyRegistered bool `json:"already_registered"`
	// Base is the pool's base, and BaseSource where it came from: the config layer that set it,
	// or origin_head or current_branch when none did (A17).
	Base       string `json:"base,omitempty"`
	BaseSource string `json:"base_source,omitempty"`
	// GitignoreUpdated is true when init added .claude/worktrees/ to .gitignore, and false when
	// git already ignored it.
	GitignoreUpdated bool `json:"gitignore_updated"`
	// RepoFileWritten is true when init wrote .upool.toml, and false when the file was there.
	RepoFileWritten bool `json:"repo_file_written"`
	// ClaudeMDBlock is what upserting upool's block into CLAUDE.md did.
	ClaudeMDBlock block.Action `json:"claude_md_block,omitempty"`
	// Slot is the first slot, which init cloned from the main checkout, and nil when it cloned
	// none.
	Slot *clonedSlot `json:"slot,omitempty"`
	// ExistingSlots are the slots the pool already had, from an earlier init whose registry entry
	// was lost or which failed part-way; init keeps them and clones none.
	ExistingSlots []existingSlot `json:"existing_slots,omitempty"`

	// repoFile is what init wrote to .upool.toml, for the human form.
	repoFile config.RepoFile
}

// clonedSlot is the slot init cloned, as state.json records it (docs/contracts.md (e)).
type clonedSlot struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// SeedHead is the main checkout's HEAD when it was copied, and SeedDirty whether it had
	// uncommitted changes, which the slot does not keep (docs/contracts.md (g) step 7).
	SeedHead  string `json:"seed_head"`
	SeedDirty bool   `json:"seed_dirty"`
}

// existingSlot is a slot init found in the pool and kept.
type existingSlot struct {
	Name  string     `json:"name"`
	State pool.State `json:"state"`
}

// Human returns one line per step init took.
func (r initReport) Human() string {
	if r.AlreadyRegistered {
		return fmt.Sprintf("%s is already registered as pool %s", r.Repo, r.Key)
	}
	var lines []string
	if r.GitignoreUpdated {
		lines = append(lines, "added "+worktreesLine+" to .gitignore")
	} else {
		lines = append(lines, worktreesLine+" is already ignored")
	}
	lines = append(lines, fmt.Sprintf("base is %s, %s", r.Base, describeBase(r.BaseSource)))
	if r.RepoFileWritten {
		f := r.repoFile
		lines = append(lines, fmt.Sprintf("wrote .upool.toml: ready = %d, max_slots = %d, "+
			"base = %q", f.Ready, f.MaxSlots, f.Base))
	} else {
		lines = append(lines, "kept the existing .upool.toml")
	}
	lines = append(lines, describeBlock(r.ClaudeMDBlock))
	if r.Slot != nil {
		lines = append(lines, fmt.Sprintf("cloned %s into %s", r.Slot.Name, r.Slot.Path))
	} else {
		var slots []string
		for _, s := range r.ExistingSlots {
			slots = append(slots, fmt.Sprintf("%s (%s)", s.Name, s.State))
		}
		lines = append(lines, "kept the pool's existing slots: "+strings.Join(slots, ", "))
	}
	lines = append(lines, fmt.Sprintf("registered %s as pool %s", r.Repo, r.Key))
	return strings.Join(lines, "\n")
}

// describeBlock says what upserting upool's block into CLAUDE.md did.
func describeBlock(a block.Action) string {
	switch a {
	case block.Created:
		return "created " + claudeMD + " with upool's block"
	case block.Inserted:
		return "added upool's block to " + claudeMD
	case block.Replaced:
		return fmt.Sprintf("updated upool's block in %s to v%d", claudeMD,
			assets.ClaudeMDBlockVersion)
	case block.KeptNewer:
		return fmt.Sprintf("kept the newer upool block in %s: this upool writes v%d", claudeMD,
			assets.ClaudeMDBlockVersion)
	}
	return claudeMD + " already has upool's block"
}

// describeBase says where a base came from, after "base is <base>, ".
func describeBase(source string) string {
	switch source {
	case baseFromOrigin:
		return "origin's default branch"
	case baseFromBranch:
		return "the current branch: origin has no default branch"
	case string(config.Env):
		return "from " + config.EnvName("base")
	case string(config.Repo):
		return "from .upool.toml"
	case string(config.User):
		return "from ~/.config/upool/config.toml"
	}
	return "from " + source
}

// initRepo is init's handler. It checks the repository before it changes anything, then upserts
// upool's block into CLAUDE.md, first so that a CLAUDE.md whose markers do not pair is refused
// with nothing written; adds .claude/worktrees/ to .gitignore, writes .upool.toml, creates the
// pool's state.json, clones the first slot into a pool that has none and, last, adds the
// repository to registry.json, so that a run that fails part-way is finished by the next rather
// than reported as already registered.
func initRepo(inv *invocation) (any, []string, error) {
	ctx := context.Background()
	wd, err := inv.getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("reading the working directory: %w", err)
	}
	g := git.Client{Runner: git.Exec{}, Env: inv.environ()}
	repo, err := g.TopLevel(ctx, wd)
	if err != nil {
		return nil, nil, fmt.Errorf("upool init runs inside a Unity project's git repository: %w",
			err)
	}
	if err := checkProject(ctx, g, repo); err != nil {
		return nil, nil, err
	}
	if reg, ok, err := registry.Lookup(inv.home, repo); err != nil {
		return nil, nil, err
	} else if ok {
		return initReport{Repo: repo, Key: reg.Key, AlreadyRegistered: true}, nil, nil
	}
	if err := checkLibraryUntracked(ctx, g, repo); err != nil {
		return nil, nil, err
	}
	if err := checkEditorClosed(repo); err != nil {
		return nil, nil, err
	}
	if err := checkLibraryCurrent(ctx, g, repo); err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(config.Inputs{Getenv: inv.getenv, Repo: repo, Home: inv.home})
	if err != nil {
		return nil, nil, err
	}
	r := initReport{Repo: repo}
	if r.Base, r.BaseSource, err = resolveBase(ctx, g, repo, cfg.Base); err != nil {
		return nil, nil, err
	}
	if err := checkBaseCommit(ctx, g, repo, r.Base, r.BaseSource); err != nil {
		return nil, nil, err
	}
	b := block.Block{Version: assets.ClaudeMDBlockVersion, Body: assets.ClaudeMDBlock}
	if r.ClaudeMDBlock, err = block.Upsert(filepath.Join(repo, claudeMD), b); err != nil {
		return nil, nil, err
	}
	if r.GitignoreUpdated, err = ignoreWorktrees(ctx, g, repo); err != nil {
		return nil, nil, err
	}
	r.repoFile = config.RepoFile{Ready: cfg.Ready.Value, MaxSlots: cfg.MaxSlots.Value, Base: r.Base}
	if r.RepoFileWritten, err = config.WriteRepoFile(repo, r.repoFile); err != nil {
		return nil, nil, err
	}
	key, existing, err := createPool(ctx, inv.home, repo, r.Base)
	if err != nil {
		return nil, nil, err
	}
	if len(existing) > 0 {
		r.ExistingSlots = existing
	} else if r.Slot, err = cloneFirstSlot(ctx, inv, g, key, repo); err != nil {
		return nil, nil, err
	}
	reg, err := registry.Add(ctx, inv.home, repo)
	if err != nil {
		return nil, nil, err
	}
	r.Key = reg.Key
	return r, nil, nil
}

// checkProject refuses repo unless it is a Unity project at its root. A project in a subdirectory
// is named in the message, since that is the likeliest reason for the refusal.
func checkProject(ctx context.Context, g git.Client, repo string) error {
	file := filepath.Join(repo, filepath.FromSlash(projectVersion))
	info, err := os.Stat(file)
	if err == nil && info.Mode().IsRegular() {
		return nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking for %s: %w", file, err)
	}
	nested, err := g.LsFiles(ctx, repo, ":(glob)**/"+projectVersion)
	if err != nil {
		return err
	}
	if len(nested) > 0 {
		project := filepath.Join(repo, filepath.FromSlash(path.Dir(path.Dir(nested[0]))))
		return fmt.Errorf("the Unity project %s is not at the root of its git repository, %s: "+
			"upool pools only a project at the root", project, repo)
	}
	return fmt.Errorf("%s is not a Unity project: it has no %s", repo, projectVersion)
}

// checkLibraryUntracked refuses repo if git tracks anything under Library/: Unity's own check, and
// a precondition of resetting slots with `git clean -fd`, which keeps Library/ only because it is
// ignored (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD, refresh on
// handout, reset without `-x`").
func checkLibraryUntracked(ctx context.Context, g git.Client, repo string) error {
	tracked, err := g.LsFiles(ctx, repo, "Library/")
	if err != nil {
		return err
	}
	if len(tracked) == 0 {
		return nil
	}
	files := "files"
	if len(tracked) == 1 {
		files = "file"
	}
	return fmt.Errorf("git tracks %d %s under Library/ in %s, such as %s: untrack Library/ "+
		"(git rm -r --cached Library) and ignore it, since Unity regenerates it",
		len(tracked), files, repo, tracked[0])
}

// checkEditorClosed refuses repo while an Editor has it open, which init detects by the instance
// files in its Library (docs/design.md, "Slot lifecycle: seed only from a Library current at HEAD,
// refresh on handout, reset without `-x`").
func checkEditorClosed(repo string) error {
	for _, name := range editorFiles {
		file := filepath.Join(repo, filepath.FromSlash(name))
		_, err := os.Lstat(file)
		if err == nil {
			return fmt.Errorf("the Unity Editor has %s open (%s exists): quit it, then run "+
				"upool init again", repo, name)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checking for %s: %w", file, err)
		}
	}
	return nil
}

// checkLibraryCurrent refuses repo while its Library is stale: the first slot seeds from the main
// checkout, and every clone of a stale Library recompiles what the seed never did
// (docs/contracts.md (g) Clone recipe, steps 0 and 1). The recipe would refresh the seed with a
// batch open; until the stale-seed refresh arrives in M4, init asks the user to.
func checkLibraryCurrent(ctx context.Context, g git.Client, repo string) error {
	s, err := clone.CheckStaleness(ctx, g, repo)
	if err != nil {
		return err
	}
	if !s.Stale() {
		return nil
	}
	return fmt.Errorf("the Library in %s is stale: %s: open the project in Unity, let it finish "+
		"importing and compiling, quit it, then run upool init again", repo, s.Reason())
}

// resolveBase returns the pool's base and where it came from: the configured base if a layer set
// it, else origin's default branch without its origin/ prefix, else the current branch (A17).
func resolveBase(ctx context.Context, g git.Client, repo string,
	configured config.Setting[string]) (base, source string, err error) {
	if configured.Set() {
		return configured.Value, string(configured.Source), nil
	}
	ref, ok, err := g.SymbolicRef(ctx, repo, originHEAD)
	if err != nil {
		return "", "", err
	}
	if ok {
		return strings.TrimPrefix(ref, "origin/"), baseFromOrigin, nil
	}
	branch, ok, err := g.SymbolicRef(ctx, repo, "HEAD")
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("choosing a base for %s: origin has no default branch and HEAD "+
			"is detached; set base in %s", repo, config.RepoPath(repo))
	}
	return branch, baseFromBranch, nil
}

// checkBaseCommit refuses a base that names no commit in repo, saying where it came from: the
// first slot is moved to base with `git checkout --detach <base>` (docs/contracts.md (g) Clone
// recipe, step 7), so a clone would get as far as copying the checkout and fail there, after init
// had written its files.
func checkBaseCommit(ctx context.Context, g git.Client, repo, base, source string) error {
	if _, err := g.Commit(ctx, repo, base); err != nil {
		return fmt.Errorf("base is %s, %s, which names no commit in %s, so no slot can be "+
			"cloned at it: create the branch, or set base to one that exists: %w", base,
			describeBase(source), repo, err)
	}
	return nil
}

// ignoreWorktrees appends worktreesLine to repo's .gitignore, creating the file if need be, unless
// git already ignores a path under .claude/worktrees, and reports whether it did.
func ignoreWorktrees(ctx context.Context, g git.Client, repo string) (bool, error) {
	ignored, err := g.CheckIgnore(ctx, repo, worktreesProbe)
	if err != nil || ignored {
		return false, err
	}
	file := filepath.Join(repo, ".gitignore")
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("reading %s: %w", file, err)
	}
	line := worktreesLine + "\n"
	if len(old) > 0 && old[len(old)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return false, fmt.Errorf("opening %s: %w", file, err)
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close() // the write's error is the one worth reporting
		return false, fmt.Errorf("writing %s: %w", file, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("closing %s: %w", file, err)
	}
	return true, nil
}

// createPool creates ~/.upool/pools/<repo-key>/state.json for repo, with its base and no slots
// (docs/contracts.md (e) State schemas), and returns the key and the slots the pool already has.
// A state.json already there for repo is kept as it is: the key is derived from the path so that
// init finds an existing pool again after the registry is lost (A7). One that belongs to another
// repository is refused, since the two would share a pool.
func createPool(ctx context.Context, home, repo, base string) (key string,
	slots []existingSlot, err error) {
	canonical, err := paths.Key(repo)
	if err != nil {
		return "", nil, err
	}
	key = registry.Key(canonical)
	err = pool.Update(ctx, home, key, func(p *pool.Pool) error {
		if p.Repo == "" {
			p.Repo, p.Base = repo, base
			return nil
		}
		existing, err := paths.Key(p.Repo)
		if err != nil {
			return err
		}
		if existing != canonical {
			return fmt.Errorf("~/.upool/pools/%s/state.json is the pool of %s, not %s", key,
				p.Repo, repo)
		}
		for _, s := range p.Slots {
			slots = append(slots, existingSlot{Name: s.Name, State: s.State})
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return key, slots, nil
}

// cloneFirstSlot clones slot-1 into the pool key from the main checkout, repo, which init has
// checked has no Editor open and a current Library (docs/contracts.md (g) Clone recipe, step 0:
// init's first slot seeds from the main checkout with its Editor closed).
func cloneFirstSlot(ctx context.Context, inv *invocation, g git.Client, key,
	repo string) (*clonedSlot, error) {
	recipe := clone.Recipe{Git: g, Copier: clone.CP{}, Proc: proc.System{}, OwnerPID: inv.pid}
	s, err := recipe.Clone(ctx, inv.home, key, repo)
	if err != nil {
		return nil, err
	}
	return &clonedSlot{Name: s.Name, Path: s.Path, SeedHead: s.SeedHead,
		SeedDirty: s.SeedDirty}, nil
}
