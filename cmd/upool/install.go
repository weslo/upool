package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/weslo/upool/internal/block"
	"github.com/weslo/upool/internal/git"
)

// What init writes into a project, which is only the hooks it registers there, and what it takes
// out of the agent files an earlier upool wrote into (A43; docs/design.md, "Hooks route, skills
// teach, leases and lanes keep agents from colliding", the three layers), and the helpers for both.

// protectedFolders are the folders of a home directory that the desktop app's helper, which runs
// the hooks, cannot read a file in: under ~/Documents, `bash` could not open a script and `head`
// failed with EPERM, though a binary there ran (docs/contracts.md (a), "WorktreeCreate output and
// app behaviour"; spike §7, item 1, names all three).
var protectedFolders = []string{"Documents", "Desktop", "Downloads"}

// upoolName is the name init looks for upool by on PATH.
const upoolName = "upool"

// skillFiles are where an earlier upool's init installed the skill, relative to the repository:
// Claude Code's project skills, and the shared directory other agents read skills from. upool no
// longer installs it, and retireAgentFiles and deinit take it out (A43).
var skillFiles = []string{".claude/skills/upool/SKILL.md", ".agents/skills/upool/SKILL.md"}

// change is what init did to one file.
type change string

const (
	created   change = "created"
	updated   change = "updated"
	unchanged change = "unchanged"
)

// installed is one file init manages, and what it did to it.
type installed struct {
	Path   string `json:"path"`
	Change change `json:"change"`
}

// describeChange says what init did to i.
func describeChange(i installed) string {
	if i.Change == unchanged {
		return i.Path + " is up to date"
	}
	return string(i.Change) + " " + i.Path
}

// checkHomeReachable refuses a home directory that is, or is inside, a folder named in
// protectedFolders once its symlinks are resolved: ~/.upool would then be under one, where the
// helper could not read the state the hooks need (#66). ~ in ~/Documents is the account's home,
// which may not be $HOME, so any such folder counts.
func checkHomeReachable(home string) error {
	if !filepath.IsAbs(home) {
		return fmt.Errorf("home directory %q is not an absolute path", home)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolving the home directory: %w", err)
	}
	if dir, ok := protectedFolder(resolved); ok {
		return fmt.Errorf("the home directory %s is in %s, which the desktop app's hook helper "+
			"cannot read from, so the hooks could not read upool's state under ~/.upool: run "+
			"upool init with HOME outside %s", resolved, dir,
			strings.Join(protectedFolders, ", "))
	}
	return nil
}

// protectedFolder returns the folder named in protectedFolders that path, symlinks resolved, is
// or is inside, if there is one.
func protectedFolder(path string) (string, bool) {
	for dir := path; ; dir = filepath.Dir(dir) {
		for _, name := range protectedFolders {
			// APFS is case-insensitive by default, so documents is the same folder.
			if strings.EqualFold(filepath.Base(dir), name) {
				return dir, true
			}
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

// findOnPath returns the first upool in pathList, a PATH value, that is the binary at exe: the
// path as PATH spells it, not the one its symlinks resolve to. For Homebrew that is
// /opt/homebrew/bin/upool rather than a Cellar path, so `brew upgrade` keeps the hooks working
// (#66); PATH under the helper is not the user's, which is why the hooks name a path at all
// (docs/design.md, "Repository, CI and Homebrew distribution"). An entry that is not absolute is
// skipped, since the helper does not run the hooks from the directory init ran in.
func findOnPath(pathList, exe string) (string, error) {
	self, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("finding upool's own binary: %w", err)
	}
	for _, dir := range filepath.SplitList(pathList) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, upoolName)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && os.SameFile(info, self) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no %s on PATH is this binary, %s, and the hooks run upool by the "+
		"path PATH finds it at: run the upool on PATH, or put this one's directory on PATH",
		upoolName, exe)
}

// retired is something of an earlier upool's that init took out of the project's agent files.
type retired struct {
	Path string `json:"path"`
	// What is "block", upool's marked block, or "skill", the upool skill's SKILL.md.
	What string `json:"what"`
}

// retireAgentFiles takes out of the project at repo what an earlier upool's init wrote into its
// agent files, which upool no longer writes, since they are the project authors' (docs/contracts.md
// A43; #230): its marked block from CLAUDE.md and AGENTS.md, deleting a file the block alone
// filled, and its SKILL.md from each of skillFiles, with the directory when nothing else is in it.
// It never writes into a file git tracks: a block or a skill there stays, with a warning naming it,
// so that its owner takes it out in a commit. A file whose markers do not pair is left as it is,
// with a warning. It returns what it took out and the warnings.
func retireAgentFiles(ctx context.Context, g git.Client, repo string) ([]retired, []string,
	error) {
	var out []retired
	var warnings []string
	for _, name := range []string{claudeMD, agentsMD} {
		file := filepath.Join(repo, name)
		doc, err := os.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", file, err)
		}
		_, found, err := block.Strip(doc)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("left %s as it is, since its upool markers "+
				"do not pair: %v", file, err))
			continue
		}
		if !found {
			continue
		}
		isTracked, err := tracked(ctx, g, repo, name)
		if err != nil {
			return nil, nil, err
		}
		if isTracked {
			warnings = append(warnings, trackedWarning(file, "the block"))
			continue
		}
		if _, err := block.Remove(file); err != nil {
			return nil, nil, err
		}
		out = append(out, retired{Path: file, What: "block"})
	}
	for _, name := range skillFiles {
		file := filepath.Join(repo, filepath.FromSlash(name))
		ours, err := upoolSkill(file)
		if err != nil || !ours {
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		isTracked, err := tracked(ctx, g, repo, name)
		if err != nil {
			return nil, nil, err
		}
		if isTracked {
			warnings = append(warnings, trackedWarning(file, "the upool skill"))
			continue
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("removing %s: %w", file, err)
		}
		if err := removeSkillDir(filepath.Dir(file)); err != nil {
			return nil, nil, err
		}
		out = append(out, retired{Path: file, What: "skill"})
	}
	return out, warnings, nil
}

// trackedWarning says that file, which git tracks, still holds what, written by an earlier upool,
// and that upool leaves it there (A43).
func trackedWarning(file, what string) string {
	return fmt.Sprintf("%s, which git tracks, still holds %s an earlier upool wrote: upool no "+
		"longer writes it and never writes into a tracked file, so take it out in a commit",
		file, what)
}

// tracked reports whether git tracks the file at rel, relative to repo, or the file it names once
// its symlinks are resolved: CLAUDE.md is often a link to AGENTS.md, and .claude/skills to
// .agents/skills, and `git ls-files` names nothing past a link. A file that resolves outside repo
// is judged by rel alone. Names match in any case, since APFS opens a tracked claude.md as
// CLAUDE.md, while git matches a pathspec by its case.
func tracked(ctx context.Context, g git.Client, repo, rel string) (bool, error) {
	files, err := g.LsFiles(ctx, repo, trackedSpec+rel)
	if err != nil || len(files) > 0 {
		return len(files) > 0, err
	}
	top, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", repo, err)
	}
	real, err := filepath.EvalSymlinks(filepath.Join(repo, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", filepath.Join(repo, rel), err)
	}
	realRel, err := filepath.Rel(top, real)
	if err != nil || !filepath.IsLocal(realRel) || strings.EqualFold(filepath.ToSlash(realRel),
		rel) {
		return false, nil
	}
	files, err = g.LsFiles(ctx, repo, trackedSpec+filepath.ToSlash(realRel))
	return len(files) > 0, err
}

// trackedSpec is the pathspec magic tracked asks git with: the path as it is, glob characters and
// all, in any case.
const trackedSpec = ":(literal,icase)"

// removeSkillDir removes dir, the directory of a SKILL.md upool has just removed, if nothing else is
// in it. A link is never removed, since the project made it: the directory it resolves to is
// removed instead, under the same rule.
func removeSkillDir(dir string) error {
	real, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolving %s: %w", dir, err)
	}
	// Remove fails on a directory that still holds anything else, which then stays.
	_ = os.Remove(real)
	return nil
}

// upoolSkill reports whether file is a SKILL.md an upool wrote: one whose front matter names the
// skill upool, as assets.Skill's does. A file that is missing is not.
func upoolSkill(file string) (bool, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", file, err)
	}
	front, ok := strings.CutPrefix(string(b), "---\n")
	if !ok {
		return false, nil
	}
	front, _, ok = strings.Cut(front, "\n---\n")
	if !ok {
		return false, nil
	}
	for line := range strings.Lines(front) {
		if strings.TrimSpace(line) == "name: upool" {
			return true, nil
		}
	}
	return false, nil
}

// writeAtomic writes data to file with mode. The write goes to a temporary file beside it that is
// renamed over it, so a hook or a Claude Code session that reads it meanwhile reads the old file
// or the new one, never half of either.
func writeAtomic(file string, data []byte, mode fs.FileMode) error {
	tmp, err := writeTemp(file, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		// A temporary file that cannot be removed is no reason to hide the error that got here.
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s to %s: %w", tmp, file, err)
	}
	return nil
}

// writeTemp writes data with mode to a new temporary file beside file, synced, and returns its
// name.
func writeTemp(file string, data []byte, mode fs.FileMode) (_ string, err error) {
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp*")
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", file, err)
	}
	defer func() {
		if err != nil {
			// Close fails harmlessly if the file is already closed, and a temporary file that
			// cannot be removed is no reason to hide the error that got here.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return "", fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	// Chmod, not CreateTemp's mode, which is 0600, and not a mode the umask can narrow.
	if err := tmp.Chmod(mode); err != nil {
		return "", fmt.Errorf("setting the mode of %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("syncing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	return tmp.Name(), nil
}
